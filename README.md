# D-RTS — Detección de fraude en tiempo real con deadline duro

**D-RTS** (*Distributed Real-Time Fraud Detection System*) es un motor de
autorización de pagos que decide si aprueba o rechaza cada transacción bajo un
**deadline duro de 200ms de extremo a extremo**. Está escrito en Go, sobre
gRPC y Redis, con un segundo servicio (Settlement), Kafka como columna de
eventos (auditoría, alertas, estadísticas), un generador de carga propio y
observabilidad con Prometheus y Grafana. Corre en binarios nativos y en
Docker Compose, y está diseñado para medirse también en AWS.

Es el proyecto de la materia *Sistemas en Tiempo Real*. Cada decisión de
diseño está registrada como un ADR en [`docs/adr/`](docs/adr/README.md).

| Para… | Leé |
|---|---|
| entender el sistema paso a paso y dónde se evalúa el tiempo real | [FLUJO-DEL-SISTEMA.md](FLUJO-DEL-SISTEMA.md) |
| saber cómo se evalúa (métricas oficiales, SLI, SLO) | [ADR-0028](docs/adr/0028-metricas-de-evaluacion-sli-slo-sla.md) |
| leer el dashboard y diagnosticar con él | [observabilidad/GUIA-DASHBOARD.md](observabilidad/GUIA-DASHBOARD.md) |
| ver la arquitectura en la nube | [En la nube: AWS](#en-la-nube-aws-propuesta) |

---

## Índice

- [¿Por qué este proyecto?](#por-qué-este-proyecto)
- [El modelo de tiempo real](#el-modelo-de-tiempo-real)
- [Arquitectura](#arquitectura)
- [Recorrido de una transacción](#recorrido-de-una-transacción)
- [Componentes](#componentes)
- [Estructura del repositorio y por qué](#estructura-del-repositorio-y-por-qué)
- [Decisiones de diseño (ADRs)](#decisiones-de-diseño-adrs)
- [Cómo se evalúa](#cómo-se-evalúa)
- [Lo que mostraron las mediciones](#lo-que-mostraron-las-mediciones)
- [En la nube: AWS (propuesta)](#en-la-nube-aws-propuesta)
- [Cómo correrlo](#cómo-correrlo)
- [Configuración](#configuración)
- [Observabilidad](#observabilidad)
- [Tests](#tests)
- [Estado y próximos pasos](#estado-y-próximos-pasos)
- [Referencias](#referencias)

---

## ¿Por qué este proyecto?

En un sistema *best-effort* una respuesta lenta es solo una respuesta lenta.
En un sistema de **tiempo real duro**, una respuesta correcta que llega tarde
**es una falla**. Autorizar un pago es un buen ejemplo: la red de tarjetas le
da al emisor un plazo, y si el motor de fraude no decide a tiempo, la
transacción se pierde o se decide sin él.

El proyecto explora tres preguntas:

1. **¿Cómo se diseña un motor que priorice cumplir el deadline?** Rechazando
   temprano lo que no va a llegar (*load shedding*) y separando el trabajo en
   una parte obligatoria y una opcional que se saltea si no hay tiempo
   (*Computación Imprecisa*, Liu et al., 1991).
2. **¿Qué pasa con ese diseño bajo carga real y con fallas reales?** Redis
   caído, un segundo servicio lento, timeouts cuyo resultado es desconocido,
   saturación de CPU.
3. **¿Qué tan estables son la latencia y el cumplimiento del deadline según
   dónde corre el sistema?** El mismo artefacto se mide en binarios nativos,
   en Docker Compose y en la nube ([ADR-0018](docs/adr/0018-evaluar-en-distintos-entornos.md)).

Todo corre sobre **infraestructura real**: no hay Redis falsos ni mocks
([ADR-0016](docs/adr/0016-tests-contra-infraestructura-real.md)).

## El modelo de tiempo real

Cada transacción es $T_i = (a_i, M_i, O_i, D_i)$, con deadline
$D_i = a_i + 200\,ms$.

| Pieza | Qué es | Presupuesto de diseño |
|---|---|---|
| **Fase mandatoria** $M_i$ | blacklist, control de velocidad (más de 3 intentos en 10s = *card testing*), viaje imposible (más de 800 km/h entre dos compras) | $WCET(M) = 20\,ms$ |
| **Fase opcional** $O_i$ | scoring de riesgo: Z-score sobre $\ln(\text{monto})$ ponderado por categoría de comercio | $WCET(O) = 80\,ms$ |
| **Settlement + red** | registrar el resultado en el segundo servicio | $C_{settle} + C_{net} = 15\,ms$ |

Dos decisiones temporales gobiernan cada transacción:

- **Admisión** (al llegar, antes de cualquier trabajo):
  $D_{rem} = deadline - ahora$. Si $D_{rem} < WCET(M) + C_{settle} + C_{net}$
  (35ms), se rechaza en el acto
  ([ADR-0003](docs/adr/0003-control-de-admision-temprano.md)).
- **Slack** (después de la mandatoria):
  $S_i = D_{rem} - C_{settle} - C_{net} - WCET(O)$. Si $S_i \geq 0$ corre el
  scoring; si no, la respuesta sale solo con la mandatoria y marcada como
  degradada ([ADR-0004](docs/adr/0004-computacion-imprecisa.md)).

Los presupuestos son **puntos de partida de diseño**, no mediciones: se
recalibran midiendo en cada entorno. Viven en un solo lugar,
[`internal/presupuesto`](internal/presupuesto/presupuesto.go), para que la
admisión y el slack no puedan contradecirse.

## Arquitectura

> El recorrido completo de una transacción, paso por paso, y **dónde y cómo
> se evalúa el tiempo real** en cada módulo:
> [FLUJO-DEL-SISTEMA.md](FLUJO-DEL-SISTEMA.md).

El sistema se lee en tres capas, en el orden en que las recorre una
transacción. Las flechas llenas son sincrónicas (cuentan contra el deadline);
las punteadas son asíncronas (no lo tocan).

```mermaid
flowchart TB
    subgraph hot["① Camino de decisión · sincrónico · ≤ 200ms de extremo a extremo"]
        direction LR
        C(["Cliente<br/>(loadgen)"])
        FE["<b>Fraud-Engine</b><br/>admisión → mandatoria<br/>→ slack → opcional"]
        ST["<b>Settlement</b><br/>registro idempotente"]
        C <== "gRPC · deadline 200ms<br/>request ⇄ decisión" ==> FE
        FE == "gRPC · timeout 15ms<br/>liquidar" ==> ST
    end

    subgraph datos["② Datos"]
        direction LR
        RD[("<b>Redis</b><br/>estado de detección<br/>estadísticas · registros")]
        KF[["<b>Kafka</b><br/>transacciones.evaluadas<br/>transacciones.liquidadas"]]
    end

    subgraph cold["③ Cold path · asíncrono · sin deadline"]
        direction LR
        AU["Auditoría<br/>cruce por tx_id"]
        ES["Estadísticas<br/>modo sombra"]
        AL["Alertas<br/>de fraude"]
    end

    FE -- "1 viaje (TxPipeline)" --> RD
    ST -- "script Lua atómico" --> RD
    FE -. "evento · no bloquea" .-> KF
    ST -. "evento · no bloquea" .-> KF
    KF -.-> AU
    KF -.-> ES
    KF -.-> AL

    classDef sinc fill:#e8f4fd,stroke:#1f6fb2,stroke-width:2px,color:#0b2e4f
    classDef cliente fill:#ffffff,stroke:#1f6fb2,stroke-width:2px,color:#0b2e4f
    classDef dato fill:#fff4e5,stroke:#b26b00,stroke-width:2px,color:#4a2c00
    classDef asinc fill:#f5f5f5,stroke:#888,stroke-dasharray: 4 3,color:#333
    class FE,ST sinc
    class C cliente
    class RD,KF dato
    class AU,ES,AL asinc
```

1. **Camino de decisión (sincrónico, ≤ 200ms).** El cliente llama al
   Fraud-Engine con un deadline; el motor decide y liquida con Settlement
   antes de responder. El deadline viaja de punta a punta en el
   `context.Context` y en el header `grpc-timeout`: es lo que da el **flujo
   completo acotado**, porque dentro de los 200ms el cliente sabe el
   resultado final, o sabe que no lo sabe
   ([ADR-0002](docs/adr/0002-camino-de-decision-sincronico-con-grpc.md)).
2. **Datos.** Redis guarda el estado que el camino de decisión consulta y
   escribe (en un solo viaje por transacción,
   [ADR-0008](docs/adr/0008-redis-un-viaje-atomico.md)). Kafka recibe los
   eventos de lo que pasó, en protobuf
   ([ADR-0024](docs/adr/0024-eventos-en-protobuf-sin-bloquear.md)).
3. **Cold path (asíncrono, sin deadline).** Consumidores que procesan los
   eventos a su ritmo: auditoría (cruza lo que respondió el motor con lo que
   registró Settlement), estadísticas en vivo en modo sombra y alertas de
   fraude. Publicar **nunca bloquea**: con Kafka caído, el camino de decisión
   da igual ([ADR-0019](docs/adr/0019-kafka-como-columna-de-eventos.md)). La
   auditoría escribe un archivo JSONL por día y hace visibles las
   discrepancias, como la *ambigua comprometida*: el cliente recibió error,
   pero la transacción se comprometió
   ([ADR-0025](docs/adr/0025-consumidores-y-auditoria.md)).

Dos reglas atraviesan todo: **fail-closed** (si no se puede verificar, no se
aprueba, [ADR-0009](docs/adr/0009-fail-closed-y-fallo-rapido.md)) y
**idempotencia por `tx_id`** (reintentar una transacción no cambia su
resultado, [ADR-0013](docs/adr/0013-ambiguedad-del-timeout-e-idempotencia.md):
un reintento responde lo que ya quedó registrado,
[ADR-0027](docs/adr/0027-reintento-responde-lo-registrado.md)).

En Docker Compose cada capa vive en su propia red privada (`aplicacion`,
`datos`, `observabilidad`), y los servicios se conectan solo a las que
necesitan, como security groups en una VPC
([ADR-0023](docs/adr/0023-redes-como-subredes-privadas.md)).

La observabilidad (métricas, dashboard) es transversal y se describe en su
[propia sección](#observabilidad).

## Recorrido de una transacción

```mermaid
sequenceDiagram
    autonumber
    participant C as Cliente (loadgen)
    participant E as Fraud-Engine
    participant R as Redis
    participant S as Settlement

    C->>E: EvaluateTransaction (deadline 200ms)
    Note over E: Admisión: ¿D_rem ≥ 35ms?<br/>si no → EARLY_DROP (µs)
    Note over E: Validación: tx_id, tarjeta,<br/>coordenadas, monto, categoría
    E->>R: Viaje 1 (TxPipeline, atómico)<br/>SISMEMBER · ZADD/ZCARD · GET pos · HGETALL stats
    R-->>E: blacklist, intentos, posición previa, estadísticas
    Note over E: Mandatoria: blacklist → velocidad → viaje imposible<br/>(un fraude es una respuesta OK con is_fraud)
    Note over E: Slack = D_rem − 15ms − 80ms<br/>≥ 0 → scoring · < 0 → degradada
    E->>R: Viaje 2 (solo si pasó): SET posición confiable
    E->>S: CommitTransaction (timeout 15ms)
    S->>R: script Lua: el primer resultado final gana
    S-->>E: estado registrado + trailer x-settle-us (C_settle)
    E-->>C: decisión (o error con motivo ErrorInfo)
```

Los resultados posibles, del lado del cliente:

```mermaid
flowchart TD
    A([Llega la transacción]) --> B{¿Trae deadline?}
    B -- no --> X1[InvalidArgument<br/>SIN_DEADLINE]
    B -- sí --> C{¿D_rem ≥ 35ms?}
    C -- no --> X2[DeadlineExceeded<br/>EARLY_DROP]
    C -- sí --> D{¿Request válida?}
    D -- no --> X3[InvalidArgument<br/>REQUEST_INVALIDA /<br/>CATEGORIA_DESCONOCIDA]
    D -- sí --> E{¿Redis respondió?}
    E -- no --> X4[Unavailable / DeadlineExceeded<br/>REDIS_*: no se pudo decidir]
    E -- sí --> K{¿El tx_id ya tiene<br/>resultado final registrado?}
    K -- "sí (es un reintento)" --> RR[OK · lo registrado,<br/>sin volver a evaluar]
    K -- no --> F{Mandatoria}
    F -- blacklist --> R1[BLACKLIST]
    F -- "más de 3 intentos en 10s" --> R2[CARD_TESTING]
    F -- "más de 800 km/h" --> R3[VIAJE_IMPOSIBLE]
    F -- pasa --> G{¿Slack ≥ 0?}
    G -- no --> H[degradada:<br/>sin scoring]
    G -- sí --> I{¿score > 0.75?}
    I -- sí --> R4[RIESGO_ALTO]
    I -- no --> H2[aprobada por el motor]
    R1 --> RS[Settlement registra<br/>REJECTED_FRAUD]
    R2 --> RS
    R3 --> RS
    R4 --> RS
    RS -- registrado --> RF[OK · is_fraud<br/>+ motivo]
    RS -- "timeout / caído" --> X5
    H --> J{Settlement}
    H2 --> J
    J -- COMMITTED --> OK[OK · aprobada]
    J -- timeout --> X5[DeadlineExceeded<br/>SETTLEMENT_TIMEOUT:<br/>resultado AMBIGUO, reintentar con el mismo tx_id]
    J -- "caído / conexión cortada" --> X6[Unavailable<br/>SETTLEMENT_NO_DISPONIBLE:<br/>también ambiguo, reintentar]
```

Por qué un fraude es una respuesta `OK` y no un error, y cómo se distinguen
los motivos: [ADR-0005](docs/adr/0005-fraude-no-es-error.md). Por qué un
timeout esperando a Settlement es "resultado desconocido" y cómo se resuelve:
[ADR-0013](docs/adr/0013-ambiguedad-del-timeout-e-idempotencia.md). Por qué
un reintento responde lo registrado sin volver a evaluar, y por qué un
rechazo sin registro confirmado también es ambiguo:
[ADR-0027](docs/adr/0027-reintento-responde-lo-registrado.md) (lo encontró
la auditoría bajo carga).

## Componentes

### Binarios (`cmd/`)

| Binario | Qué hace | ADRs |
|---|---|---|
| [`server`](cmd/server/main.go) | **Fraud-Engine.** Admisión, validación, fase mandatoria contra Redis, slack, fase opcional y liquidación con Settlement. Puerto gRPC `:50051`, métricas `:2112`. | 0002–0009, 0012, 0013, 0027 |
| [`settlement`](cmd/settlement/main.go) | **Settlement.** Registra cada decisión (`COMMITTED`, `REJECTED_FRAUD`, `ABORTED_TIMEOUT`) de forma idempotente y calcula la latencia de extremo a extremo. Puerto `:50052`, métricas `:2113`. | 0012, 0013 |
| [`loadgen`](cmd/loadgen/main.go) | **Generador de carga** de lazo abierto (llegadas de Poisson). Tasa fija o un **perfil del día** con picos ([`perfiles/`](perfiles/)). Mide la latencia desde el instante programado, clasifica resultados y reintenta los ambiguos. Métricas `:2114`. | 0014, 0026 |
| [`cargar-stats`](cmd/cargar-stats/main.go) | Calcula media, desvío y peso de riesgo por categoría (algoritmo de Welford) desde el dataset y los guarda en Redis. | 0010 |
| [`salud`](cmd/salud/main.go) | Chequeo de salud por gRPC para contenedores sin shell. | 0020 |
| [`eventos`](cmd/eventos/main.go) | **Consumidores del cold path**, un rol por contenedor: `auditoria` (cruza los dos topics y detecta discrepancias), `alertas` (y **bloqueo automático** de tarjetas ante card testing o viaje imposible), `estadisticas` (modo sombra) y `volcar` (muestra los eventos como JSON). | 0025, 0029 |
| [`gateway`](cmd/gateway/main.go) | **Gateway de la demo con QR.** Recibe compras desde el celular (HTTP) y las manda al motor (gRPC), poniendo el deadline de 200ms en el borde. Página del celular (tarjeta propia, escenarios de fraude, desglose de latencia) y del presentador (QR y gráfico en vivo). Puerto `:8080`, métricas `:2116`. | 0029 |

### Paquetes (`internal/`)

| Paquete | Responsabilidad |
|---|---|
| [`presupuesto`](internal/presupuesto/presupuesto.go) | El modelo temporal: deadline global, WCETs, reserva, `Admitir`, `Slack`, `CorreOpcional`. Funciones puras. |
| [`mandatoria`](internal/mandatoria/mandatoria.go) | Fase mandatoria contra Redis en un solo viaje (`TxPipeline`): blacklist, ventana de velocidad (ZSET), posición confiable, Haversine. |
| [`opcional`](internal/opcional/opcional.go) | Scoring: Z-score sobre ln(monto) ponderado por categoría, con costo de CPU simulado configurable. |
| [`settlement`](internal/settlement/settlement.go) | El servicio Settlement: registro con script Lua ("el primer resultado final gana"), con TTL, y trailer con su tiempo interno. |
| [`eventos`](internal/eventos/eventos.go) | Productor de eventos que nunca bloquea (`TryProduce`, buffer acotado, descartes contados). |
| [`consumo`](internal/consumo/consumo.go) | Bucle de consumo "al menos una vez" (confirma después de procesar) con métrica de lag. |
| [`auditoria`](internal/auditoria/auditoria.go) | Cruce de los dos topics por `tx_id` con ventana, clasificación pura y archivo JSONL por día. |
| [`estadisticas`](internal/estadisticas/estadisticas.go) | Media y desvío en una pasada (Welford), compartido por la línea base y las estadísticas en vivo. |
| [`fallas`](internal/fallas/fallas.go) | Motivos de error (`google.rpc.ErrorInfo`): el contrato de errores con los clientes. |
| [`metricas`](internal/metricas/metricas.go) | Interceptor gRPC (tasa, errores, latencia, en vuelo, respuestas tardías) y métricas del modelo temporal. |
| [`redisclient`](internal/redisclient/redisclient.go) | Cliente de Redis con la configuración del hot path: respeta el deadline del `ctx` y no reintenta. |
| [`servicio`](internal/servicio/servicio.go) | Ciclo de vida: salud gRPC estándar y apagado ordenado ante SIGTERM. |
| [`config`](internal/config/config.go) | Configuración desde variables de entorno, con validación al arrancar. |
| [`registro`](internal/registro/registro.go) | Logs estructurados (`log/slog`) con niveles, desde el entorno. |
| [`pruebas`](internal/pruebas/pruebas.go) | Helpers de los tests: Redis real con prefijo único y servidores gRPC reales en puertos reales. |

### Contrato

[`proto/transactions.proto`](proto/transactions.proto) define dos servicios:
`FraudEngine.EvaluateTransaction` y `Settlement.CommitTransaction`.
[`proto/eventos.proto`](proto/eventos.proto) define los eventos de Kafka
(`TransaccionEvaluada`, `TransaccionLiquidada`), que reusan los mensajes del
contrato. El código generado está versionado; se regenera con `make proto`
solo si cambia un `.proto`.

## Estructura del repositorio y por qué

```
.
├── proto/                 contrato gRPC + código generado
├── cmd/                   un directorio por binario: solo arma las piezas
│   ├── server/            Fraud-Engine
│   ├── settlement/        Settlement
│   ├── loadgen/           generador de carga
│   ├── cargar-stats/      estadísticas del scoring → Redis
│   ├── salud/             chequeo de salud para contenedores
│   ├── eventos/           consumidores del cold path (un rol por contenedor)
│   └── gateway/           gateway de la demo con QR (HTTP → gRPC)
├── internal/              toda la lógica (no importable desde afuera del módulo)
├── observabilidad/        Prometheus y Grafana, provisionados desde archivos
├── perfiles/              curvas de carga del día para el loadgen (ADR-0026)
├── docs/adr/              decisiones de diseño (img/: el diagrama de AWS, generado por código)
├── FLUJO-DEL-SISTEMA.md   una transacción paso a paso y dónde se evalúa el tiempo real
├── infra/                 Terraform para AWS (próximamente, Etapa 7.6)
├── Dockerfile             una imagen por servicio (--target)
├── docker-compose.yml     el sistema completo, por perfiles
├── Makefile               atajos
├── data/                  dataset (fuera de git; se descarga aparte)
└── auditoria/             registro de auditoría, un JSONL por día (fuera de git)
```

- **`cmd/` solo cablea, `internal/` hace.** Cada binario lee su configuración,
  crea las piezas y las conecta. La lógica vive en `internal/` por dos
  razones: un paquete `main` **no se puede importar** (los tests del motor
  necesitan levantar un Settlement real, y por eso Settlement vive en
  `internal/settlement`), y separar "armar" de "hacer" deja la lógica
  testeable sin red.
- **`internal/` lo hace cumplir el compilador:** solo este módulo puede
  importar esos paquetes. Es la API privada del proyecto.
- **Un paquete por responsabilidad, sin interfaces de más.** Hay una sola
  implementación de cada cosa (por ejemplo, un solo almacén: Redis), así que
  no hay interfaces "por si acaso". El modelo temporal está aislado en
  `presupuesto` como funciones puras: se testea a 1µs de los bordes sin
  depender del reloj.
- **Un solo `Dockerfile`** con una salida por servicio: los mismos binarios,
  compilados con la misma versión de Go que los nativos, en imágenes
  distroless de 32–49MB, sin shell y no-root
  ([ADR-0020](docs/adr/0020-imagenes-salud-y-compose.md)).
- **La observabilidad es código:** el dashboard y el datasource se cargan
  desde archivos versionados; se levanta sin configurar nada
  ([ADR-0015](docs/adr/0015-observabilidad-prometheus-grafana.md)).
- **El dataset no se versiona ni va en las imágenes:** pesa ~500MB y se monta
  como volumen.

## Decisiones de diseño (ADRs)

Índice completo con estado y etapa: [`docs/adr/README.md`](docs/adr/README.md).

| Tema | ADRs |
|---|---|
| **Modelo de tiempo real** | [0002](docs/adr/0002-camino-de-decision-sincronico-con-grpc.md) camino sincrónico con deadline propagado · [0003](docs/adr/0003-control-de-admision-temprano.md) admisión temprana · [0004](docs/adr/0004-computacion-imprecisa.md) computación imprecisa · [0011](docs/adr/0011-costo-simulado-y-wcet.md) peor caso, no promedio |
| **Correctitud ante fallas** | [0005](docs/adr/0005-fraude-no-es-error.md) un fraude no es un error · [0009](docs/adr/0009-fail-closed-y-fallo-rapido.md) fail-closed y fallo rápido · [0012](docs/adr/0012-settlement-segundo-servicio-sincronico.md) Settlement con timeout propio · [0013](docs/adr/0013-ambiguedad-del-timeout-e-idempotencia.md) ambigüedad del timeout e idempotencia · [0022](docs/adr/0022-conexion-caida-es-resultado-ambiguo.md) conexión caída = ambiguo · [0027](docs/adr/0027-reintento-responde-lo-registrado.md) un reintento responde lo registrado |
| **Detección** | [0006](docs/adr/0006-que-se-registra-ante-un-rechazo.md) qué se registra ante un rechazo · [0007](docs/adr/0007-validacion-estricta-de-la-request.md) validación · [0008](docs/adr/0008-redis-un-viaje-atomico.md) Redis en un viaje atómico · [0010](docs/adr/0010-modelo-de-scoring.md) scoring |
| **Cold path (Kafka)** | [0019](docs/adr/0019-kafka-como-columna-de-eventos.md) Kafka al costado · [0023](docs/adr/0023-redes-como-subredes-privadas.md) redes como subredes privadas · [0024](docs/adr/0024-eventos-en-protobuf-sin-bloquear.md) eventos en protobuf sin bloquear · [0025](docs/adr/0025-consumidores-y-auditoria.md) consumidores y auditoría |
| **Medir y operar** | [0014](docs/adr/0014-load-generator-de-lazo-abierto.md) generador de lazo abierto · [0015](docs/adr/0015-observabilidad-prometheus-grafana.md) observabilidad · [0016](docs/adr/0016-tests-contra-infraestructura-real.md) tests reales · [0017](docs/adr/0017-configuracion-por-entorno-y-apagado-ordenado.md) configuración y apagado · [0018](docs/adr/0018-evaluar-en-distintos-entornos.md) evaluar por entornos · [0020](docs/adr/0020-imagenes-salud-y-compose.md) imágenes, salud y Compose · [0021](docs/adr/0021-logs-con-niveles.md) logs con niveles · [0026](docs/adr/0026-perfil-de-carga-diario.md) perfil de carga diario · [0028](docs/adr/0028-metricas-de-evaluacion-sli-slo-sla.md) métricas oficiales, SLI y SLO · [0029](docs/adr/0029-demo-interactiva-y-bloqueo-automatico.md) demo con QR y bloqueo automático |

## Cómo se evalúa

El dashboard sirve para diagnosticar; lo que se **reporta** son las métricas
del contrato de tiempo real, medidas **en el borde** del sistema (el
loadgen) y calculadas desde el CSV de cada transacción
([ADR-0028](docs/adr/0028-metricas-de-evaluacion-sli-slo-sla.md)):

| Métrica oficial (SLI) | Objetivo (SLO) |
|---|---|
| Decisiones a tiempo (1 − *Deadline Miss Ratio*) | ≥ 99% |
| Latencia ≤ 100ms | ≥ 99% |
| Decisiones con scoring (no degradadas) | ≥ 99% |
| Correctitud (la respuesta coincide con lo registrado) | 100% |
| Sin ambiguas comprometidas | ≥ 99,9% |

Los objetivos valen **dentro de una envolvente de carga** (inicial: 400 RPS
en la notebook de desarrollo): el SLO es el mismo en todos los entornos, y
lo que se compara es **hasta qué carga se cumple**. Cada transacción tiene
un deadline duro (tarde = falla), pero la garantía del sistema es
estadística: en la nube no se puede probar un peor caso, solo medirlo
(*firm real-time*).

## Lo que mostraron las mediciones

Medido en la notebook de desarrollo (Intel i7-12650H, 16 hilos), en Docker
Compose, con el cargador conectado y el perfil de energía en `performance`.
Son datos de **ese** entorno: la comparación formal entre entornos es lo que
sigue. A batería, el CPU baja a menos de la mitad de su frecuencia y la
latencia se duplica, así que el estado de energía es parte de cada medición.

| Qué | Resultado |
|---|---|
| Fase mandatoria: 6 comandos a Redis | uno por uno: P50 133µs · con `TxPipeline`: **P50 27µs** (≈ 1 viaje) |
| Red entre motor y Settlement (`C_net`) | P50 0.34ms, P99 0.68ms, contra 15ms reservados |
| Scoring con costo simulado | se presupuesta por **peor caso**: con 40M iteraciones, media 65ms pero P99 81ms (no entraba en 80ms) |
| Redis caído | sin reintentos: `Unavailable` en **~1ms**; con los reintentos por defecto de go-redis, 200ms |
| CPU por transacción en el motor | **~21ms** (casi todo, el scoring simulado): fija la capacidad |
| Quiebre | **~500 RPS**: arriba, el % a tiempo cae; se recupera en menos de 5s al bajar de ~470 RPS |
| Un día de carga (pico 800 RPS, 24 min) | 85% a tiempo en el día; en los picos de las 12h y las 19h, ~65–67% |
| Ventana del mediodía, antes y después del [ADR-0027](docs/adr/0027-reintento-responde-lo-registrado.md) | **~75% → ~97%** a tiempo; ambiguas comprometidas 16% → ~1%; inconsistentes 20 → **0** |
| Kafka arriba / caído / deshabilitado | el camino de decisión da **igual** en los tres; con Kafka caído los eventos esperan en el buffer y se entregan al volver |
| Apagado con SIGTERM a mitad de carga | **0** transacciones en vuelo perdidas |

Dos hallazgos importan más:

- **El colapso por congestión.** Sin una cola delante del handler, toda
  request pasa la admisión con ~200ms y después todas compiten por el CPU y
  vencen juntas: pasado el quiebre, el sistema entrega **menos** que con
  menos carga. La admisión solo protege si hay espera que consuma $D_{rem}$
  ([ADR-0003](docs/adr/0003-control-de-admision-temprano.md)).
- **La auditoría encontró un bug que ningún test había visto:** bajo
  sobrecarga, un reintento podía responder "rechazada" sobre una transacción
  ya cobrada (dos intentos decidían distinto: uno degradado, otro con
  scoring). Se corrigió en el ADR-0027, y de paso los reintentos dejaron de
  pagar el scoring, lo que desarmó la tormenta de reintentos.

## En la nube: AWS (propuesta)

![Arquitectura propuesta en AWS](docs/adr/img/arquitectura-aws.png)

El mismo sistema, con **las mismas imágenes** y **la misma observabilidad**,
pero distribuido: cada pieza en su host y hablando por la red real. Es la
diferencia con la notebook que se quiere medir. Al mismo tiempo, está
diseñado para costar poco: se crea con Terraform, se mide y se destruye.
Estado: **diseño**. La implementación es la Etapa 7.6; las decisiones van al
ADR-0030 y el diagrama se genera desde
[`docs/adr/img/arquitectura-aws.py`](docs/adr/img/arquitectura-aws.py).

**Qué hace cada parte:**

| Parte | Dónde corre | Qué hace |
|---|---|---|
| **loadgen** | EC2 propia (c7i.large, spot), en la subred borde | Genera la carga que se **mide**. Hace de pasarela de pagos: está cerca del sistema, así que su latencia no incluye internet. Los 200ms son el presupuesto del motor, no del pago completo |
| **ALB interno** | subred aplicación | Reparte las llamadas gRPC **por request** entre las réplicas del motor (un balanceador de conexiones mandaría todo a una sola, porque HTTP/2 mantiene la conexión abierta) |
| **Fraud-Engine** | ECS, de 2 a 8 réplicas en c7i.large (una on-demand de base, el resto spot) | Decide cada transacción: admisión, mandatoria, slack, scoring. **Escala horizontalmente** según las requests por réplica, y con escalado programado antes de los picos conocidos del día |
| **Settlement + Redis** | EC2 de datos (c7i.large, **on-demand**: Redis guarda estado) | Settlement registra cada decisión una sola vez; Redis guarda la blacklist, la ventana de velocidad, las posiciones, las estadísticas y los registros |
| **Kafka** | EC2 propia (t3.medium) | La columna de eventos. Un broker sin replicación, declarado (en producción serían 3 o MSK) |
| **Consumidores** | EC2 fría (t3.large) | Auditoría, alertas y estadísticas en sombra, como en local |
| **Prometheus + Grafana** | EC2 fría | Las mismas métricas y el mismo dashboard que en local. Prometheus descubre las réplicas nuevas por las etiquetas de EC2; Grafana suma CloudWatch como segunda fuente y se accede por un túnel de SSM, no es pública |
| **CloudWatch** | servicio de AWS | La infraestructura: instancias, réplicas, el ALB (con su propia medición de latencia en el borde), el auto scaling y los logs |
| **Demo con QR** | ALB público + gateway, solo en modo demo | Los asistentes compran (o intentan un fraude) desde el celular. El gateway pone el deadline de 200ms en el borde, como una pasarela real, y muestra aparte la latencia de internet de cada uno |
| **Gestión** | GitHub Actions, GHCR, Terraform | Las imágenes se construyen una vez y se promueven por digest; Terraform crea y destruye el entorno; el acceso a AWS es por OIDC, sin claves guardadas; una alarma de presupuesto vigila el gasto |

**Cómo se mantiene barato:** spot en los motores y el loadgen, una sola
zona, Redis y Kafka propios en lugar de servicios administrados, instancias
t3 solo en el camino frío (sus créditos de CPU no afectan al deadline), sin
NAT Gateway y todo efímero. Orden de magnitud: menos de un dólar por hora
mientras está prendido.

## Cómo correrlo

### Requisitos

- Go 1.27 o superior.
- Docker con Compose v2.
- El dataset *Credit Card Transactions Fraud Detection* (Sparkov, licencia
  CC0) en `data/`. Con la CLI de Kaggle:

  ```sh
  kaggle datasets download -d kartik2112/fraud-detection -p data/ --unzip
  ```

### Opción A: todo en Docker Compose

```sh
make sistema-up                                   # Redis, Kafka, Settlement, Fraud-Engine, consumidores + Prometheus y Grafana
make carga-compose ARGS="-rps 300 -duracion 30s"  # carga dentro del Compose
make sistema-down                                 # bajar todo
```

El orden de arranque lo dan los chequeos de salud: Redis → carga de
estadísticas (un contenedor que corre una vez y termina) → Settlement →
Fraud-Engine. Kafka y sus topics arrancan en paralelo: el motor no depende de
ellos. La auditoría queda en `auditoria/AAAA-MM-DD.jsonl`; para ver los
eventos en vivo, `make volcar`.

### Opción B: binarios nativos

```sh
docker run -d --name redis-drts -p 6379:6379 redis   # Redis
make cargar-stats                                    # estadísticas del scoring
make run-settlement                                  # en una terminal
make run-server                                      # en otra
make carga-nominal                                   # 50 RPS, 30s
go run ./cmd/loadgen -rps 700 -duracion 30s          # o cualquier tasa
make kafka-up                                        # Kafka (opcional; los servicios usan KAFKA_BROKERS=localhost:9092)
make obs-up                                          # Prometheus + Grafana (opcional)
```

Cada corrida del generador imprime un resumen (P50/P99/P100 por categoría, %
dentro del deadline) y deja un CSV por transacción en `resultados/`.

### Un día de carga (perfil con picos)

```sh
make carga-compose ARGS="-perfil perfiles/dia-habil.csv -rps 800"                # el día entero en 24 min
make carga-compose ARGS="-perfil perfiles/dia-habil.csv -rps 800 -desde 11 -hasta 14 -compresion 360"  # solo el mediodía, en 30s
```

La tasa sigue una curva del día (madrugada baja, mañana estable, picos al
mediodía y a las 19h): un proceso de Poisson no homogéneo
([ADR-0026](docs/adr/0026-perfil-de-carga-diario.md)). `-rps` es la tasa
del pico y `-compresion 60` hace que 1 hora del perfil dure 1 minuto. Al
final se imprime, además, un resumen **por franja horaria**: el pico es lo
que importa.

### La demo con QR

```sh
make demo-up                          # el sistema + el gateway de la demo
# en el navegador: http://<IP de la máquina>:8080/presentador?t=demo-local  → el QR
make demo-cpu CPUS=1                  # el motor limitado a 1 núcleo
make demo-carga                       # carga de fondo (55 RPS): el sistema queda al borde
make demo-cpu CPUS=4                  # más capacidad, en vivo: la latencia baja
```

Los asistentes escanean el QR, compran con su propia tarjeta (de fantasía)
e intentan engañar al motor (card testing, viaje imposible, tarjeta robada,
monto anormal). Ven en su celular cuánto tardó cada parte (su internet, la
espera en el sistema, el motor), y si su tarjeta queda bloqueada: el
**camino frío** la bloquea por Kafka unos milisegundos después
([ADR-0029](docs/adr/0029-demo-interactiva-y-bloqueo-automatico.md)). El
token del QR se cambia con `DEMO_TOKEN`. Abrí el presentador con la IP de
la máquina, no con `localhost`: el QR apunta al mismo host.

## Configuración

Por variables de entorno ([ADR-0017](docs/adr/0017-configuracion-por-entorno-y-apagado-ordenado.md)).
Los valores por defecto sirven para correr en una sola máquina. Un valor mal
escrito impide arrancar.

| Variable | Fraud-Engine | Settlement | Para qué |
|---|---|---|---|
| `GRPC_ADDR` | `:50051` | `:50052` | dónde escucha |
| `METRICAS_ADDR` | `:2112` | `:2113` | `/metrics` (`""` = no exponer) |
| `REDIS_ADDR` | `localhost:6379` | `localhost:6379` | Redis |
| `REDIS_PREFIJO` | `drts:` | `drts:` | prefijo de las claves (permite compartir un Redis) |
| `SETTLEMENT_ADDR` | `localhost:50052` | — | Settlement |
| `ITERACIONES_SCORING` | `10000000` | — | costo de CPU simulado del scoring (~16ms) |
| `APAGADO_ESPERA` | `5s` | `5s` | máximo para terminar las requests en vuelo |
| `APAGADO_AVISO` | `0s` | `0s` | tiempo en `NOT_SERVING` antes de dejar de atender (detrás de un balanceador) |
| `LOG_NIVEL` | `info` | `info` | `debug` agrega una línea por transacción ([ADR-0021](docs/adr/0021-logs-con-niveles.md)) |
| `LOG_FORMATO` | `texto` | `texto` | `json` para la nube |
| `KAFKA_BROKERS` | `""` | `""` | brokers de Kafka; vacío = eventos deshabilitados (el sistema funciona igual) |
| `SETTLEMENT_TTL` | — | `24h` | cuánto vive cada registro de Settlement en Redis (mientras se pueda reintentar) |

El generador de carga se configura por flags: `go run ./cmd/loadgen -h`.
El consumidor de alertas bloquea tarjetas según `BLOQUEO_AUTOMATICO` (por
ejemplo `CARD_TESTING,VIAJE_IMPOSIBLE`; vacío = sin bloqueo).

## Observabilidad

```mermaid
flowchart LR
    subgraph sistema["Sistema"]
        direction TB
        FE["Fraud-Engine :2112"]
        ST["Settlement :2113"]
        LG["loadgen :2114"]
        EV["consumidores :2115<br/>auditoría · alertas · estadísticas"]
    end
    PR["<b>Prometheus</b><br/>scrape cada 1s"]
    GF["<b>Grafana</b><br/>dashboard provisionado"]
    FE -. "/metrics" .-> PR
    ST -. "/metrics" .-> PR
    LG -. "/metrics" .-> PR
    EV -. "/metrics" .-> PR
    PR --> GF
```

`make obs-up` (o `make sistema-up`) levanta Prometheus (http://localhost:9090)
y Grafana (http://localhost:3000, sin login, solo en `localhost`). El
dashboard **"D-RTS — Tiempo real"** se lee de arriba hacia abajo:

1. **Lo que ve el cliente:** % dentro del deadline, carga ofrecida vs
   *goodput*, resultados por categoría, latencia con la línea de los 200ms.
2. **Por qué:** requests en vuelo, respuestas tardías (el servidor no se
   entera de un *deadline miss*: esta métrica lo deja ver), $D_{rem}$ al
   llegar, slack, decisiones, duración de cada fase.
3. **Settlement:** estados registrados y latencia de extremo a extremo.
4. **Runtime de Go y CPU:** núcleos por proceso, pausas del GC, goroutines.
5. **Cold path (Kafka):** eventos publicados y descartados, buffer del
   productor, lag de cada consumidor, resultado de la auditoría (con las
   discrepancias contadas aparte), alertas por motivo y deriva de las
   estadísticas en sombra. Con Kafka caído, las filas 1–4 no cambian: es la
   forma de verlo.

Para la demo hay un segundo dashboard, **"D-RTS — Demo en vivo"**: las
compras de la clase, la latencia contra el deadline, la **tabla de
tarjetas** (cada uno busca la suya y ve si quedó activa, en revisión o
bloqueada), los fraudes por tipo y cómo aprende el sistema por Kafka.

Qué muestra cada panel, cómo leerlo, cómo diagnosticar con él y qué
métricas comparar entre entornos:
[observabilidad/GUIA-DASHBOARD.md](observabilidad/GUIA-DASHBOARD.md).

Prometheus scrapea las dos formas de correr el sistema con la etiqueta
`entorno` (`nativo` / `compose`), con el mismo dashboard. El interceptor de
métricas cuesta ~120ns por request.

## Tests

```sh
docker start redis-drts    # los tests usan Redis real
make kafka-up              # y Kafka real
go test -race ./...
```

Los tests usan **Redis real**, **Kafka real** y **servidores gRPC reales** en
puertos reales. Si falta alguno, fallan con un mensaje que dice cómo levantarlo
(no se saltean: un test salteado se ve "ok"). `REDIS_ADDR` permite apuntar a
otro Redis. Algunos tests reproducen fallas con infraestructura real
modificada: un Settlement que tarda, uno que compromete y "pierde" la
respuesta, un puerto donde no escucha nadie.

## Estado y próximos pasos

| Etapa | Estado |
|---|---|
| Admisión, fase mandatoria, Redis, slack y fase opcional, Settlement, generador de carga, observabilidad | ✅ |
| Contenedores: configuración por entorno, apagado ordenado, salud, Compose | ✅ |
| Kafka como columna de eventos: Kafka, redes privadas, eventos en protobuf publicados sin bloquear | ✅ ([ADR-0023](docs/adr/0023-redes-como-subredes-privadas.md), [ADR-0024](docs/adr/0024-eventos-en-protobuf-sin-bloquear.md)) |
| Consumidores: auditoría con detección de discrepancias, estadísticas en vivo (sombra), alertas; TTL en Settlement | ✅ ([ADR-0025](docs/adr/0025-consumidores-y-auditoria.md)) |
| Perfil de carga diario: Poisson no homogéneo, picos al mediodía y a las 19h | ✅ ([ADR-0026](docs/adr/0026-perfil-de-carga-diario.md)) |
| Corrección: un reintento responde lo registrado (lo encontró la auditoría) | ✅ ([ADR-0027](docs/adr/0027-reintento-responde-lo-registrado.md)) |
| Métricas oficiales y objetivos (SLI, SLO, SLA) | ✅ ([ADR-0028](docs/adr/0028-metricas-de-evaluacion-sli-slo-sla.md)) |
| Experimento: Kafka en el camino de decisión (modo asíncrono) | planificado |
| Nube (AWS): arquitectura y diagrama | ✅ diseño · ADR-0030 por escribir |
| Nube (AWS): Terraform, CI/CD, medición (fase 1: una EC2 · fase 2: distribuido con auto scaling) | en curso |
| Demo interactiva con QR: gateway, tarjeta por celular, bloqueo automático por el camino frío, dashboard de la demo | ✅ ([ADR-0029](docs/adr/0029-demo-interactiva-y-bloqueo-automatico.md)) |
| Calibración por entorno: pool de workers contra el colapso por congestión, pausas del GC, comparación nativo / Compose / nube | planificado |

## Referencias

- J. W. S. Liu, K.-J. Lin, W.-K. Shih, A. C. Yu, J.-Y. Chung y W. Zhao (1991).
  *Algorithms for Scheduling Imprecise Computations*. IEEE Computer.
- Dataset: *Credit Card Transactions Fraud Detection* (Kartik Shenoy, generado
  con Sparkov), Kaggle, licencia CC0. Sintético: no contiene datos reales de
  personas.
- Formato de los ADRs: Michael Nygard, *Documenting Architecture Decisions*.
