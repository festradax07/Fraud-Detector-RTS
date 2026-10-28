# ADR-0029: Demo interactiva con QR y bloqueo automático de tarjetas

- **Estado:** Aceptada
- **Fecha:** 2026-10-28
- **Etapa:** demo de la presentación (adelanta una pieza planeada para
  después de la nube)

## Contexto

Para la presentación se quiere que los asistentes **usen** el sistema: que
escaneen un QR, compren desde su celular, intenten engañar al motor y vean
sus propios tiempos, en particular cómo sube la latencia cuando el sistema
está saturado y cómo baja cuando se le agrega capacidad. Tres problemas:

1. **El navegador no habla gRPC.** Hace falta una pieza que traduzca.
2. **Un endpoint público** puede recibir tráfico de cualquiera.
3. **La blacklist estaba vacía** en el sistema desplegado (solo los tests
   cargaban tarjetas), y nada "aprendía" de un fraude detectado: la misma
   tarjeta podía seguir intentando.

## Decisión

### El gateway de la demo ([`cmd/gateway`](../../cmd/gateway/main.go))

- **HTTP/JSON del celular → gRPC al motor.** El gateway **pone el deadline
  de 200ms en el borde**, como la pasarela de un procesador de pagos: el
  tiempo de internet del celular queda afuera del presupuesto del motor, y
  la página lo muestra aparte. El `ctx` de la request HTTP es la base (si el
  celular se va, se cancela).
- **La página del celular** muestra, por compra: la decisión y su motivo,
  si salió degradada, y el desglose del tiempo total en **internet**
  (total − tiempo en el servidor), **red interna y espera** (tiempo en el
  servidor − lo que informa el motor) y **el motor decidiendo**
  (`processing_time_ms`). Más un historial contra la línea de los 200ms y un
  botón para repetir.
- **La pantalla del presentador**: el QR (armado con el host por el que
  entró el presentador), contadores y un gráfico en vivo de la latencia de
  las compras de la clase.
- **Escenarios** (funciones puras): compra normal, card testing (4 compras
  seguidas), viaje imposible (Buenos Aires y Tokio), tarjeta robada y monto
  anormal. **Cada celular tiene su tarjeta** (un número de fantasía,
  claramente no real) y puede pedir una nueva.
- **Protección:** token obligatorio (va en el QR) comparado en tiempo
  constante, y dos límites: **por celular** (2 compras por segundo,
  ráfagas de 6) y **por IP**, generoso (40 por segundo, ráfagas de 80):
  en un aula todos los celulares salen a internet por la misma IP (el NAT
  del wifi), así que un límite estricto por IP frenaría a toda la clase.
- **Dashboard de la demo** ("D-RTS — Demo en vivo"): las compras de la
  clase, la latencia contra el deadline, **una tabla con la tarjeta de cada
  celular** y su estado, los fraudes por tipo y el camino frío (alertas,
  bloqueos, lag). El número de fantasía lo calcula el servidor, así el
  celular y el dashboard muestran el mismo. La etiqueta `tarjeta` en
  Prometheus es una excepción a la regla de no usar identificadores como
  etiquetas: el conjunto está acotado (una por celular, tope de 500).
- **No es parte del sistema medido:** la carga que se mide la genera el
  loadgen. El gateway va en un perfil de Compose propio (`demo`).

### Bloqueo automático desde el camino frío

- El consumidor de **alertas**, al recibir un fraude con un motivo de
  **señal fuerte** (configurable: `BLOQUEO_AUTOMATICO`, en el Compose
  `CARD_TESTING,VIAJE_IMPOSIBLE`), **agrega la tarjeta a la blacklist** en
  Redis. Las compras siguientes de esa tarjeta se rechazan por `BLACKLIST`.
- **Un monto anormal (`RIESGO_ALTO`) no bloquea:** rechaza la compra pero
  puede ser legítima; en un sistema real se le preguntaría al titular.
- **Lo hace el camino frío, no el motor:** el motor decide en milisegundos
  y no escribe la blacklist. El bloqueo llega por Kafka unos milisegundos
  después (**eventualmente consistente**): una compra en ese instante puede
  pasar. Es lo que ocurre en los sistemas reales, donde el bloqueo lo hacen
  los sistemas que procesan las alertas.
- `SADD` es idempotente: con la entrega "al menos una vez" de Kafka,
  procesar un evento dos veces no cambia nada. Métrica:
  `drts_bloqueos_total{motivo}`.
- `cargar-stats -blacklist` carga la tarjeta robada de la demo
  (`demo-robada`): la blacklist pasa a ser un dato de referencia más.

### La capacidad en vivo, en una sola máquina

Para mostrar que más capacidad baja la latencia, en una sola máquina se
cambia el **límite de CPU del motor en vivo** (`docker update --cpus`,
`make demo-cpu`): con 1 núcleo y una carga de fondo de ~55 RPS el sistema
queda al borde; al pasar a 4 núcleos, la latencia vuelve a lo normal sin
reiniciar nada. Se presenta como lo que es: **escalado vertical**. El
horizontal (sumar máquinas) es el auto scaling de la arquitectura en AWS.

## Alternativas consideradas

- **gRPC-Web desde el navegador:** necesita un proxy igual (Envoy) y no
  permite poner el deadline del lado del servidor.
- **Bloquear desde el motor (en el camino caliente):** una escritura más
  dentro de los 200ms y mezclar decidir con reaccionar. En los sistemas
  reales el bloqueo es una reacción posterior.
- **Bloquear también por riesgo alto:** bloquearía compras legítimas
  inusuales.
- **Sumar réplicas del motor en la misma máquina:** no agrega CPU (el motor
  está limitado por CPU), así que la latencia no mejora: no mostraría nada
  real.
- **Una tarjeta nueva en cada compra:** se probó, pero hacía imposible ver
  el bloqueo. Con la tarjeta del celular, repetir más de 3 compras en 10s
  es card testing de verdad, y la página lo explica.

## Consecuencias

- Un endpoint público durante la demo: token, límite por IP y existe solo
  mientras dura (en AWS, Terraform lo crea y lo destruye).
- El bloqueo automático cambia el comportamiento del sistema, pero no las
  mediciones: el loadgen usa una tarjeta única por transacción y nunca
  dispara esas reglas.
- La carga que deja "al borde" a 1 núcleo depende de la máquina: medida en
  la notebook de desarrollo (45 RPS sin cambios visibles, 55 al borde, 62 o
  más casi todo falla); se recalibra en AWS.
