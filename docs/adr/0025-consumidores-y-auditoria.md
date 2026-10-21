# ADR-0025: Consumidores del cold path; la auditoría cruza los dos lados

- **Estado:** Aceptada
- **Fecha:** 2026-10-20
- **Etapa:** 8 (Kafka)

## Contexto

Con los eventos en Kafka ([ADR-0024](0024-eventos-en-protobuf-sin-bloquear.md))
faltaba quién los procesara: un registro durable (para que los registros de
Settlement en Redis puedan expirar), alertas de fraude y estadísticas en
vivo. Y quedaba un problema visible solo desde afuera de cada servicio: la
ambigua comprometida ([ADR-0013](0013-ambiguedad-del-timeout-e-idempotencia.md)),
que ningún servicio por sí solo puede detectar.

## Decisión

- **Un binario, varios roles** ([`cmd/eventos`](../../cmd/eventos/main.go),
  `-rol auditoria | alertas | estadisticas | volcar`), cada rol en su propio
  contenedor y con su propio **consumer group**: cada grupo recibe todos los
  eventos y se procesa a su ritmo.
- **Entrega "al menos una vez"** ([`internal/consumo`](../../internal/consumo/consumo.go)):
  el offset se confirma recién después de procesar cada lote (sin
  autocommit), con los rebalanceos bloqueados mientras se procesa. Los roles
  son idempotentes por `tx_id`. Un grupo nuevo empieza desde el principio
  (retención de 7 días). El lag de cada grupo es una métrica.
- **Auditoría** ([`internal/auditoria`](../../internal/auditoria/auditoria.go)):
  - cruza `evaluadas` con `liquidadas` por `tx_id` y clasifica cada
    transacción: `CONSISTENTE`, `NO_COMPROMETIDA` (error y nada comprometido),
    **`AMBIGUA_COMPROMETIDA`** (el cliente recibió error, pero se comprometió),
    `REGISTRO_PERDIDO`, `EVENTO_PERDIDO`, `INCONSISTENTE`;
  - **ventana de 30s**: los dos eventos vienen de topics y servicios
    distintos y pueden llegar en cualquier orden; se espera a la pareja y, si
    no llega, se clasifica con lo que hay. Con reintentos, cuenta la última
    evaluación (lo que el cliente terminó recibiendo);
  - la clasificación es una función pura (`Clasificar`);
  - cada transacción clasificada se escribe en un **archivo JSONL por día**
    (append-only, `fsync` una vez por segundo): el registro durable.
  - Detecta, no corrige: conciliar está fuera del alcance del proyecto.
- **Los registros de Settlement en Redis pasan a tener TTL** (`SETTLEMENT_TTL`,
  24h como punto de partida): solo tienen que durar mientras un cliente pueda
  reintentar el mismo `tx_id`.
- **Estadísticas en modo sombra:** media y desvío de ln(monto) por categoría
  de las transacciones aprobadas, en claves aparte (`stats_vivo:*`) que **el
  motor no usa**, y cuánto se alejan de la línea base, en desvíos. El sistema
  no tiene una señal de "transacción confirmada como legítima" (en la
  realidad es el contracargo, días después): aprender directo de las
  aprobadas dejaría que un atacante corra la media. Primero se observa.
- **Alertas:** fraudes por motivo (en un sistema real, se abriría un caso).

## Alternativas consideradas

- **Un consumidor con todos los roles:** un solo grupo; si un rol se atrasa,
  atrasa a los otros.
- **Auditoría en una base de datos (Postgres):** un componente más para
  operar y desplegar; los archivos JSONL alcanzan y se inspeccionan con `jq`.
- **Alimentar al motor con las estadísticas en vivo:** vulnerable al
  envenenamiento sin una señal de confirmación.

## Consecuencias

- Verificado en Compose a 300 RPS: la auditoría detectó **99 ambiguas
  comprometidas** (y 32 no comprometidas, 2 registros perdidos) que hasta
  ahora eran invisibles; la deriva de las estadísticas en sombra fue ~0σ
  (tráfico y línea base salen del mismo generador de datos).
- **Limitación conocida:** los offsets se confirman al procesar, no al
  cerrar la ventana; si la auditoría se cae con transacciones esperando su
  pareja, esas no se clasifican.
- Kafka y los consumidores usan ~0.4 núcleos en la máquina de desarrollo. En
  una sola máquina casi saturada, hasta eso se nota en la cola de latencia:
  argumento para separarlos en la nube ([ADR-0023](0023-redes-como-subredes-privadas.md)).
