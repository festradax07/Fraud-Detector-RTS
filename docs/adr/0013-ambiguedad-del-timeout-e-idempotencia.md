# ADR-0013: Ambigüedad del timeout: idempotencia y "el primer resultado final gana"

- **Estado:** Aceptada
- **Fecha:** 2026-10-04
- **Etapa:** 5

## Contexto

Midiendo, aparecieron transacciones que vencieron esperando a Settlement
(el cliente recibió `DeadlineExceeded`) pero que **sí** quedaron comprometidas:
la respuesta se atrasó después del commit. Cuando una llamada por red vence,
no se sabe si del otro lado se ejecutó; no se puede eliminar, solo acotar.
gRPC lo dice en la definición del código: `DEADLINE_EXCEEDED` "puede
devolverse aunque la operación se haya completado".

## Decisión

- Un timeout esperando a Settlement es un **resultado desconocido** (motivo
  `SETTLEMENT_TIMEOUT`), no un "falló". El cliente **reintenta con el mismo
  `tx_id`** y el sistema converge al resultado real.
- **Idempotencia de verdad: el primer resultado final gana.** `COMMITTED` y
  `REJECTED_FRAUD` son finales; `ABORTED_TIMEOUT` no (no se comprometió nada).
  Settlement lo aplica con un **script Lua** atómico ("si ya hay resultado
  final, devolverlo; si no, escribir"): hecho como `HGET` + `HSET` desde el
  cliente, dos reintentos simultáneos se colarían entre los dos comandos.
- **El motor obedece al registro:** si un reintento se evalúa distinto (por
  ejemplo, la tarjeta entró en la blacklist entre medio), al cliente se le
  responde lo registrado. Si la plata ya se comprometió, decir "fraude" sería
  mentirle.
- En el control de velocidad, el mismo `tx_id` es el mismo miembro del ZSET:
  el reintento no suma intentos.

## Alternativas consideradas

- **Sobrescribir el registro (`HSET`):** era la primera versión. Un test
  mostró que un reintento con el `emission_timestamp` original **pisaba un
  `COMMITTED` con `ABORTED_TIMEOUT`**. "Mismos datos, mismo resultado" no es
  idempotencia.
- **Compensación (*saga*):** al vencer, el motor manda "deshacé esto".
  Requiere un RPC nuevo, y la compensación también puede fallar o llegar
  tarde: la ambigüedad se corre de lugar.

## Consecuencias

- Si el cliente no reintenta, queda una transacción comprometida que el
  cliente cree fallida; resolverlo es conciliación contable, fuera del
  alcance del proyecto.
- **Problema conocido:** un `Unavailable` por una conexión que se cae a mitad
  de una request (por ejemplo, el motor muere con SIGKILL) también es
  ambiguo, y el load generator hoy no lo reintenta.
- Bajo saturación, los reintentos suman carga a un sistema ya saturado
  (*tormenta de reintentos*); se mitiga con un presupuesto de reintentos y
  *backoff* (pendiente).
