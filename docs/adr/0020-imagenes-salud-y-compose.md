# ADR-0020: Imágenes distroless, salud por gRPC y Compose por perfiles

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** 7.5

## Contexto

El mismo sistema tiene que correr como contenedores en Docker Compose y
después en la nube ([ADR-0018](0018-evaluar-en-distintos-entornos.md)). Hace
falta empaquetarlo, que el orquestador sepa cuándo un servicio está listo, y
poder levantar solo la parte que se necesita en cada momento.

## Decisión

- **Un solo [`Dockerfile`](../../Dockerfile) con varias salidas** (`--target
  fraud-engine | settlement | loadgen | cargar-stats`): una etapa compila
  todos los binarios (estáticos, `CGO_ENABLED=0`, `-trimpath`) con **la
  misma versión de Go que los binarios nativos** (si no, una diferencia de
  latencia podría ser del compilador y no del entorno); cada imagen final es
  **distroless static** (sin shell, sin gestor de paquetes, usuario no-root).
- **El dataset no va en la imagen:** se monta como volumen. El
  [`.dockerignore`](../../.dockerignore) lo excluye del contexto de build
  (si no, cada build mandaría ~500MB).
- **Salud con el protocolo estándar de gRPC** (`grpc.health.v1`). Como la
  imagen no tiene shell ni `curl`, el chequeo lo hace un binario propio,
  [`cmd/salud`](../../cmd/salud/main.go). El mismo protocolo lo usan Compose y
  ECS (vía ese binario) y Kubernetes/k3s de forma nativa. "Sano" significa
  "listo": se marca SERVING después de verificar Redis.
- **Al apagar:** primero NOT_SERVING, después una **ventana de aviso**
  configurable (`APAGADO_AVISO`, 0 en local) en la que se sigue atendiendo,
  y recién después se deja de aceptar requests
  ([ADR-0017](0017-configuracion-por-entorno-y-apagado-ordenado.md)).
- **Compose por perfiles** ([`docker-compose.yml`](../../docker-compose.yml)):
  sin perfil, solo observabilidad; `sistema` agrega Redis, la carga de
  estadísticas (un contenedor que corre una vez y termina), Settlement y el
  motor, con el orden de arranque dado por los chequeos de salud; `carga`
  agrega el load generator.
- **Redis del Compose sin snapshots a disco:** un snapshot hace `fork()` y
  puede frenar a Redis en medio de una medición; los datos se recargan solos.
- **Sin límites de CPU por ahora:** para comparar contra nativo en igualdad
  de condiciones; los límites y su *throttling* se estudian en la
  calibración.
- **Prometheus scrapea las dos formas de correr** (etiqueta `entorno`:
  `nativo` o `compose`) con el mismo dashboard.

## Alternativas consideradas

- **`scratch` como base:** todavía más chica, pero hay que copiar a mano los
  certificados y el usuario.
- **Imágenes con shell (alpine/debian):** chequeo de salud con `curl`, pero
  más superficie de ataque y otra cosa que mantener.
- **`grpc_health_probe` de terceros:** habría que bajar un binario en el
  build; uno propio son ~40 líneas.
- **Un Compose por grupo:** se duplica la red y la configuración común.

## Consecuencias

- Imágenes de 32 a 49MB. Sin shell, depurar adentro del contenedor requiere
  otras herramientas (`docker compose logs`, métricas, o un contenedor de
  depuración aparte).
- La imagen corre como uid 65532: para escribir resultados en una carpeta
  del host, el load generator corre con el uid del usuario.
- Sin `APAGADO_AVISO`, el NOT_SERVING no lo alcanza a ver nadie: el drenaje
  cierra las conexiones existentes y un chequeo ve "conexión rechazada". En
  la nube, detrás de un balanceador, hay que darle unos segundos.
- Particularidades de Compose encontradas: correr la carga con solo
  `--profile carga` falla (su dependencia está en `sistema`), y un contenedor
  de `docker compose run` no recibe el nombre del servicio en la red sin
  `--use-aliases` (Prometheus no lo encuentra). Ambas quedan resueltas en el
  `Makefile` (`make carga-compose`).
