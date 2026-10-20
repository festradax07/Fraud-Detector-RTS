# ADR-0005: Un fraude no es un error; las fallas llevan motivo (ErrorInfo)

- **Estado:** Aceptada
- **Fecha:** 2026-09-24 (motivos con ErrorInfo: 2026-10-04)
- **Etapa:** 2, extendida en la 6

## Contexto

El cliente tiene que poder distinguir tres situaciones: el motor decidió
"no" (fraude), el motor no pudo decidir (infraestructura caída, tiempo
agotado) y el cliente mandó algo inválido. Además, varias fallas distintas
comparten código gRPC: un `DeadlineExceeded` puede ser un early drop, un
timeout esperando a Settlement (resultado ambiguo) o el timer del propio
cliente.

## Decisión

- **Un fraude es una respuesta exitosa:** código `OK`, `is_fraud = true` y un
  código fijo en `rejection_reason` (`BLACKLIST`, `CARD_TESTING`,
  `VIAJE_IMPOSIBLE`, `RIESGO_ALTO`). El detalle (intentos, km/h) va al log.
- **Los errores gRPC quedan para "no pude decidir"** y para requests
  inválidas.
- Cada error lleva un detalle estándar `google.rpc.ErrorInfo` con un
  `Reason` legible por máquina (ver
  [`internal/fallas`](../../internal/fallas/fallas.go)): el código gRPC dice
  la *clase* de error y el motivo dice *cuál*. Un `DeadlineExceeded` sin
  motivo lo generó el timer del cliente.

## Alternativas consideradas

- **Fraude como error gRPC:** mezcla "el motor funcionó y dijo que no" con
  "el motor no funcionó".
- **Distinguir fallas por el texto del mensaje:** frágil; cualquier cambio de
  redacción rompe a los clientes.
- **Un trailer propio con el motivo:** funciona, pero es una convención del
  proyecto; `ErrorInfo` es el estándar de gRPC.

## Consecuencias

- El load generator clasifica cada resultado sin parsear texto, y la
  métrica `drts_requests_total{codigo, motivo}` separa los casos.
- Los motivos son parte del contrato con los clientes: cambiarlos rompe a
  quien los usa.
