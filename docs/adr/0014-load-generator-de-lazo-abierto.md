# ADR-0014: Load generator de lazo abierto

- **Estado:** Aceptada
- **Fecha:** 2026-10-04
- **Etapa:** 6

## Contexto

Para evaluar un sistema de tiempo real bajo carga hace falta un generador que
no esconda la latencia. Un generador de *lazo cerrado* (N workers que esperan
cada respuesta para mandar la siguiente) afloja justo cuando el sistema se
pone lento, y las latencias salen mejores de lo que son (*coordinated
omission*).

## Decisión

[`cmd/loadgen`](../../cmd/loadgen/main.go):
- **Lazo abierto:** las llegadas siguen un proceso de Poisson (intervalos
  exponenciales de media 1/RPS) y cada una sale en su propia goroutine, sin
  esperar a las anteriores. Los instantes se calculan sobre el reloj
  absoluto, para no acumular deriva. Con semilla fija, la secuencia es
  reproducible.
- **La latencia se mide desde el instante programado**, no desde el envío
  real; el retraso de lanzamiento se reporta aparte (si crece, la corrida no
  es válida).
- **Datos:** categoría, monto y coordenadas del comercio de `fraudTest.csv`,
  con **una tarjeta única por transacción**. Con las 924 tarjetas reales, a
  350 RPS cada una recibiría ~3.8 transacciones cada 10s y casi todo daría
  `CARD_TESTING` (compresión temporal del dataset).
- **Clasificación** por `ErrorInfo` ([ADR-0005](0005-fraude-no-es-error.md)):
  `PROCESSED`, `EARLY_DROP`, `DEADLINE_MISS`, `AMBIGUOUS` (se reintenta con el
  mismo `tx_id`), `ERROR`.

## Alternativas consideradas

- **Lazo cerrado:** subestima la latencia bajo carga.
- **Reproducir las tarjetas reales del dataset:** mide el falso positivo por
  compresión, no el sistema.

## Consecuencias

- Los chequeos de la mandatoria corren pero casi no rechazan; los rechazos
  salen del scoring (~1.1%). Se puede sumar tráfico de ataque a propósito.
- Fue lo que reveló el colapso por congestión
  ([ADR-0003](0003-control-de-admision-temprano.md)): en la notebook de
  desarrollo, 350 RPS respondieron 100% a tiempo y 700 RPS solo 56%.
