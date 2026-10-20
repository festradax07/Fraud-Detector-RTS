# ADR-0004: Computación imprecisa: fase mandatoria siempre, opcional según slack

- **Estado:** Aceptada
- **Fecha:** 2026-10-03
- **Etapa:** 4

## Contexto

Hay chequeos sin los que no hay decisión segura (blacklist, velocidad,
viaje imposible) y otros que la mejoran pero no son indispensables (scoring
de riesgo). Si se espera siempre a todos, una transacción con poco tiempo
llega tarde; si se saltean siempre, se pierde precisión. Base teórica: Liu,
Lin, Shih, Yu & Zhao (1991), *Algorithms for Scheduling Imprecise
Computations*.

## Decisión

- La **fase mandatoria** (M) corre siempre.
- Después de ella se recalcula `D_rem` (importa cuánto queda *ahora*, con la
  mandatoria y el viaje a Redis ya pagados) y el slack
  `S = D_rem − C_settle − C_net − WCET(O)`.
- `S ≥ 0` → corre la **fase opcional** (O, scoring). `S < 0` → la respuesta
  sale solo con la mandatoria y `is_degraded = true`.
- El presupuesto que entra en el slack es el **WCET de diseño** de la
  opcional (80ms), no su costo promedio (ver
  [ADR-0011](0011-costo-simulado-y-wcet.md)).

## Alternativas consideradas

- **Correr siempre las dos fases:** responde tarde cuando hay poco tiempo.
- **Decidir con el costo promedio medido:** el promedio entra, el peor caso
  no; un porcentaje de transacciones rompería el deadline.
- **Cortar la opcional a mitad de camino:** posible en teoría (resultado
  monótono), pero agrega complejidad; se prefirió decidir antes de empezar.

## Consecuencias

- **Degradar es aceptar riesgo:** con poco slack, una compra que con scoring
  saldría `RIESGO_ALTO` se aprueba solo con la mandatoria. `is_degraded`
  deja constancia para auditoría.
- El diseño es conservador: con el WCET de diseño (80ms) se degrada aunque el
  scoring real tarde ~16ms. Recalibrar con datos es trabajo pendiente.
