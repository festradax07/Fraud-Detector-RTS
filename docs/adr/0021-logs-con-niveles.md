# ADR-0021: Logs estructurados con niveles; el detalle por transacción en Debug

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** refactor posterior a la 7.5

## Contexto

Los servicios escribían varias líneas de log por transacción (~6 entre el
motor y Settlement), con `log.Printf` y sin niveles. Medido en la máquina de
desarrollo: una línea cuesta ~0.8µs aun con 16 goroutines compitiendo, así
que **la latencia no es el problema** (a 1000 RPS es ~0.3% de un núcleo). El
problema es el **volumen**: a 1000 RPS son miles de líneas por segundo, ~2GB
por hora en los logs de un contenedor, que Docker por defecto no rota. En la
nube eso llena el disco. Además, el log de aprobación incluía el token de la
tarjeta: aunque sea simulado, en pagos los identificadores de tarjeta no se
loguean.

## Decisión

- **`log/slog` con niveles**, configurado desde el entorno
  ([`internal/registro`](../../internal/registro/registro.go)):
  `LOG_NIVEL` (`debug`, `info`, `warn`, `error`; por defecto `info`) y
  `LOG_FORMATO` (`texto` o `json`, para la nube). Cada línea lleva el
  servicio.
- **Qué va en cada nivel:**
  - `Debug`: el detalle de cada transacción (fraude con su motivo, degradada,
    aprobada, registrada). Apagado por defecto.
  - `Info`: arranque, configuración, apagado y reintentos (normales con
    idempotencia).
  - `Warn`: lo que alguien tiene que mirar: registro perdido, commit sin
    confirmar, sin verificar contra Redis, transacción abortada.
- **En el camino de decisión, los `Debug` van detrás de una guarda**
  (`registro.Depurando(ctx)`). Medido: `slog.Debug` con el nivel apagado igual
  arma sus argumentos y cuesta 26ns y 2 reservas de memoria por llamada; con
  la guarda, 3ns y ninguna.
- **No se loguean identificadores de tarjeta**, solo el `tx_id`.
- **Rotación en Compose:** como máximo 3 archivos de 10MB por servicio.
- Las métricas ([ADR-0015](0015-observabilidad-prometheus-grafana.md))
  cuentan cada resultado: el log no es la forma de contar, es la forma de
  investigar un caso.

## Alternativas consideradas

- **Dejar `log.Printf`:** sin niveles no hay forma de bajar el volumen sin
  perder los eventos importantes.
- **Una librería de logging de terceros (zap, zerolog):** más rápidas, pero
  la diferencia no importa a este volumen y `slog` está en la librería
  estándar.
- **`slog.LogAttrs` en vez de la guarda:** no reserva memoria, pero cuesta
  26ns aun apagado y es más verboso.

## Consecuencias

- Para ver cada transacción hay que levantar el servicio con
  `LOG_NIVEL=debug`, y aceptar el volumen.
- Bajo una falla masiva (por ejemplo, Settlement caído), los `Warn` siguen
  siendo uno por transacción afectada; si hiciera falta, se agrega muestreo.
