# Decisiones de arquitectura (ADRs) — D-RTS

El D-RTS (*Distributed Real-Time Fraud Detection System*) es un motor de
autorización de pagos que decide aprobar o rechazar cada transacción bajo un
**deadline duro de 200ms de extremo a extremo**: una respuesta correcta que
llega tarde cuenta como falla.

Este directorio registra las decisiones de diseño: qué se decidió, por qué,
qué alternativas se descartaron y cómo condiciona el desarrollo. Cómo se
escriben: [ADR-0001](0001-registrar-decisiones-con-adrs.md) y la
[plantilla](0000-plantilla.md).

```
[Load generator] --gRPC, deadline 200ms--> [Fraud-Engine] --gRPC, 15ms--> [Settlement]
                                                 |                              |
                                              [Redis] <-------------------------+
                                     (estado de detección, estadísticas, registros)
```

| ADR | Decisión | Estado | Etapa |
|---|---|---|---|
| [0001](0001-registrar-decisiones-con-adrs.md) | Registrar las decisiones de arquitectura con ADRs | Aceptada | transversal |
| [0002](0002-camino-de-decision-sincronico-con-grpc.md) | Camino de decisión sincrónico, con gRPC y el deadline propagado | Aceptada | 0–1 (setup y control de admisión) |
| [0003](0003-control-de-admision-temprano.md) | Control de admisión temprano (load shedding) | Aceptada | 1, revisada en la 4 |
| [0004](0004-computacion-imprecisa.md) | Computación imprecisa: fase mandatoria siempre, opcional según slack | Aceptada | 4 |
| [0005](0005-fraude-no-es-error.md) | Un fraude no es un error; las fallas llevan motivo (ErrorInfo) | Aceptada | 2, extendida en la 6 |
| [0006](0006-que-se-registra-ante-un-rechazo.md) | Qué se registra en el estado de detección ante un rechazo | Aceptada | 2, revisada en la 4 |
| [0007](0007-validacion-estricta-de-la-request.md) | Validación estricta de la request | Aceptada | 2, 3 y 4 |
| [0008](0008-redis-un-viaje-atomico.md) | Redis como estado del hot path, con un solo viaje atómico | Aceptada | 3 |
| [0009](0009-fail-closed-y-fallo-rapido.md) | Fail-closed y fallo rápido ante fallas de infraestructura | Aceptada | 3 |
| [0010](0010-modelo-de-scoring.md) | Scoring con Z-score sobre ln(monto), ponderado por categoría | Aceptada | 4 |
| [0011](0011-costo-simulado-y-wcet.md) | Costo de la fase opcional simulado; presupuestos por peor caso | Aceptada | 4 |
| [0012](0012-settlement-segundo-servicio-sincronico.md) | Settlement como segundo servicio sincrónico, con timeout propio | Aceptada (política ante fallas: ver 0027) | 5 |
| [0013](0013-ambiguedad-del-timeout-e-idempotencia.md) | Ambigüedad del timeout: idempotencia y "el primer resultado final gana" | Aceptada | 5 |
| [0014](0014-load-generator-de-lazo-abierto.md) | Load generator de lazo abierto | Aceptada | 6 |
| [0015](0015-observabilidad-prometheus-grafana.md) | Observabilidad con Prometheus y Grafana provisionados | Aceptada | 7 |
| [0016](0016-tests-contra-infraestructura-real.md) | Los tests usan infraestructura real | Aceptada | 3 en adelante |
| [0017](0017-configuracion-por-entorno-y-apagado-ordenado.md) | Configuración por entorno y apagado ordenado | Aceptada | 7.5 |
| [0018](0018-evaluar-en-distintos-entornos.md) | Evaluar el sistema real en distintos entornos, no contra un simulador | Aceptada | transversal (afecta a 7.5, la etapa de nube y la calibración) |
| [0019](0019-kafka-como-columna-de-eventos.md) | Kafka como columna de eventos, fuera del camino de decisión | Aceptada (implementada: ADR-0023, ADR-0024, ADR-0025) | 8 (pasa a ser parte del núcleo) |
| [0020](0020-imagenes-salud-y-compose.md) | Imágenes distroless, salud por gRPC y Compose por perfiles | Aceptada | 7.5 |
| [0021](0021-logs-con-niveles.md) | Logs estructurados con niveles; el detalle por transacción en Debug | Aceptada | refactor posterior a la 7.5 |
| [0022](0022-conexion-caida-es-resultado-ambiguo.md) | Una conexión caída con la request en vuelo es un resultado ambiguo | Aceptada | refactor posterior a la 7.5 (complementa a ADR-0013) |
| [0023](0023-redes-como-subredes-privadas.md) | Redes del Compose como subredes privadas; Kafka como unidad aparte | Aceptada | 8 (Kafka) |
| [0024](0024-eventos-en-protobuf-sin-bloquear.md) | Eventos en protobuf, publicados sin bloquear | Aceptada | 8 (Kafka) |
| [0025](0025-consumidores-y-auditoria.md) | Consumidores del cold path; la auditoría cruza los dos lados | Aceptada | 8 (Kafka) |
| [0026](0026-perfil-de-carga-diario.md) | Perfil de carga diario (Poisson no homogéneo) | Aceptada | 6 (extiende el load generator) |
| [0027](0027-reintento-responde-lo-registrado.md) | Un reintento responde lo registrado; sin registro no hay decisión | Aceptada (reemplaza parte de 0012) | corrección posterior a la 8 |
| [0028](0028-metricas-de-evaluacion-sli-slo-sla.md) | Métricas de evaluación y objetivos (SLI, SLO y SLA) | Aceptada | transversal (evaluación por entornos) |
| [0029](0029-demo-interactiva-y-bloqueo-automatico.md) | Demo interactiva con QR y bloqueo automático de tarjetas | Aceptada | demo de la presentación |

## Cómo leerlos

- **Modelo de tiempo real:** 0002 (sincronía y deadline), 0003 (admisión),
  0004 (computación imprecisa), 0011 (peor caso, no promedio).
- **Correctitud ante fallas:** 0005 (fraude vs error), 0009 (fail-closed),
  0012 (Settlement), 0013 (ambigüedad del timeout e idempotencia), 0022 (conexión caída), 0027 (reintentos responden lo registrado).
- **Detección:** 0006 (qué se registra), 0007 (validación), 0008 (Redis),
  0010 (scoring).
- **Medir y operar:** 0014 (load generator), 0026 (perfil de carga diario), 0028 (métricas oficiales y SLOs), 0015 (observabilidad), 0016
  (tests reales), 0017 (configuración y apagado), 0018 (entornos), 0019
  (Kafka), 0020 (imágenes, salud y Compose), 0021 (logs), 0023 (redes), 0024 (eventos), 0025 (consumidores y auditoría).
