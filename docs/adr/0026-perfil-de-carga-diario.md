# ADR-0026: Perfil de carga diario (Poisson no homogéneo)

- **Estado:** Aceptada
- **Fecha:** 2026-10-21
- **Etapa:** 6 (extiende el load generator; insumo de la evaluación por entornos)

## Contexto

El load generator ([ADR-0014](0014-load-generator-de-lazo-abierto.md))
genera una tasa **constante** por corrida. Un sistema de pagos no recibe una
tasa constante: hay horas de carga estable (la mañana), picos (el mediodía,
la tarde-noche) y valles (la madrugada). Para un sistema de tiempo real lo
que importa es el peor momento: cómo le va en el pico, y si se recupera
cuando el pico pasa. [ADR-0018](0018-evaluar-en-distintos-entornos.md) ya
pide medir todos los entornos con "el mismo perfil de carga", pero ese perfil
no existía.

El dataset Sparkov no sirve para sacar la forma del día: su distribución por
hora es un escalón (~63% del máximo de 0 a 11h, ~98% de 12 a 23h), sin valle
nocturno ni picos.

## Decisión

- **Proceso de Poisson no homogéneo:** la tasa es λ(t) = pico × fracción(hora).
  Se genera por *thinning* (Lewis y Shedler, 1979): candidatos a la tasa del
  pico, cada uno aceptado con probabilidad fracción(t). Sigue siendo lazo
  abierto, con semilla fija y reproducible.
- **El perfil es un archivo**, no código: `perfiles/*.csv` con puntos
  `hora,fraccion` (fracción del pico, entre 0 y 1), interpolados linealmente.
  Dos puntos con la misma hora forman un escalón (pico súbito). El mismo
  archivo va a todos los entornos.
- **La forma va en fracciones y el pico en `-rps`:** la forma del día es la
  misma en todos los entornos; la tasa absoluta se elige por experimento (por
  debajo o por encima del quiebre de cada entorno).
- **Compresión temporal** (`-compresion`, por defecto 60: 1 hora del perfil =
  1 minuto real). `-desde` y `-hasta` corren solo una ventana del día.
- **Reporte por franja horaria** además del global: el global promedia el
  día y esconde el pico. El CSV suma la columna `hora_perfil`, y el gauge
  `drts_loadgen_rps_objetivo` sigue la curva (Grafana la dibuja).
- **Sin perfil, nada cambia:** no se sortea nada extra, así que con la misma
  semilla las llegadas son idénticas a las de antes.
- **`perfiles/dia-habil.csv`** es una forma **ilustrativa**, no medida:
  madrugada ~10%, mañana estable 50%, mediodía 85%, pico máximo a las 19h.

## Alternativas consideradas

- **Tasa constante por tramos** (cambiar la λ cada N segundos): más simple,
  pero las rampas quedan como escaleras y hay que elegir N. El thinning da
  rampas exactas con el mismo código.
- **Correr varias corridas a tasas distintas** (la "escalera"): sirve para
  encontrar el quiebre, no para ver la transición ni la recuperación dentro
  de una misma corrida.
- **Sacar la forma del dataset:** no tiene una curva diaria realista (ver
  Contexto).
- **Perfil en RPS absolutos:** ataría el perfil a la capacidad de una
  máquina.

## Consecuencias

- **La compresión hace las rampas más bruscas que en la realidad:** con ×60,
  una subida de 2 horas pasa en 2 minutos. Es una prueba más exigente que el
  día real, no más blanda. El sistema tampoco tiene tiempo de "estabilizarse"
  en cada hora (por ejemplo, el GC).
- **Volumen:** un día completo con pico de 800 RPS son ~520.000
  transacciones, y otros tantos registros de Settlement en Redis (con TTL,
  [ADR-0025](0025-consumidores-y-auditoria.md)).
- **La forma ilustrativa es un supuesto declarado.** Si se consiguen datos
  públicos de pagos por hora, se reemplaza el CSV sin tocar código.
- Primera corrida (11 a 14h, pico 800 RPS, ×360, Compose en la notebook de
  desarrollo): las tasas logradas siguieron la curva (538, 692, 574 RPS por
  franja, contra 540, 680, 580 esperados), y el % a tiempo bajó en el pico
  del mediodía (76%) respecto de las franjas vecinas (94% y 86%).
