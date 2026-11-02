# ADR-0030: El balanceo entre réplicas del motor es de la infraestructura (ALB), no de la aplicación

- **Estado:** Aceptada
- **Fecha:** 2026-11-01
- **Etapa:** 7.6 (nube, Fase 2: el sistema distribuido en AWS)

## Contexto

En la Fase 2 el motor corre en **varias réplicas**, cada una en su máquina
([ADR-0018](0018-evaluar-en-distintos-entornos.md)). Sus clientes son el
gateway de la demo ([ADR-0029](0029-demo-interactiva-y-bloqueo-automatico.md))
y el load generator ([ADR-0014](0014-load-generator-de-lazo-abierto.md)).
Alguien tiene que repartir las transacciones entre las réplicas.

El problema concreto es gRPC. Va sobre **HTTP/2**: el cliente abre **una
sola conexión** y multiplexa por ella todas las requests concurrentes. Por
defecto (política `pick_first`), aunque el nombre del motor resuelva a varias
IPs, el cliente se conecta a la primera y le manda **todo**. Un balanceador
que reparte **conexiones** tampoco alcanza: hay una sola conexión, así que
termina todo en una réplica, y las que suma el auto scaling quedan sin
tráfico. Hace falta repartir **por request**.

Hay dos lugares donde se puede hacer eso, y es una pregunta de diseño de
fondo: **¿de qué lado del ciclo de vida del software vive esa complejidad?**

- **En la aplicación (desarrollo):** cada cliente sabe que hay varias
  réplicas, las descubre y reparte.
- **En la infraestructura (operación / plataforma):** el cliente le habla a
  **un nombre** y no sabe cuántas réplicas hay detrás. La plataforma
  reparte, controla la salud y suma o saca réplicas.

Restricciones que pesan:

- **Tiempo real:** cada salto de red consume presupuesto del deadline de
  200ms. El motor lo ve como un D<sub>rem</sub> más chico al llegar
  ([ADR-0003](0003-control-de-admision-temprano.md)).
- **El mismo artefacto en todos los entornos**
  ([ADR-0017](0017-configuracion-por-entorno-y-apagado-ordenado.md),
  [ADR-0018](0018-evaluar-en-distintos-entornos.md)): la imagen que corre en
  la notebook tiene que ser la misma que corre en AWS.
- **Microservicios:** el sistema ya tiene cinco servicios propios, Redis y
  Kafka. Cada responsabilidad que se agrega a cada cliente se multiplica.
- **Costo:** la infraestructura es efímera (se crea para la sesión y se
  destruye), y lo que se cobra aunque esté apagada pesa más que lo que se
  cobra por hora.

## Decisión

**El balanceo lo hace la infraestructura: un Application Load Balancer
(ALB) interno delante de las réplicas del motor.** La aplicación no sabe
cuántas réplicas hay.

1. **ALB interno con target group de protocolo gRPC.** El ALB termina el
   HTTP/2 del cliente y reparte **cada request** entre las réplicas sanas.
   Hacia las réplicas habla gRPC **sin TLS** (lo permite AWS): el motor no
   cambia.
2. **Salud:** el health check del target group es el servicio estándar
   `grpc.health.v1.Health/Check` que el motor ya expone
   ([ADR-0020](0020-imagenes-salud-y-compose.md)), y espera el código gRPC
   `0` (OK). El *deregistration delay* se alinea con el apagado ordenado
   ([ADR-0017](0017-configuracion-por-entorno-y-apagado-ordenado.md)): una
   réplica que se baja deja de recibir requests nuevas y termina las que
   tiene en vuelo.
3. **TLS con una autoridad certificante propia, sin dominio.** El ALB solo
   acepta gRPC por HTTPS. Terraform genera una autoridad y un certificado
   para `motor.drts.internal` (zona privada de Route 53, apuntando al ALB) y
   lo importa en ACM. El gateway y el load generator **confían solo en esa
   autoridad** y verifican el nombre: es TLS verificado de verdad, no "TLS
   sin verificar".
4. **Lo único que cambia en la aplicación es configuración:** el cliente
   recibe la ruta de la autoridad (`MOTOR_CA` en el gateway, `-ca` en el
   load generator; `servicio.CredencialesCliente`). Sin ella, se conecta sin
   TLS, como en la notebook y en Docker Compose. El mismo binario sirve en
   los dos casos.
5. **Entre el motor y Settlement no hay balanceador:** Settlement tiene una
   sola réplica.

## Alternativas consideradas

### Balanceo del lado del cliente (se implementó y se sacó)

El motor se registra en un DNS con una IP por réplica; el cliente usa la
política `round_robin` de gRPC: abre una conexión a cada réplica y reparte
cada request. Para encontrar las réplicas nuevas, el servidor vence las
conexiones cada tanto (`MaxConnectionAge`) y el cliente vuelve a resolver
el DNS.

- **A favor:** es la opción **más rápida** (sin salto extra: el cliente va
  directo a la réplica) y **gratis**.
- **En contra, y por qué se descartó:**
  - **Mete conocimiento de la topología en cada cliente.** El desarrollador
    de cada servicio tiene que entender descubrimiento de servicios,
    políticas de balanceo, backoff de reconexión y vencimiento de
    conexiones. En un sistema de microservicios eso se repite en cada
    cliente y en cada lenguaje.
  - **Acopla el servidor al mecanismo:** el motor necesitaba
    `MaxConnectionAge` solo para que los clientes re-resolvieran el DNS.
  - **La reacción depende del DNS:** el TTL y el backoff del cliente
    deciden cuánto tarda en dejar de mandarle a una réplica caída o en
    usar una nueva. Con el ALB, eso lo decide el health check, en un solo
    lugar.
  - **Pierde la vista central:** el ALB da requests por réplica
    (`RequestCountPerTarget`), la señal natural para el auto scaling.

En la industria, el balanceo del lado del cliente a escala existe (gRPC
*proxyless* con xDS, *service mesh*), pero no lo escribe el desarrollador de
cada servicio: lo maneja un **plano de control** de la plataforma. Es decir,
aun ahí la complejidad vive en la infraestructura. Un plano de control está
fuera del alcance de este proyecto.

### Envoy como sidecar en la máquina de los clientes

*(Agregada el 2026-11-01, después de tomar la decisión: no la cambia, la
refuerza.)*

**Qué es Envoy:** un proxy de capa 7 de código abierto (CNCF), el que usan
por debajo los *service mesh* como Istio. Entiende HTTP/2 y gRPC, así que
reparte **por request** (como el ALB), hace health checks gRPC y no exige
TLS.

**Cómo sería:** un contenedor de Envoy en la máquina del borde, al lado del
gateway y del load generator. Los clientes le hablan a `localhost:50051`
sin TLS y Envoy reparte entre `motor-1` y `motor-2`, que encuentra por DNS.
Desaparecen el ALB, el certificado y la subred de la segunda zona.

- **A favor:**
  - Sigue siendo balanceo de la infraestructura, transparente para la
    aplicación: es la versión mínima del patrón *sidecar* de un service
    mesh, sin plano de control.
  - No hay salto extra por la red (el tramo cliente → Envoy queda dentro
    de la máquina), así que probablemente sea más rápido que el ALB.
  - No necesita TLS ni certificado, y como sidecar no cuesta nada.
- **En contra, y por qué se descartó:** **no acompaña al auto scaling.**
  El destino de los motores es escalar solos (un ASG o ECS), y ahí el
  problema no es repartir sino **descubrir** las réplicas que aparecen y
  desaparecen:
  - **Registro:** con el ALB, el ASG anota cada instancia nueva en el
    target group, automáticamente. Con Envoy, los registros DNS son fijos
    (`motor-1`, `motor-2`): habría que armar el descubrimiento a mano
    (Cloud Map, un script que actualice el DNS al crear o borrar cada
    instancia, o un plano de control).
  - **Métrica para escalar:** el ALB da **requests por réplica**
    (`RequestCountPerTarget`), la señal que anticipa la saturación del
    motor. Con Envoy quedaría la CPU, o exportar sus métricas a CloudWatch
    a mano.
  - **Operación:** la configuración de Envoy la escribimos y mantenemos
    nosotros; el ALB lo opera AWS (alta disponibilidad incluida).

  O sea: Envoy resuelve bien el balanceo con un número **fijo** de
  réplicas, pero la integración ALB + ASG es justamente lo que costaría
  construir a mano. Ese descubrimiento dinámico es el plano de control que
  quedó fuera de alcance.

**Envoy queda descartado para todo el diseño** (no se usa ni se mide):
surgió como una consulta y se registra acá para dejar el porqué.

### Network Load Balancer (NLB)

Más rápido y barato que el ALB (capa 4), pero reparte **conexiones**, no
requests. Con un cliente que abre una sola conexión HTTP/2, manda todo a
una réplica. Solo funciona con varias conexiones por cliente y vencimiento
de conexiones en el servidor: vuelve a meter el mecanismo en la aplicación.

### ALB con un dominio propio y un certificado público

Lo "de producción" cuando del otro lado hay navegadores u otros clientes
que no controlamos. Acá los que le hablan al ALB son **nuestros propios
servicios**, así que una autoridad pública no aporta nada. Y un dominio se
paga **por año, por adelantado** (~$13–15 un `.com`), aunque la
infraestructura esté destruida: es el único costo del proyecto que no es
por hora de uso.

### ALB con certificado autofirmado y el cliente sin verificar

Igual de gratis que la autoridad propia, pero el cliente acepta cualquier
certificado: cifra sin autenticar al servidor. Con la autoridad propia el
costo es el mismo y la verificación es real.

## Consecuencias

- **La aplicación queda simple y portátil:** el cliente le habla a un
  nombre. En la notebook ese nombre es el motor; en AWS, el ALB. Agregar o
  sacar réplicas no toca código ni configuración de los clientes.
- **Un salto más, que se mide.** El ALB agrega latencia a cada transacción
  (del orden de milisegundos, a medir). Sale del mismo presupuesto de 200ms
  y aparece en dos lugares: la latencia del cliente (la métrica oficial,
  [ADR-0028](0028-metricas-de-evaluacion-sli-slo-sla.md)) y el
  D<sub>rem</sub> con el que llega la transacción al motor. **Experimento:**
  el mismo perfil de carga contra un motor directo y contra el ALB; la
  diferencia es **el precio de que el balanceo sea transparente**. Es el
  mismo tipo de comparación que la evaluación por entornos: cuánto cuesta
  distribuir.
- **El deadline atraviesa el ALB:** gRPC lo propaga en el header
  `grpc-timeout`, que el ALB reenvía. El motor sigue calculando
  D<sub>rem</sub> con el deadline que le llega.
- **Costo:** el ALB se cobra por hora mientras existe (~$0,02–0,03 más el
  tráfico). Se crea y se destruye con el resto de la sesión. Sin dominio,
  no queda ningún costo fijo anual.
- **Requisitos de AWS que condicionan la red:** el ALB necesita subredes en
  **dos zonas de disponibilidad**, aunque los motores estén en una.
- **El certificado es efímero:** lo genera Terraform en cada sesión, con
  una validez corta. No hay rotación que mantener; la clave privada vive en
  el state local de Terraform (que no se versiona).
- **Queda abierto:** si en un entorno sin ALB (por ejemplo, k3s) hiciera
  falta repartir entre réplicas, el equivalente es el Service de Kubernetes
  o un ingress: de nuevo, la plataforma, no la aplicación.
