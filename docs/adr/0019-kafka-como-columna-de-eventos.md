# ADR-0019: Kafka como columna de eventos, fuera del camino de decisión

- **Estado:** Aceptada (pendiente de implementación)
- **Fecha:** 2026-10-19
- **Etapa:** 8 (pasa a ser parte del núcleo)

## Contexto

Hay dos problemas abiertos: los registros de Settlement crecen sin límite en
Redis ([ADR-0012](0012-settlement-segundo-servicio-sincronico.md)), y las
estadísticas del scoring son fijas ([ADR-0010](0010-modelo-de-scoring.md)).
En arquitecturas de pagos reales, Kafka aparece con *topics* por dominio. La
pregunta es si va en el camino de decisión o al costado.

## Decisión

- **El camino de decisión sigue siendo sincrónico**
  ([ADR-0002](0002-camino-de-decision-sincronico-con-grpc.md)). Es lo que da
  el **flujo completo acotado**: dentro de los 200ms, el cliente sabe el
  resultado final, o sabe que no lo sabe.
- **Kafka va como columna de eventos, al costado:** el motor y Settlement
  publican lo que pasó (transacción evaluada, liquidada, alerta de fraude),
  con clave = tarjeta para mantener el orden por tarjeta. Consumen, cada uno
  a su ritmo: auditoría (registro durable; Redis puede pasar a guardar con
  TTL), estadísticas en vivo (actualizan las del scoring en Redis, aprendiendo
  de transacciones confirmadas) y alertas.
- **Publicar nunca bloquea el hot path:** productor asíncrono con buffer
  acotado; si se llena, se descarta el evento y se cuenta en una métrica.
- El **modo asíncrono** (la transacción entra por un topic y la decisión
  sale por otro) se implementa solo como **experimento de comparación**, no
  como camino principal.

## Alternativas consideradas

- **Kafka en el camino de decisión:**
  - las pausas por *rebalance* del grupo de consumidores (deploy, escalado,
    caída) frenan el consumo segundos: todas las transacciones en vuelo
    pierden el deadline;
  - el mensaje no lleva `ctx` ni `grpc-timeout`: el deadline tiene que ir
    adentro como instante absoluto, y eso depende de relojes sincronizados
    entre hosts;
  - la respuesta vuelve por otro topic (correlación por `tx_id`): no hay
    garantía de flujo completo acotado.
  - A favor, y por eso vale el experimento: Kafka le da al sistema la cola que
    le falta; la espera en la partición consume `D_rem` y la admisión
    ([ADR-0003](0003-control-de-admision-temprano.md)) empezaría a actuar.
- **Sin Kafka:** los dos problemas abiertos quedan sin resolver.

## Consecuencias

- La entrega es *al menos una vez*: los consumidores tienen que ser
  idempotentes por `tx_id` (ya es la regla del sistema, ver
  [ADR-0013](0013-ambiguedad-del-timeout-e-idempotencia.md)).
- Broker en modo KRaft (Kafka 4 ya no usa ZooKeeper): uno en Compose, y en
  la nube como servicio administrado o dentro del mismo clúster.
- El experimento de modo asíncrono convierte "¿sirve Kafka para 200ms?" en
  un resultado medido por entorno.
