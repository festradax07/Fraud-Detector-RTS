# ADR-0008: Redis como estado del hot path, con un solo viaje atómico

- **Estado:** Aceptada
- **Fecha:** 2026-10-03
- **Etapa:** 3

## Contexto

El estado de detección pasó de la memoria del proceso a Redis (para que
varias instancias del motor lo compartan). Cada comando suelto a Redis es un
viaje de ida y vuelta por la red, y la fase mandatoria tiene un presupuesto
de 20ms.

## Decisión

**Modelo de datos** (prefijo `drts:` en el servicio; uno único por test):

| Dato | Clave | Tipo | TTL |
|---|---|---|---|
| Blacklist | `blacklist` | SET | — |
| Intentos | `vel:<tarjeta>` | ZSET (miembro = `tx_id`, score = ms) | 10s |
| Posición | `pos:<tarjeta>` | STRING `lat,lon,ms` | 26h |
| Estadísticas | `stats:<categoría>` | HASH | — |

- **Un solo viaje:** `SISMEMBER` + `ZREMRANGEBYSCORE`/`ZADD`/`ZCARD`/`EXPIRE` +
  `GET` + `HGETALL` van juntos en un **`TxPipeline`** (`MULTI/EXEC`). Lo que
  ahorra viajes es el *pipelining*; lo que agrega `TxPipeline` es la
  **atomicidad**: el conteo no depende de cómo se intercalen dos requests de
  la misma tarjeta. El `SET` de la posición es un segundo viaje, solo para
  aprobadas (depende del resultado).
- En el ZSET el miembro es el `tx_id`: con el timestamp, dos intentos en el
  mismo milisegundo se fundirían. Los scores van en **milisegundos**: son
  `double` (exactos hasta ~9e15) y `UnixNano` (~1.8e18) perdería precisión.
- El **TTL de la posición se calcula**: a 800 km/h se cruza la máxima
  distancia de la Tierra (πR ≈ 20.015km) en ~25h; pasado eso la posición ya
  no puede causar un rechazo. Se redondea hacia arriba (26h).

## Alternativas consideradas

- **Comandos secuenciales:** ~6 viajes. Medido en la notebook de desarrollo:
  133µs de P50 contra 27µs con pipeline; la diferencia escala con el RTT.
- **Pipeline no transaccional:** mismo costo de red, pero sin atomicidad.

## Consecuencias

- Desaparece el mutex del proceso: la concurrencia la ordena Redis.
- Los TTL resuelven la fuga de memoria de claves de tarjetas que no vuelven.
- Las estadísticas viajan en el mismo pipeline aunque la request se degrade:
  son unos bytes; pedirlas después costaría un viaje entero.
