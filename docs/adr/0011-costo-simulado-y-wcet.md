# ADR-0011: Costo de la fase opcional simulado; presupuestos por peor caso

- **Estado:** Aceptada
- **Fecha:** 2026-10-03
- **Etapa:** 4

## Contexto

El Z-score real tarda ~40ns. Con ese costo, degradar no ahorraría nada y el
mecanismo de slack no tendría qué demostrar. Además, al medir costos para
fijar presupuestos aparecen dos trampas.

## Decisión

- El scoring suma un **costo de CPU simulado** y controlable
  (`ITERACIONES_SCORING`, por defecto 10M ≈ 16ms), separado del cálculo del
  score: imita el perfil de costo de un modelo más pesado.
- **Los presupuestos se fijan contra el peor caso, no contra el promedio.**
  Medido en la notebook de desarrollo: con 40M iteraciones la media (65ms)
  entra en el WCET de 80ms, pero el P99 y el máximo (81ms) no.
- El loop está escrito para que el compilador no pueda eliminarlo: la
  primera versión arrancaba en 1.0, que es un punto fijo de la iteración; el
  compilador lo probó y **borró el cálculo** (el benchmark medía un loop
  vacío). Se detectó por un tiempo físicamente imposible (0.12ns por
  operación dependiente) y se confirmó con `go tool objdump`.

## Alternativas consideradas

- **Sin costo simulado:** el slack nunca importa.
- **Calibrar por el promedio de `testing.B`:** un porcentaje de transacciones
  rompería el presupuesto.

## Consecuencias

- La perilla se ajusta por entorno sin recompilar, para comparar entornos
  ([ADR-0018](0018-evaluar-en-distintos-entornos.md)).
- Todo benchmark se valida mirando qué mide, no solo cuánto da.
