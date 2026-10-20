# syntax=docker/dockerfile:1
#
# Un Dockerfile, varias imágenes (una por servicio), elegidas con --target:
#
#   docker build --target fraud-engine -t drts/fraud-engine .
#
# Etapa 1 (compilar): la toolchain de Go compila TODOS los binarios juntos.
# Etapa 2 (una por servicio): una imagen mínima con solo su binario.
#
# Las mismas imágenes van a todos los entornos (Compose, nube): si el
# artefacto cambia entre entornos, la comparación deja de valer (ADR-0018).

# Misma versión de Go que la de los binarios nativos: si nativo y contenedor
# salieran de compiladores distintos, una diferencia de latencia podría ser
# del compilador y no del entorno.
FROM golang:1.27.1-bookworm AS compilar
WORKDIR /src

# Primero solo go.mod/go.sum: Docker cachea esta capa y no vuelve a bajar
# dependencias mientras no cambien (aunque cambie el código).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
# CGO_ENABLED=0: binario estático, sin depender de la libc del sistema; por
# eso entra en una imagen sin sistema operativo de verdad.
# -trimpath: sin rutas de esta máquina adentro del binario (reproducible).
# -ldflags "-s -w": sin tablas de símbolos ni de depuración (más chico).
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/ \
      ./cmd/server ./cmd/settlement ./cmd/loadgen ./cmd/cargar-stats ./cmd/salud

# distroless/static: ~6MB, sin shell, sin gestor de paquetes, usuario no-root
# (uid 65532). Trae certificados CA y zona horaria; nada más. Menos
# superficie de ataque, y un binario estático de Go no necesita nada más.
FROM gcr.io/distroless/static-debian12:nonroot AS base
WORKDIR /app
# salud: chequeo de salud por gRPC (no hay shell ni curl para hacerlo).
COPY --from=compilar /out/salud /salud

FROM base AS fraud-engine
COPY --from=compilar /out/server /fraud-engine
EXPOSE 50051 2112
ENTRYPOINT ["/fraud-engine"]

FROM base AS settlement
COPY --from=compilar /out/settlement /settlement
EXPOSE 50052 2113
ENTRYPOINT ["/settlement"]

FROM base AS loadgen
COPY --from=compilar /out/loadgen /loadgen
EXPOSE 2114
ENTRYPOINT ["/loadgen"]

FROM base AS cargar-stats
COPY --from=compilar /out/cargar-stats /cargar-stats
ENTRYPOINT ["/cargar-stats"]
