# Guía del dashboard "D-RTS — Tiempo real"

Para quien opera el sistema, para quien llega al proyecto y quiere entender
qué está mirando, y para comparar el mismo sistema en distintos entornos
(binarios nativos, Docker Compose, AWS;
[ADR-0018](../docs/adr/0018-evaluar-en-distintos-entornos.md)).

El dashboard se carga solo desde
[`grafana/dashboards/drts.json`](grafana/dashboards/drts.json): no hay que
importarlo ni configurarlo
([ADR-0015](../docs/adr/0015-observabilidad-prometheus-grafana.md)).
Grafana en http://localhost:3000 (sin login, solo `localhost`), Prometheus en
http://localhost:9090.

---

## Cómo se lee

De arriba hacia abajo, del **síntoma** a la **causa**:

| Fila | Pregunta | De quién son las métricas |
|---|---|---|
| 1 · Lo que ve el cliente | ¿Se cumple el deadline de 200ms? | el loadgen (el cliente) |
| 2 · Fraud-Engine por dentro | ¿Por qué? | el motor |
| 3 · Settlement | ¿Qué quedó registrado? | Settlement (y el motor, en los reintentos) |
| 4 · Runtime de Go y CPU | ¿Falta CPU? ¿Pausas del GC? | todos los procesos |
| 5 · Cold path (Kafka) | ¿Salen los eventos, se procesan, qué encuentran? | productores, consumidores, auditoría |

**Si la fila 1 está bien, el sistema está bien.** Las demás sirven para
entender por qué la fila 1 está como está.

### Antes de sacar conclusiones

1. **La fila 1 dice "No data" si no corre una carga.** Sus métricas las
   publica el loadgen, que solo existe mientras corre. Una corrida se sigue
   viendo mientras quede dentro del rango de tiempo elegido.
2. **Los dos entornos locales se suman.** Prometheus scrapea el entorno
   nativo y el de Compose (etiqueta `entorno`), y los paneles no filtran por
   entorno. Si los dos corren a la vez, los números se mezclan. Corré uno
   solo, o filtrá con `{entorno="compose"}` en Prometheus.
3. **Los percentiles salen de buckets.** Prometheus interpola dentro de cada
   tramo del histograma. Las formas y los cambios son confiables; los
   valores exactos, no. Ejemplos: un P99 "plano" en 250ms significa "por
   encima de 200ms" (el último tramo es 200–250ms); el D_rem en reposo se ve
   en ~175ms cuando el real ronda los 200ms. **Para números exactos, el CSV
   del loadgen** (`resultados/`), que tiene la latencia de cada transacción.
4. **El navegador también usa CPU.** El dashboard refresca cada 1s. En la
   misma máquina que corre el sistema, durante una medición conviene cerrar
   la pestaña o subir el refresco a 10s o más.

---

## Lectura en 30 segundos

Los seis paneles que dicen si hay un problema:

| Panel | Sano | Mirar | Problema |
|---|---|---|---|
| % respondidas a tiempo | ≥ 99% (verde) | 95–99% (amarillo) | < 95% (rojo) |
| Carga ofrecida vs goodput | las dos líneas juntas | el goodput se separa | el goodput **cae** mientras la ofrecida sube (colapso) |
| Retraso de lanzamiento P99 (fila 1) | pocos ms | > 5ms | > 20ms: **la corrida no vale** (el generador no sigue el ritmo) |
| CPU por proceso: total | lejos de los hilos de la máquina | cerca | en el tope |
| Discrepancias en el rango | 0 (verde) | — | > 0 (rojo): ver cuáles en "Auditoría: resultado del cruce" |
| Eventos descartados | 0 | — | > 0: el cold path pierde eventos |

---

## Diagnóstico rápido: síntoma → causa probable

| Lo que ves | Dónde confirmarlo | Causa probable |
|---|---|---|
| % a tiempo cae, goodput cae, ofrecida alta | CPU total en el tope; **todas** las fases suben juntas; requests en vuelo altas | **Saturación de CPU**: colapso por congestión. El sistema no tiene cola: todas las requests comparten el CPU y vencen juntas ([ADR-0003](../docs/adr/0003-control-de-admision-temprano.md)) |
| Lo mismo, y muchos AMBIGUOUS | "Requests por código": SETTLEMENT_TIMEOUT alto; "Reintentos resueltos con lo registrado" sube | **Tormenta de reintentos**: cada ambigua se reintenta y suma carga. Desde [ADR-0027](../docs/adr/0027-reintento-responde-lo-registrado.md) un reintento con resultado registrado no vuelve a pagar el scoring |
| Solo la fase `settlement` sube, con `c_net` arriba | CPU de Settlement normal | La **red** o el transporte entre motor y Settlement (en AWS: entre hosts) |
| Solo `c_settle` sube | CPU de Settlement, fase `mandatoria` | **Redis** lento del lado de Settlement |
| Solo la fase `mandatoria` sube | — | **Redis** lento del lado del motor |
| Suben las degradadas | slack P5 bajo 0 | Falta tiempo para el scoring: el sistema está cerca de su límite. **Es el diseño funcionando** (Computación Imprecisa, [ADR-0004](../docs/adr/0004-computacion-imprecisa.md)) |
| Suben los EARLY_DROP | D_rem P1 cerca de 35ms | Las requests llegan al handler casi sin tiempo: se perdió **antes** (cliente, transporte gRPC, planificación) |
| REDIS_NO_DISPONIBLE o SETTLEMENT_NO_DISPONIBLE con los dos servicios sanos | CPU en el tope | Visto bajo saturación: una espera interna termina clasificada como "no disponible". La decisión es correcta (fail-closed), el motivo es engañoso. **Pendiente de investigar** |
| Respuestas tardías > 0 | latencia del handler P99 > 200ms | El motor sigue trabajando para clientes que ya se fueron: trabajo desperdiciado |
| Picos cortos con carga baja | CPU del motor bajo, GC chico | Algo **fuera** del sistema le quitó CPU (otros programas en la misma máquina) |
| Publicados en 0 y buffer subiendo | — | **Kafka caído o lento.** El camino de decisión no se entera; los eventos esperan hasta 10.000 y después se descartan ([ADR-0024](../docs/adr/0024-eventos-en-protobuf-sin-bloquear.md)) |
| Lag subiendo | — | Un **consumidor** caído o lento. Los eventos esperan en Kafka (7 días) |
| INCONSISTENTE > 0 | auditoría en `auditoria/AAAA-MM-DD.jsonl` | **Un bug**: el cliente recibió una decisión distinta de la registrada. Tiene que ser 0 |
| Deriva > ±2σ en una categoría | — | El tráfico de esa categoría ya no se parece a la línea base del scoring |

---

## Panel por panel

★ = métrica clave para comparar entornos (ver la sección siguiente).

### Fila 1 · Lo que ve el cliente

**% respondidas a tiempo (≤ 200ms)** ★
- **Qué es:** la fracción de transacciones cuya respuesta llegó dentro del
  deadline, medida desde el instante **programado** de llegada (si el propio
  generador se atrasa, el atraso cuenta).
- **Métrica:** `drts_loadgen_latencia_segundos` (bucket `le="0.2"` sobre el
  total).
- **Cómo leerlo:** es **la** métrica de un sistema de tiempo real duro: una
  respuesta correcta que llega tarde es una falla. Verde ≥ 99%, amarillo ≥ 95%.
- **Ojo:** es un promedio de la ventana. En una corrida con picos, el número
  que importa es el **del pico** (el loadgen lo imprime por franja horaria).

**Carga ofrecida vs goodput** ★
- **Qué es:** *ofrecida* = transacciones nuevas lanzadas por segundo;
  *goodput* = las que terminaron PROCESSED a tiempo; *objetivo* = la tasa
  pedida (con `-perfil` dibuja la curva del día,
  [ADR-0026](../docs/adr/0026-perfil-de-carga-diario.md)).
- **Métricas:** `drts_loadgen_ofrecidas_total`,
  `drts_loadgen_resultados_total{categoria="PROCESSED"}`,
  `drts_loadgen_rps_objetivo`.
- **Cómo leerlo:** juntas = da abasto. Si el goodput se queda plano, se
  llegó a la capacidad. Si **baja** mientras la ofrecida sube, es colapso:
  rinde menos que con menos carga.
- **Ojo:** la ofrecida no incluye los reintentos. Bajo saturación, el motor
  recibe bastante más que esto (ver "Requests por código").

**Resultados por categoría**
- **Qué es:** el resultado del primer intento: PROCESSED, EARLY_DROP
  (rechazada por la admisión), DEADLINE_MISS (sin respuesta en 200ms),
  AMBIGUOUS (no se sabe si se comprometió: se reintenta con el mismo
  `tx_id`), ERROR.
- **Métrica:** `drts_loadgen_resultados_total{categoria}`.
- **Sano:** casi todo PROCESSED. Lo demás aparece al acercarse al límite.

**Latencia vista por el cliente** ★
- **Qué es:** P50, P99 y P99.9 desde el instante programado. Línea roja: el
  deadline.
- **Métrica:** `drts_loadgen_latencia_segundos`.
- **Ojo:** por encima de 200ms el valor es solo "más de 200": el último
  tramo del histograma es 200–250ms. Para la cola exacta, el CSV.

**Generador: en vuelo y retraso de lanzamiento** ★ (validez de la corrida)
- **Qué es:** *en vuelo* = transacciones esperando respuesta en el cliente;
  *retraso P99* = cuánto tarde sale cada transacción respecto de lo
  programado (eje derecho).
- **Métricas:** `drts_loadgen_en_vuelo`,
  `drts_loadgen_retraso_lanzamiento_segundos`.
- **Cómo leerlo:** si el retraso crece, el generador ya no lanza a la tasa
  pedida: lo que se mide es su límite, no el del sistema. Pasa cuando el
  loadgen comparte CPU con el sistema (medido: 20–30ms en los picos, en la
  notebook de desarrollo).

### Fila 2 · Fraud-Engine por dentro

**Requests en vuelo (servidor)**
- **Qué es:** cuántas requests atiende el motor en este instante. Cada una
  es una goroutine; no hay cola: todas comparten el CPU.
- **Métrica:** `drts_en_vuelo{servicio="fraud_engine"}`.
- **Cómo leerlo:** si se dispara, cada una tarda más y todas vencen juntas:
  la firma del colapso por congestión.

**Latencia del handler (servidor)**
- **Qué es:** lo que tarda el handler, medido por el interceptor del motor.
- **Métrica:** `drts_latencia_segundos{servicio="fraud_engine"}`.
- **Cómo leerlo:** comparala con la del cliente. La diferencia es lo que se
  pierde fuera del handler: red, transporte gRPC y espera de CPU.

**Respuestas tardías (servidor)**
- **Qué es:** requests que el motor terminó con el contexto ya vencido. El
  servidor no se entera de un DEADLINE_MISS: esta es la forma de verlo
  desde su lado.
- **Métrica:** `drts_respuestas_tardias_total`.
- **Sano:** 0. Si sube, es trabajo desperdiciado.

**D_rem al llegar** ★
- **Qué es:** cuánto le queda a cada request al llegar al handler, antes de
  la admisión. P50 y P1 (el 1% que llega con menos tiempo). Línea roja: el
  mínimo para admitir (35ms).
- **Métrica:** `drts_drem_llegada_segundos`.
- **Cómo leerlo:** en reposo se ve plano (~175ms y ~150ms, por la
  interpolación del tramo 150–200ms; el real ronda los 200ms). Si **el P1
  cae**, hay requests perdiendo tiempo antes del handler (medido bajo
  sobrecarga: ~35–50ms). Lo que cae bajo la línea roja son los EARLY_DROP.
- **Por qué compara entornos:** en AWS, con más saltos de red, es donde se
  vería espera antes del handler.

**Slack al decidir la fase opcional**
- **Qué es:** $S_i = D_{rem} − C_{settle} − C_{net} − WCET(O)$, medido después
  de la mandatoria. Arriba de 0 corre el scoring; debajo, la respuesta sale
  degradada.
- **Métrica:** `drts_slack_segundos`.
- **Cómo leerlo:** si el P5 cruza 0, una parte del tráfico se está
  degradando. El nivel en reposo (~120ms) también es interpolación (el real
  ronda 104ms).

**Decisiones por segundo**
- **Qué es:** lo que respondió el motor: aprobada o rechazada (con motivo),
  y si fue degradada.
- **Métrica:** `drts_decisiones_total{decision,motivo,degradada}`.
- **Cómo leerlo:** las degradadas aparecen en los picos (la Computación
  Imprecisa cambiando calidad por tiempo). Los rechazos RIESGO_ALTO rondan el
  ~1% en la carga del dataset.

**Requests por código y motivo** ★ (reintentos)
- **Qué es:** todas las respuestas del motor por código gRPC y motivo
  (ErrorInfo): OK, SETTLEMENT_TIMEOUT, EARLY_DROP, REDIS_TIMEOUT, etc.
- **Métrica:** `drts_requests_total{servicio="fraud_engine"}`.
- **Cómo leerlo:** la suma es lo que recibe el motor, **reintentos
  incluidos**. Medido en un pico: ~680/s de carga nueva, ~1.015/s recibidas.
  Si esta suma se separa mucho de la ofrecida de la fila 1, hay tormenta de
  reintentos.

**Duración de cada fase (P99)** ★ (C_net)
- **Qué es:** mandatoria (viaje 1 a Redis, presupuesto 20ms), opcional
  (scoring, presupuesto 80ms, ~16ms sin carga), settlement (llamada entera,
  reserva 15ms) = c_settle (adentro de Settlement) + c_net (red + gRPC).
- **Métrica:** `drts_fase_segundos{fase}`.
- **Cómo leerlo:** si sube **una** fase, es esa dependencia. Si suben
  **todas juntas**, el motor espera CPU.
- **Por qué compara entornos:** `c_net` es el costo de la distribución. En
  una máquina es ~0,3ms (P50); entre hosts en AWS va a ser lo primero que
  cambie.

### Fila 3 · Settlement

**Estados registrados**
- **Qué es:** lo que Settlement registró por segundo: COMMITTED,
  REJECTED_FRAUD, ABORTED_TIMEOUT (llegó después del deadline global: no se
  compromete nada).
- **Métrica:** `drts_settlement_estados_total{estado}`.
- **Cómo leerlo:** en un pico puede haber **más COMMITTED que goodput**: se
  comprometieron transacciones cuya respuesta no llegó a tiempo al cliente
  (las ambiguas). El cruce exacto lo hace la auditoría (fila 5). El
  contador incluye las llamadas de reintentos.

**Latencia de extremo a extremo (hasta Settlement)**
- **Qué es:** desde la emisión en el cliente hasta que la transacción llega a
  Settlement. Resolución de 1ms.
- **Métrica:** `drts_latencia_extremo_a_extremo_segundos`.
- **Ojo:** compara los relojes de dos procesos. Vale en una sola máquina;
  **entre hosts no se compara** sin declarar el error de sincronización. La
  latencia de referencia es la del cliente (fila 1).

**Reintentos resueltos con lo registrado**
- **Qué es:** reintentos que el motor respondió con lo que ya estaba
  registrado, sin volver a evaluar
  ([ADR-0027](../docs/adr/0027-reintento-responde-lo-registrado.md)).
- **Métrica:** `drts_respuestas_registradas_total{estado}`.
- **Cómo leerlo:** 0 con el sistema sano. Sube con las respuestas ambiguas.
  Cada uno ahorra el scoring y no puede decidir distinto del intento
  registrado.

### Fila 4 · Runtime de Go y CPU

**CPU por proceso (núcleos)** ★ (CPU por transacción)
- **Qué es:** núcleos que usa cada proceso, y el total.
- **Métrica:** `process_cpu_seconds_total` (rate).
- **Cómo leerlo:** el total contra los hilos de la máquina dice si el CPU
  está saturado. Dividido por las RPS da el **CPU por transacción** (medido
  en la notebook de desarrollo: ~21ms de CPU por transacción en el motor,
  casi todo del scoring simulado, `ITERACIONES_SCORING`). Eso fija la
  capacidad: núcleos disponibles / CPU por transacción.

**Pausa máxima del garbage collector**
- **Qué es:** la pausa *stop-the-world* más larga reciente, por proceso.
- **Métrica:** `go_gc_duration_seconds{quantile="1.0"}`.
- **Cómo leerlo:** pausas de unos ms son normales. Una pausa que coincide
  con un pico de latencia es sospechosa (se investiga con
  `GODEBUG=gctrace=1`).

**Goroutines**
- **Qué es:** goroutines vivas por proceso.
- **Métrica:** `go_goroutines`.
- **Cómo leerlo:** en el motor siguen a las requests en vuelo. Si crecen sin
  que crezca la carga, hay goroutines que no terminan (una fuga).

### Fila 5 · Cold path (Kafka)

El cold path va **al costado** del camino de decisión: si falla, las filas 1
a 4 no cambian ([ADR-0019](../docs/adr/0019-kafka-como-columna-de-eventos.md)).

**Eventos publicados por topic**
- **Qué es:** eventos confirmados por Kafka por segundo. *evaluadas*: los
  publica el motor (cada respuesta); *liquidadas*: Settlement (cada
  registro).
- **Métrica:** `drts_eventos_publicados_total{topic}`.
- **Sano:** los dos topics parejos con el tráfico.

**Eventos descartados**
- **Qué es:** eventos que no llegaron a Kafka, por motivo: `buffer_lleno`,
  `entrega` (venció el plazo de 30s), `serializacion`.
- **Métrica:** `drts_eventos_descartados_total{topic,motivo}`.
- **Sano:** 0 (las series existen en 0 desde el arranque, así que "0" y
  "no se mide" se distinguen).

**Buffer del productor**
- **Qué es:** eventos esperando ser entregados, por servicio. Línea roja: el
  máximo (10.000).
- **Métrica:** `drts_eventos_en_buffer`.
- **Cómo leerlo:** cerca de 0. Si sube, Kafka está lento o caído; al volver,
  se vacía.

**Lag de los consumidores**
- **Qué es:** eventos que le faltan leer a cada consumer group (auditoría,
  alertas, estadísticas).
- **Métrica:** `drts_consumo_lag` (suma de particiones).
- **Sano:** 0.

**Discrepancias en el rango del dashboard**
- **Qué es:** cuántas transacciones dieron AMBIGUA_COMPROMETIDA,
  REGISTRO_PERDIDO, EVENTO_PERDIDO o INCONSISTENTE en el rango elegido.
- **Métrica:** `drts_auditoria_resultados_total` (`increase` sobre el rango).
- **Ojo:** es aproximado (Prometheus extrapola en los bordes). El detalle
  exacto está en `auditoria/AAAA-MM-DD.jsonl`.

**Auditoría: resultado del cruce** ★ (riesgo de negocio)
- **Qué es:** cada transacción, cruzando lo que respondió el motor con lo
  que registró Settlement
  ([ADR-0025](../docs/adr/0025-consumidores-y-auditoria.md)).
- **Métrica:** `drts_auditoria_resultados_total{resultado}`.
- **Cómo leerlo:**

  | Resultado | Significa | Esperable |
  |---|---|---|
  | CONSISTENTE | respuesta = registro | casi todo |
  | NO_COMPROMETIDA | error y nada registrado | coherente; sube con la carga |
  | AMBIGUA_COMPROMETIDA | el cliente vio error, pero se cobró | **el riesgo de negocio de los picos** |
  | REGISTRO_PERDIDO | hubo decisión sin registro | ~0 desde ADR-0027 |
  | EVENTO_PERDIDO | falta el evento de un lado | 0 si no hay descartes |
  | INCONSISTENTE | la respuesta contradice el registro | **0 siempre: si no, es un bug** |

- El cruce va hasta ~30s atrasado (la ventana que espera la pareja).

**Alertas de fraude por motivo**
- **Qué es:** fraudes detectados por segundo: BLACKLIST, CARD_TESTING,
  VIAJE_IMPOSIBLE (mandatoria), RIESGO_ALTO (scoring).
- **Métrica:** `drts_alertas_total{motivo}`.

**Auditoría: transacciones esperando su pareja**
- **Qué es:** transacciones dentro de la ventana de 30s esperando el otro
  evento.
- **Métrica:** `drts_auditoria_pendientes`.
- **Cómo leerlo:** ≈ RPS × 30s y vuelve a 0 al terminar la carga. Si crece
  sin tasa que lo explique, falta un lado.

**Estadísticas en sombra: deriva por categoría**
- **Qué es:** cuánto se alejó la media en vivo de ln(monto) de la línea base,
  en desvíos. El motor **no** usa estas estadísticas (modo sombra).
- **Métrica:** `drts_stats_vivo_deriva_sigmas{categoria}`.
- **Sano:** cerca de 0 (medido: entre −0,24σ y +0,08σ con el dataset).

---

## El dashboard de la demo: "D-RTS — Demo en vivo"

Un segundo dashboard, en la misma carpeta (D-RTS), pensado para
**proyectarlo** durante la demo con QR
([ADR-0029](../docs/adr/0029-demo-interactiva-y-bloqueo-automatico.md)).
Lo ve el público, así que se lee sin saber de métricas. Sus datos salen del
gateway de la demo (`drts_demo_*`) y de los consumidores de Kafka.

| Fila | Panel | Qué muestra | Para decir en voz alta |
|---|---|---|---|
| **La clase comprando** | Compras · Fraudes detectados · Tarjetas bloqueadas · Sin decisión a tiempo | los totales de la clase | "Ya detectamos N fraudes" |
| | Latencia mediana (último minuto) | lo que tarda el sistema, sin el internet de cada uno | "La mitad de sus compras se decidió en menos de X ms" |
| | Compras por segundo | por resultado (aprobada, rechazada, error) | — |
| | Latencia de las compras de la clase | mediana y 95% contra la línea de 200ms | "Cuando saturo el motor, sube; cuando le doy CPU, baja" |
| **Las tarjetas de la clase** | Tarjetas de la clase | una fila por celular: su número de fantasía, su estado, sus compras y sus fraudes | "Busquen su tarjeta: ¿quedó bloqueada?" |
| | Qué fraudes detectó el sistema | rechazos por motivo | — |
| | Transacciones por botón | qué probaron (un card testing son 4) | — |
| **Kafka: cómo aprende el sistema** | Eventos que pasan por Kafka | evaluadas y liquidadas por segundo | "Cada decisión queda como evento" |
| | Alertas de fraude | lo que procesa el consumidor de alertas | — |
| | Bloqueos hechos por el camino frío | veces que el consumidor agregó una tarjeta a la lista negra | "El sistema aprendió y bloqueó la tarjeta" |
| | Atraso de los consumidores | el lag: en 0, el camino frío va al día | — |
| **El sistema por dentro** | CPU que usa el motor | núcleos: se ve el salto de 1 a 4 con `make demo-cpu` | "Le di más CPU: escalado vertical" |
| | Carga de fondo | lo que manda el loadgen | "Esto es lo que lo satura" |

**Para leerlo bien:**
- **El estado de cada tarjeta es el que observaron sus compras.** "En
  revisión" quiere decir que el motor detectó fraude y el bloqueo viene en
  camino por Kafka; "bloqueada", que una compra ya dio `BLACKLIST`. Si
  alguien detecta fraude y no vuelve a comprar, su tarjeta queda "en
  revisión" aunque el camino frío ya la haya bloqueado: es la consistencia
  eventual, a la vista.
- **"Bloqueos hechos por el camino frío" cuenta bloqueos, no tarjetas:**
  dos rechazos seguidos por card testing son dos bloqueos de la misma
  tarjeta (bloquear de nuevo no cambia nada). El conteo de tarjetas está en
  "Tarjetas bloqueadas".
- **La etiqueta `tarjeta` es una excepción** a la regla de no poner
  identificadores en las etiquetas de Prometheus: vale porque la demo tiene
  un conjunto acotado de tarjetas (una por celular, con un tope de 500).
- **Cada demo empieza en cero** cuando se levanta el gateway. Para "limpiar"
  la tabla entre ensayos, reiniciarlo:
  `docker compose --profile sistema --profile demo restart gateway`.

---

## Qué comparar entre entornos

El objetivo de la evaluación es el **mismo sistema** (la misma imagen) en
binarios nativos, Docker Compose y AWS, con la misma carga. Las métricas
**oficiales** y sus objetivos (SLI y SLO) están definidos en
[ADR-0028](../docs/adr/0028-metricas-de-evaluacion-sli-slo-sla.md); esta tabla
suma las de diagnóstico que explican los resultados. En cada corrida se
registran:

| # | Métrica | De dónde sale | Por qué importa | Qué esperar en AWS |
|---|---|---|---|---|
| 1 | % a tiempo, global y **por franja** (el pico) | CSV del loadgen | el criterio de éxito de tiempo real duro | depende de la capacidad de la instancia |
| 2 | Latencia del cliente P50 / P99 / P99.9 / máx | CSV (no los buckets) | la distribución completa, no un número | sube por la red |
| 3 | **Quiebre**: RPS donde el % a tiempo baja de 99%, y dónde se recupera | CSV en ventanas de 5s (escalera o perfil) | la capacidad real del entorno | otro valor: otros CPU |
| 4 | Goodput en el pico | panel "Carga ofrecida vs goodput" o CSV | cuánto rinde cuando se pasa del límite | — |
| 5 | CPU por transacción (núcleos / RPS) | fila 4 | explica el quiebre: capacidad ≈ núcleos / CPU por tx | cambia con el tipo de CPU |
| 6 | **C_net** P50 / P99 | "Duración de cada fase" | el costo de distribuir el sistema | **lo primero que cambia** entre hosts |
| 7 | D_rem al llegar, P1 | fila 2 | tiempo perdido antes del handler | más saltos, más espera |
| 8 | % degradadas y % EARLY_DROP | CSV o "Decisiones por segundo" | cuánto actúan los mecanismos del modelo temporal | — |
| 9 | % AMBIGUA_COMPROMETIDA, e INCONSISTENTE | auditoría (JSONL) | riesgo de negocio; INCONSISTENTE tiene que ser 0 | — |
| 10 | Retraso de lanzamiento P99 | resumen del loadgen | **validez**: si es alto, la corrida no cuenta | con el loadgen en otro host, debería bajar |
| 11 | Pausa máxima del GC | fila 4 | ¿explica la cola de latencia? | — |
| 12 | Descartes y lag del cold path | fila 5 | el cold path no debe perder nada | 0 en todos |

**Qué no comparar:**
- La latencia de extremo a extremo de Settlement entre hosts (compara
  relojes de máquinas distintas).
- Valores exactos de percentiles leídos de los buckets.
- Una corrida con el retraso de lanzamiento alto: el límite medido es el del
  generador.

### Para que la comparación valga

- **La misma imagen** (mismo digest) en todos los entornos.
- **La misma carga:** semilla (`-semilla`), perfil (`-perfil`), pico
  (`-rps`), compresión, deadline.
- **Varias corridas por entorno**, y la distribución completa.
- **La máquina en igualdad de condiciones:** en una notebook, con el
  cargador conectado y el perfil de energía en `performance` (a batería, la
  latencia medida se duplicó); sin otros programas pesados ni builds; el
  dashboard cerrado o con refresco lento.
- **Anotar** junto a cada resultado: entorno, tipo de máquina o instancia,
  estado de energía, versión (SHA) y los parámetros de la carga.
