# ADR-0002: Camino de decisión sincrónico, con gRPC y el deadline propagado

- **Estado:** Aceptada
- **Fecha:** 2026-09-20
- **Etapa:** 0–1 (setup y control de admisión)

## Contexto

El cliente necesita una respuesta definitiva (aprobada / rechazada / no se
pudo decidir) dentro de 200ms. Cada salto del camino (cliente → motor →
Redis, motor → Settlement) tiene que saber cuánto tiempo le queda, y dejar
de trabajar cuando ese tiempo se termina.

## Decisión

- El camino de decisión (*hot path*) es **sincrónico**: request/respuesta con
  gRPC sobre HTTP/2, contrato en [`proto/transactions.proto`](../../proto/transactions.proto).
- El deadline viaja con la request: el cliente fija un timeout, gRPC lo
  manda en el header `grpc-timeout` y el servidor lo recibe en el
  `context.Context`. **Todo** I/O del hot path (Redis, Settlement) recibe
  ese mismo `ctx`, nunca `context.Background()`.
- Una llamada **sin deadline** se rechaza (`InvalidArgument`, motivo
  `SIN_DEADLINE`): es un bug del cliente, y procesarla "sin límite" lo
  taparía.

## Alternativas consideradas

- **REST/JSON:** no tiene propagación de deadline estándar ni contrato
  tipado; habría que reinventar ambos.
- **Mensajería asíncrona (colas, Kafka) en el camino de decisión:** ver
  [ADR-0019](0019-kafka-como-columna-de-eventos.md). Rompe la garantía de
  *flujo completo acotado*: el cliente deja de tener una respuesta definitiva
  dentro del deadline.

## Consecuencias

- `grpc-timeout` es una **duración**, no un instante: el deadline sobrevive
  aunque los relojes de distintos hosts no coincidan.
- Cada componente puede cortar en cuanto el deadline vence, y el corte se
  propaga hacia abajo sin código extra.
- Las librerías tienen que respetar el `ctx` de verdad: go-redis no lo hace
  por defecto (ver [ADR-0009](0009-fail-closed-y-fallo-rapido.md)).
