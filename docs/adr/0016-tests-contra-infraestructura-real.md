# ADR-0016: Los tests usan infraestructura real

- **Estado:** Aceptada
- **Fecha:** 2026-10-03
- **Etapa:** 3 en adelante

## Contexto

En un sistema de tiempo real la latencia de red y el comportamiento real de
cada componente (timeouts, reintentos, atomicidad) son parte de lo que se
estudia. Un sustituto en memoria los esconde.

## Decisión

- Los tests corren contra **Redis real** (Docker) y contra **servidores gRPC
  reales en puertos TCP reales** (`localhost:0`). No se usan Redis falsos
  (`miniredis`) ni mocks.
- Si la infraestructura no está, el test **falla** con un mensaje que dice
  cómo levantarla. No se usa `t.Skip`: un test salteado se ve "ok" y esconde
  que falta infraestructura.
- Cada test usa su propio prefijo de claves y se limpia al terminar.
- Los casos de falla se reproducen con infraestructura real modificada (un
  puerto donde no escucha nadie, un servidor real que tarda en responder).

## Alternativas consideradas

- **Mocks / `miniredis`:** tests más rápidos y sin Docker, pero no habrían
  encontrado que go-redis ignora el deadline del ctx, ni el estado de los
  canales gRPC al arrancar, ni la ambigüedad del timeout.

## Consecuencias

- Para correr los tests hace falta Redis levantado (`REDIS_ADDR` permite
  apuntar a otro, por ejemplo desde un contenedor).
- Los tests dependen del estado de esa instancia: con cientos de miles de
  claves de corridas de carga, `SCAN` (que recorre toda la base) se vuelve
  lento.
