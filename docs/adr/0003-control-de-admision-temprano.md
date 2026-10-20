# ADR-0003: Control de admisión temprano (load shedding)

- **Estado:** Aceptada
- **Fecha:** 2026-09-20 (presupuesto derivado: 2026-10-04)
- **Etapa:** 1, revisada en la 4

## Contexto

Si una transacción llega con tan poco tiempo que ni la fase mandatoria más
Settlement entran en lo que queda, procesarla es gastar CPU en algo que de
todas formas va a llegar tarde, y quitárselo a las que sí pueden cumplir.

## Decisión

Al llegar, **antes de cualquier trabajo**, el motor calcula
`D_rem = deadline − ahora`. Si `D_rem < WCET(M) + C_settle + C_net`, rechaza
en el acto con `DeadlineExceeded` y motivo `EARLY_DROP`.

El umbral **no es un número suelto**: vive en
[`internal/presupuesto`](../../internal/presupuesto/presupuesto.go) como
`MinParaAdmitir = WCETMandatoria + ReservaSettleNet` (20 + 15 = 35ms, puntos
de partida de diseño a recalibrar midiendo). El slack
([ADR-0004](0004-computacion-imprecisa.md)) usa las mismas piezas, así que
no pueden contradecirse. Las funciones son puras (reciben duraciones, no
leen el reloj): los bordes se testean a 1µs.

## Alternativas consideradas

- **Procesar todo y dejar que venza:** desperdicia CPU justo cuando el
  sistema está más cargado.
- **Constantes independientes para admisión y slack:** recalibrar una podía
  dejar la otra inconsistente.

## Consecuencias

- Rechazar una transacción condenada cuesta microsegundos.
- **Problema conocido (medido en la Etapa 6):** el motor atiende cada
  request en su propia goroutine, sin cola. Toda request llega con ~200ms y
  pasa la admisión; bajo saturación de CPU, todas compiten a la vez y vencen
  juntas. Resultado: **colapso por congestión** (el *goodput* cae de ~205/s a
  700 RPS a ~62/s a 1400 RPS) con casi ningún `EARLY_DROP`. La admisión solo
  protege si hay **espera antes del handler** que consuma `D_rem`: un pool de
  workers (pendiente) o una cola. La métrica `drts_drem_llegada_segundos` lo
  deja ver en vivo.
