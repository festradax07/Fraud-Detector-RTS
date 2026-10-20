# ADR-0015: Observabilidad con Prometheus y Grafana provisionados

- **Estado:** Aceptada
- **Fecha:** 2026-10-04
- **Etapa:** 7

## Contexto

El colapso por congestión solo se veía en el resumen al final de una
corrida. Hacía falta verlo en vivo, y medir lo que el servidor no puede ver
solo: el servidor no se entera de un deadline miss (para él la request "salió
bien").

## Decisión

- **Dos niveles de métricas** ([`internal/metricas`](../../internal/metricas/metricas.go)):
  un **interceptor gRPC propio** (tasa, errores por motivo, latencia, en
  vuelo, *respuestas tardías* = terminó con el ctx vencido) y métricas del
  dominio en cinco puntos del handler (`D_rem` al llegar, slack, duración de
  cada fase). Settlement y el load generator exponen las suyas; el load
  generator es el único que ve los deadline miss del lado del cliente.
- **Límites de los histogramas en los umbrales del diseño** (35, 95 y 200ms):
  los percentiles se interpolan dentro de cada tramo.
- **Registro propio por servidor** (el global hace *panic* al registrar dos
  veces), etiquetas con valores acotados (nunca `tx_id`).
- **Infraestructura:** `docker-compose.yml` con Prometheus y Grafana en
  versiones fijas, scrape cada 1s, puertos atados a `127.0.0.1` (Grafana entra
  sin login), datasource y dashboard **provisionados** desde archivos: se
  levanta sin configurar nada.

## Alternativas consideradas

- **`go-grpc-middleware`:** esconde justo lo que se quería entender; el
  interceptor propio son ~40 líneas.
- **Scrape cada 15s (por defecto):** dos puntos por corrida de 30s.
- **Dashboard editado en la UI:** se pierde en la base de datos de Grafana;
  en JSON queda versionado.

## Consecuencias

- Costo medido del interceptor: 120ns por request, sin reservas de memoria.
- Prometheus 3 normaliza las etiquetas `le`/`quantile` (`"1"` → `"1.0"`): las
  consultas tienen que usar la forma normalizada.
