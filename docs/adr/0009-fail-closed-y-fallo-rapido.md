# ADR-0009: Fail-closed y fallo rápido ante fallas de infraestructura

- **Estado:** Aceptada
- **Fecha:** 2026-10-03
- **Etapa:** 3

## Contexto

Si Redis no responde, el motor no puede verificar la transacción. Hay que
decidir qué responder y cuánto esperar antes de hacerlo.

## Decisión

- **Fail-closed:** sin verificar no se aprueba. Se responde `Unavailable`
  (motivo `REDIS_NO_DISPONIBLE`) o `DeadlineExceeded` (`REDIS_TIMEOUT`): el
  motor *no pudo decidir*, no decidió que es fraude. Lo mismo al arrancar:
  si Redis no responde al `PING`, el servicio no arranca.
- **El deadline llega hasta Redis:** go-redis **ignora el deadline del ctx
  por defecto** (usa sus propios timeouts de 3s). Se activa
  `ContextTimeoutEnabled: true`. Se encontró leyendo el código fuente de la
  librería.
- **Sin reintentos** (`MaxRetries: -1`, `DialerRetries: 1`): con los
  reintentos por defecto, con Redis caído cada request tardaba los 200ms
  enteros antes de fallar. Sin ellos falla en ~1ms. Ojo: en ambos campos el
  `0` significa "valor por defecto", no "desactivar".
- La configuración del cliente vive en un solo lugar,
  [`internal/redisclient`](../../internal/redisclient/redisclient.go).

## Alternativas consideradas

- **Fail-open (aprobar sin verificar):** más disponibilidad, a costa de dejar
  pasar fraude justo cuando no se ve. Si alguna vez se necesita, va como modo
  configurable y explícito, nunca como default.
- **Reintentar:** absorbe fallas de un instante, pero bajo una caída real
  todas las requests queman su deadline esperando, que es lo opuesto al load
  shedding.

## Consecuencias

- Una falla de un instante hace fallar esa request. Se acepta: el cliente
  puede reintentar con el mismo `tx_id` sin que cuente doble
  ([ADR-0013](0013-ambiguedad-del-timeout-e-idempotencia.md)).
- Medido: con Redis caído, `Unavailable` en ~1ms; al volver, el pool
  reconecta solo, sin reiniciar el servicio.
