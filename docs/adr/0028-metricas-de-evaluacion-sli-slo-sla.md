# ADR-0028: Métricas de evaluación y objetivos (SLI, SLO y SLA)

- **Estado:** Aceptada
- **Fecha:** 2026-10-27
- **Etapa:** transversal (la evaluación por entornos, [ADR-0018](0018-evaluar-en-distintos-entornos.md))

## Contexto

El sistema mide mucho: el dashboard tiene más de 30 paneles
([ADR-0015](0015-observabilidad-prometheus-grafana.md)), el loadgen deja un
CSV por transacción y la auditoría cruza cada respuesta con su registro.
Pero nunca se escribió **cuáles de esas métricas son las oficiales**: las
que se reportan, se comparan entre entornos y dicen si el sistema cumple.
Estaban repartidas entre la guía del dashboard, el loadgen y las
conversaciones del equipo.

Además, hay una tensión de fondo. En el tiempo real duro clásico, la
garantía es **determinística**: un análisis de planificabilidad demuestra,
con WCETs probados, que ninguna tarea se pasa de su deadline. Eso solo es
posible con hardware y planificación controlados. En la nube (CPU
compartida, red, GC, vecinos ruidosos) no se puede probar un WCET: solo se
puede **medir** cuántas veces se cumple el deadline.

## Decisión

### 1. Tres capas de métricas

| Capa | Para qué | Ejemplos |
|---|---|---|
| **Diagnóstico** | explicar por qué pasa algo | CPU, GC, duración de cada fase, C_net, D_rem al llegar, lag |
| **Contrato de tiempo real** (las oficiales) | decir si se cumple y comparar entornos | ver el punto 2 |
| **SLI / SLO / SLA** | enunciar la garantía: qué se promete, hasta qué carga y en qué ventana | ver los puntos 3 a 5 |

Las métricas de diagnóstico no se reportan como resultado: se usan para
explicar los resultados.

### 2. Las métricas oficiales del contrato de tiempo real

**Fuente principal: el CSV del loadgen** (una fila por transacción, latencia
exacta). Los percentiles de Prometheus salen de buckets interpolados y no se
usan para reportar. **Se mide en el borde** del sistema (el loadgen, que hace
de pasarela; en AWS también el balanceador): el servidor no se entera de un
deadline incumplido, así que una medición de su lado lo esconde.

**Transacciones válidas:** todas las que lanzó el loadgen, menos las
rechazadas por request inválida (`InvalidArgument`: es un error del cliente,
no del sistema). Se cuenta el **primer intento** de cada transacción.

| Métrica | Definición |
|---|---|
| **Deadline Miss Ratio (DMR)** | 1 − (decisiones a tiempo / válidas). Una decisión a tiempo es `PROCESSED` con latencia ≤ 200ms desde el instante programado. EARLY_DROP, DEADLINE_MISS, AMBIGUOUS y ERROR cuentan como incumplidos: el cliente no recibió una decisión a tiempo |
| **Distribución de la latencia** | P50, P99, P99.9 y **máximo observado**. El máximo se compara con el presupuesto (115ms de peor caso de diseño, [ADR-0011](0011-costo-simulado-y-wcet.md)): es lo más cercano a un WCET medido |
| **Tasa de degradación** | decisiones sin fase opcional / decisiones. Es el "error" de la Computación Imprecisa de Liu ([ADR-0004](0004-computacion-imprecisa.md)) |
| **Tasa de rechazo temprano** | EARLY_DROP / válidas: cuánto actúa la admisión ([ADR-0003](0003-control-de-admision-temprano.md)) |
| **Capacidad bajo SLO** | el mayor RPS sostenido en el que se cumple el SLO de deadline (punto 4). Se mide con la escalera o el perfil del día ([ADR-0026](0026-perfil-de-carga-diario.md)) |
| **Correctitud** | transacciones cruzadas por la auditoría que no dieron INCONSISTENTE ([ADR-0025](0025-consumidores-y-auditoria.md)) |
| **Ambiguas comprometidas** | AMBIGUA_COMPROMETIDA / cruzadas: el cliente vio error y se cobró |

### 3. Los SLIs

Cada SLI es una de las métricas oficiales, expresada como **eventos buenos /
eventos válidos**:

| SLI | Eventos buenos | Eventos válidos |
|---|---|---|
| **Deadline** | decisiones a tiempo (≤ 200ms) | transacciones válidas |
| **Latencia** | transacciones con latencia ≤ 100ms | transacciones válidas |
| **Calidad de la decisión** | decisiones con fase opcional | decisiones |
| **Correctitud** | cruces que no dieron INCONSISTENTE | cruces de la auditoría |
| **Riesgo** | cruces que no dieron AMBIGUA_COMPROMETIDA | cruces de la auditoría |
| **Frescura del camino frío** | segundos con el lag de la auditoría ≤ 60s | segundos de la corrida |

### 4. Los SLOs

Un SLO sin una carga declarada no significa nada: pasado el punto de
quiebre, ningún objetivo se cumple. Por eso cada SLO vale **dentro de una
envolvente de carga**, y la ventana es **la corrida** (en un sistema en
producción sería una ventana móvil de 30 días).

| SLI | SLO |
|---|---|
| Deadline | **≥ 99%** |
| Latencia | **≥ 99%** (el P99 queda en ≤ 100ms, la mitad del deadline) |
| Calidad de la decisión | **≥ 99%** con scoring |
| Correctitud | **100%** (una sola INCONSISTENTE es un bug) |
| Riesgo | **≥ 99,9%** (≤ 0,1% de ambiguas comprometidas) |
| Frescura del camino frío | **≥ 99%** |

**Envolvente de carga inicial: hasta 400 RPS sostenidos**, en Docker
Compose en la notebook de desarrollo. Sale de lo medido ahí (el quiebre está
cerca de 500 RPS) con un margen. Cada entorno tiene su propia envolvente, y
medirla es parte de la evaluación: **el SLO es el mismo para todos los
entornos; lo que cambia es hasta qué carga se cumple** (la capacidad bajo
SLO).

Estos objetivos son **decisiones de diseño, no mediciones**. Se revisan con
los resultados de la evaluación y de la calibración (Etapa 10).

### 5. Presupuesto de error y SLA

- **Presupuesto de error:** el complemento de cada SLO. Con el 99% del SLO
  de deadline, el 1% de las transacciones de la corrida puede incumplir. El
  informe de cada corrida dice cuánto del presupuesto se consumió. Si una
  corrida dentro de la envolvente lo agota, hay un problema que investigar
  antes de seguir midiendo.
- **SLA hipotético:** el proyecto no tiene un cliente real. Para mostrar la
  diferencia entre lo que se apunta y lo que se promete, se declara un SLA
  ilustrativo **más laxo que el SLO**: **98% de decisiones a tiempo hasta
  la envolvente de carga, por mes**, con una bonificación como consecuencia
  si no se cumple. La distancia entre el SLO (99%) y el SLA (98%) es el
  margen para detectar y corregir antes de incumplir el contrato.

### 6. El tipo de tiempo real

Cada transacción tiene un **deadline duro**: una respuesta tarde no tiene
valor y cuenta como falla. Pero la garantía del **sistema** es
**estadística** (el DMR medido contra un SLO), no determinística. En la
terminología de tiempo real, un sistema en el que un resultado tardío no
tiene valor pero se toleran fallas ocasionales se suele llamar *firm
real-time*. Es la forma honesta de enunciar la garantía de un sistema de
tiempo real sobre infraestructura que no se puede analizar formalmente.

## Alternativas consideradas

- **Quedarse con el dashboard:** sirve para diagnosticar, pero no dice qué
  se promete ni permite decir si un entorno "cumple".
- **Garantía determinística** (análisis de planificabilidad con WCETs
  probados): no es posible en la nube.
- **Medir los SLIs del lado del servidor:** el servidor no ve los deadlines
  incumplidos del cliente.
- **SLOs sin envolvente de carga:** cualquier SLO se incumple pasado el
  quiebre; sin la carga, el número no dice nada.

## Consecuencias

- La evaluación entre entornos se enuncia como **"capacidad bajo SLO"** más
  la distribución completa de las métricas oficiales, y no como números
  sueltos.
- Falta una herramienta que calcule los SLIs y el presupuesto consumido a
  partir del CSV del loadgen y la auditoría, igual en todos los entornos.
- Una fila de SLOs en el dashboard (SLI contra objetivo, presupuesto
  restante y velocidad de consumo) queda como mejora: el dashboard mide con
  buckets, y la fuente oficial es el CSV.
- Los valores de los SLOs y de la envolvente se revisan con la evaluación.
  Un cambio de objetivo es un ADR nuevo.
