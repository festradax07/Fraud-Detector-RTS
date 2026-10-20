# ADR-0010: Scoring con Z-score sobre ln(monto), ponderado por categoría

- **Estado:** Aceptada
- **Fecha:** 2026-10-03
- **Etapa:** 4

## Contexto

La fase opcional puntúa el riesgo de una transacción. Hacía falta un modelo
explicable, de costo acotado, y cuyos parámetros no fueran inventados.

## Decisión

```
z     = (ln(monto) − media_log[categoría]) / desvío_log[categoría]
score = 1 − 0.25^(peso[categoría] · z² / 9)        rechazo si score > 0.75
```

- **Logaritmo del monto:** los montos son log-normales (en `shopping_net` la
  media es $88 y la mediana $8); el Z-score supone forma de campana.
- **z², no z:** el fraude no siempre es un monto alto. En `gas_transport` los
  fraudes tienen z ≈ −6 (cargas chiquitas para probar la tarjeta).
- **Peso = tasa de fraude de la categoría / tasa global** (de 0.27 a 3.03):
  sale de los datos.
- Con `peso · z² = 9` el score da exactamente 0.75: para peso 1 es |z| = 3,
  la regla de las 3 sigmas. Es la única decisión de diseño de la fórmula.
- **Línea base precalculada:** las estadísticas se calculan con
  [`cmd/cargar-stats`](../../cmd/cargar-stats/main.go) (algoritmo de Welford)
  sobre `fraudTrain.csv` del dataset sintético *Credit Card Transactions Fraud
  Detection* (Sparkov, CC0) y se cargan en Redis. `fraudTest.csv` queda para
  evaluar.

## Alternativas consideradas

- **Red neuronal feedforward:** más potente, pero opaca y abre un proyecto de
  ML aparte (entrenamiento, exportar pesos, inferencia). Lo que le importa al
  mecanismo de slack es el costo, y ese se simula
  ([ADR-0011](0011-costo-simulado-y-wcet.md)).
- **Aprender las estadísticas del tráfico, en el hot path:** sin datos al
  arrancar (*cold start*), un atacante paciente puede ir corriendo la media
  con compras que pasan, y escribe estado global mientras compite contra el
  deadline. La versión realista (asíncrona, fuera del hot path) está en
  [ADR-0019](0019-kafka-como-columna-de-eventos.md).

## Consecuencias

- Contra `fraudTest.csv`: detecta el 65.6% de los fraudes y rechaza el 0.92%
  de las legítimas. Es un dato de referencia: la precisión no es el objetivo
  del proyecto.
- En cada Redis nuevo hay que correr `make cargar-stats`; sin estadísticas,
  toda transacción es "categoría desconocida".
