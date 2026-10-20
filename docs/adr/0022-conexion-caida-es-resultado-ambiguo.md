# ADR-0022: Una conexión caída con la request en vuelo es un resultado ambiguo

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** refactor posterior a la 7.5 (complementa a ADR-0013)

## Contexto

[ADR-0013](0013-ambiguedad-del-timeout-e-idempotencia.md) estableció que un
timeout esperando a Settlement es un resultado desconocido, y que el cliente
reintenta con el mismo `tx_id`. Quedó como problema conocido el otro caso de
la misma familia: **una conexión que se cae con la request en vuelo**. Se vio
probando el apagado: con SIGKILL al motor, las transacciones en vuelo
recibieron `Unavailable` sin saber si del otro lado se había comprometido
algo. Lo mismo vale un salto más adentro: si se cae la conexión entre el
motor y Settlement, el motor responde `SETTLEMENT_NO_DISPONIBLE` sin saber si
Settlement llegó a comprometer.

## Decisión

El load generator (el cliente de referencia) trata como **ambiguo** y
reintenta con el mismo `tx_id`:

- `SETTLEMENT_TIMEOUT` (ya era así),
- `SETTLEMENT_NO_DISPONIBLE`,
- un `Unavailable` **sin motivo** `ErrorInfo`: no lo armó el servidor, lo armó
  gRPC del lado del cliente porque la conexión falló (categoría `AMBIGUOUS`,
  motivo `CONEXION_CAIDA`).

## Alternativas consideradas

- **Distinguir "no llegó a mandarse" de "se cayó en vuelo":** gRPC no lo
  expone de forma confiable. No hace falta: como el sistema es idempotente,
  reintentar es seguro en los dos casos.
- **No reintentar:** deja transacciones comprometidas que el cliente cree
  fallidas.

## Consecuencias

- Con el motor completamente caído, cada transacción se intenta hasta el
  máximo (por defecto 3) antes de darse por fallida. Falla rápido igual
  (conexión rechazada en microsegundos), pero suma carga: bajo saturación
  agrava la *tormenta de reintentos* de ADR-0013. Un presupuesto de
  reintentos con *backoff* sigue pendiente.
- Las corridas con fallas de conexión ahora reportan esas transacciones como
  `AMBIGUOUS` (con su resolución tras reintentar), no como `ERROR`.
