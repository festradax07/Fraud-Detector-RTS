# ADR-0024: Eventos en protobuf, publicados sin bloquear

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** 8 (Kafka)

## Contexto

[ADR-0019](0019-kafka-como-columna-de-eventos.md) fijó que Kafka va al costado
del camino de decisión y que publicar nunca bloquea. Faltaba decidir el
formato de los eventos, la librería y cómo cumplir "no bloquear" en la
práctica.

## Decisión

- **Formato: protobuf**, en [`proto/eventos.proto`](../../proto/eventos.proto),
  aparte del contrato gRPC. Los eventos **reusan** sus mensajes
  (`EvaluationResponse`, `TxStatus`): un solo lenguaje de esquema para las
  llamadas y los eventos, sin estructuras duplicadas que se desincronicen.
  Reglas de evolución: nunca reusar el número de un campo; los cambios
  compatibles agregan campos; los incompatibles suben `version`.
- **Topics y claves:** `drts.transacciones.evaluadas` (lo que respondió el
  motor, clave = `card_token` para mantener el orden por tarjeta) y
  `drts.transacciones.liquidadas` (lo que registró Settlement, clave =
  `tx_id`). El motor publica también cuando Settlement no confirmó (el caso
  ambiguo), para que la auditoría pueda cruzarlo.
- **Librería: `franz-go`** (Go puro). `confluent-kafka-go` necesita CGO
  (envuelve librdkafka) y rompería la compilación estática y las imágenes
  distroless ([ADR-0020](0020-imagenes-salud-y-compose.md)).
- **No bloquear** ([`internal/eventos`](../../internal/eventos/eventos.go)):
  - `TryProduce`, no `Produce`: `Produce` **bloquea** cuando el buffer está
    lleno; `TryProduce` falla al instante. Lo que no entra se descarta y se
    cuenta (`drts_eventos_descartados_total{topic, motivo}`).
  - Buffer de 10.000 eventos (~10s a 1000 eventos/s) y plazo de entrega de
    30s (el default es ilimitado: con Kafka caído el buffer quedaría lleno
    para siempre). Puntos de partida de diseño.
  - `context.Background()` a propósito: el evento no tiene que morir con la
    request.
  - Productor idempotente con `acks=all` (defaults de `franz-go`): un evento
    confirmado no se pierde ni se duplica en el broker; la espera es
    asíncrona.
- **Kafka es opcional:** sin `KAFKA_BROKERS`, el productor es `nil` y no hace
  nada. Al apagar, se intenta entregar el buffer después de drenar las
  requests en vuelo.

## Alternativas consideradas

- **JSON:** se lee a ojo con las herramientas de Kafka, pero duplica en otro
  formato estructuras que ya están en el `.proto`.
- **`segmentio/kafka-go`:** más simple, menos mantenida.
- **Productor sincrónico:** cada transacción esperaría a Kafka.

## Consecuencias

- Medido en la máquina de desarrollo, misma carga (300 RPS), intercalado: con
  Kafka arriba, caído o deshabilitado, el camino de decisión da igual (100% a
  tiempo, P50 ~19ms, P99 31–33ms).
- Con Kafka caído, los eventos esperan en el buffer; si Kafka no vuelve, se
  pierden y queda registrado cuántos (al apagar, un Warn con los pendientes).
- Los reintentos de transacciones ambiguas vuelven a publicar: los
  consumidores tienen que ser idempotentes por `tx_id`.
- Leer los eventos a ojo requiere decodificarlos (un consumidor que los
  muestre como JSON con `protojson`).
