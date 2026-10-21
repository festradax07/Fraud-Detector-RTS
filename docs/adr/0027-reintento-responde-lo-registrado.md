# ADR-0027: Un reintento responde lo registrado; sin registro no hay decisión

- **Estado:** Aceptada (reemplaza la "política ante fallas" de
  [ADR-0012](0012-settlement-segundo-servicio-sincronico.md))
- **Fecha:** 2026-10-21
- **Etapa:** corrección posterior a la 8 (la encontró la auditoría del cold
  path, [ADR-0025](0025-consumidores-y-auditoria.md))

## Contexto

En una corrida de un día de carga
([ADR-0026](0026-perfil-de-carga-diario.md), pico 800 RPS, 521.482
transacciones), la auditoría encontró **50 transacciones INCONSISTENTE**: el
cliente recibió "rechazada (RIESGO_ALTO)" y Settlement tenía la transacción
**COMMITTED**. La secuencia:

1. Intento 1: llega con poco tiempo, sale degradado (sin scoring) y
   aprobado. Settlement lo compromete, pero la respuesta no llega a tiempo:
   el cliente recibe un error ambiguo y reintenta.
2. Intento 2, mismo `tx_id`: llega con 200ms, corre el scoring y da fraude.
3. La llamada a Settlement vence (sobrecarga). Por la política de
   ADR-0012 ("un rechazo se responde igual, ya es la decisión segura"), el
   motor responde "rechazada" **sin saber qué quedó registrado**.

Dos causas:

- **Con la Computación Imprecisa, dos intentos de la misma transacción
  pueden decidir distinto** (uno degradado, otro con scoring). La regla de
  [ADR-0013](0013-ambiguedad-del-timeout-e-idempotencia.md) ("el motor
  obedece al registro") solo se cumplía cuando Settlement respondía.
- **"Rechazar es lo seguro" vale en un primer intento, no en un reintento**:
  puede haber un commit de un intento anterior, ya hecho o todavía en vuelo.
  El motor no sabe si una request es un reintento.

## Decisión

- **Un reintento responde lo registrado.** El viaje 1 a Redis (el mismo
  `TxPipeline` de la fase mandatoria, [ADR-0008](0008-redis-un-viaje-atomico.md))
  trae también el registro de Settlement del `tx_id` (un `HGETALL` más, sin
  viaje extra). Si tiene un resultado **final** (COMMITTED o REJECTED_FRAUD),
  se responde eso, sin volver a evaluar. ABORTED_TIMEOUT no es final (un
  reintento a tiempo lo reemplaza): con ese estado se evalúa normalmente.
- **Sin registro confirmado no se responde una decisión.** Si la llamada a
  Settlement falla, la respuesta es el error ambiguo (`SETTLEMENT_TIMEOUT` o
  `SETTLEMENT_NO_DISPONIBLE`) también para un rechazo. El cliente reintenta
  con el mismo `tx_id` y converge a lo registrado. Esto cierra la ventana que
  la primera regla deja: el registro de un intento anterior que todavía está
  en vuelo cuando el reintento lee Redis.
- **El reintento igual le reenvía lo registrado a Settlement**, pero la
  respuesta no depende de esa llamada. Es para el cold path: si el intento
  anterior quedó escrito en Redis pero la respuesta de Redis no llegó a
  Settlement (`i/o timeout`), Settlement nunca publicó el evento, y la
  auditoría vería un EVENTO_PERDIDO. Settlement sigue siendo el único que
  publica lo registrado.
- El formato de la clave y la lectura del registro viven en
  `internal/settlement` (`Clave`, `LeerRegistro`), el dueño del dato.
- Métrica nueva: `drts_respuestas_registradas_total{estado}`.

## Alternativas consideradas

- **Solo la segunda regla (error ambiguo también en rechazos):** corrige la
  inconsistencia, pero cada reintento vuelve a pagar el scoring (~16ms de
  CPU) justo bajo sobrecarga, y puede volver a decidir distinto.
- **Solo la primera regla:** deja la carrera con un commit en vuelo.
- **Responder lo registrado sin avisar a Settlement:** se probó, y en la
  primera corrida aparecieron 2 EVENTO_PERDIDO (registros sin evento).
- **Que el motor publique el evento de lo registrado:** dos productores para
  el mismo hecho; Settlement dejaría de ser la fuente.

## Consecuencias

- Misma ventana de carga (11 a 14h del perfil, pico 800 RPS, ×60, Compose en
  la notebook de desarrollo), antes y después:

  | | Antes | Después (2 corridas) |
  |---|---|---|
  | % a tiempo | ~75% | 96,9% y 96,6% |
  | AMBIGUA_COMPROMETIDA | 17.719 (16%) | 1.073 y 1.213 (~1%) |
  | INCONSISTENTE | 20 | 0 |
  | REGISTRO_PERDIDO | 149 | 0 |

  La mejora de latencia es un efecto lateral: ~12.700 reintentos por
  corrida se respondieron sin scoring, y eso desarmó buena parte de la
  tormenta de reintentos. Es una corrida por lado (no una distribución) y la
  de "antes" era parte de un día completo: el orden de magnitud vale, los
  decimales no.
- **Un rechazo con Settlement caído ya no llega como "fraude"** sino como
  error. Para el comercio el efecto es el mismo (no se aprueba), y la
  alternativa era una respuesta que podía ser falsa. Es la objeción que
  ADR-0012 usó para descartar esta opción; con la inconsistencia medida, se
  invierte.
- El motor lee claves que escribe Settlement (acoplamiento de datos entre
  los dos servicios), sobre el mismo Redis.
- Un reintento que encuentra registro le suma una llamada a Settlement
  (~1ms; acotada por los 15ms de `ReservaSettleNet`) y le ahorra el scoring.
- El evento que Settlement republica en ese caso lleva la latencia del
  reintento, no la del intento que se registró.
