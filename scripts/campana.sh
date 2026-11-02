#!/bin/bash
# Campaña de medición (ADR-0018, ADR-0028): la MISMA escalera de carga, con
# las mismas semillas, en cualquier entorno. Lo único que cambia entre
# entornos es cómo se invoca al loadgen.
#
#   scripts/campana.sh <entorno> <raiz> <dir> <comando del loadgen...>
#
#   entorno  nombre que va en los archivos (local, aws-fase2...)
#   raiz     carpeta del repo en ESTA máquina (donde está resultados/)
#   dir      carpeta de salida, relativa a la raíz (resultados/campana-...)
#
# Ejemplo (local, Docker Compose):
#   scripts/campana.sh local . resultados/campana-20261102 \
#     docker compose --profile sistema --profile carga run --rm --use-aliases loadgen
#
# Deja por corrida: <entorno>-rps<R>-s<S>.csv (una fila por transacción) y
# .txt (el resumen). Las repeticiones van intercaladas (semilla 1 de toda la
# escalera, después la 2, la 3): una perturbación pasajera de la máquina no
# cae entera sobre un mismo punto.
set -u
ENTORNO=$1 RAIZ=$2 DIR=$3
shift 3

ESCALONES="${ESCALONES:-50 70 100 130 160 200 300 400 500}"
SEMILLAS="${SEMILLAS:-1 2 3}"
DURACION="${DURACION:-60s}"
PAUSA="${PAUSA:-10}"

mkdir -p "$RAIZ/$DIR"
chown 1000:1000 "$RAIZ/$DIR" 2>/dev/null || true

corrida() { # nombre, argumentos del loadgen...
  local base=$1; shift
  # -salida es relativa a /app dentro del contenedor (= la raíz del repo).
  "$@" -salida "$DIR/$base.csv" > "$RAIZ/$DIR/$base.txt" 2>&1
  echo "$(date +%T) $base: $(grep -h 'dentro del deadline' "$RAIZ/$DIR/$base.txt")"
  sleep "$PAUSA"
}

echo "$(date +%T) campaña $ENTORNO: escalones [$ESCALONES], semillas [$SEMILLAS], $DURACION cada uno"
for s in $SEMILLAS; do
  for r in $ESCALONES; do
    corrida "$ENTORNO-rps$r-s$s" "$@" -rps "$r" -duracion "$DURACION" -semilla "$s" -gracia 0
  done
done

# El día hábil comprimido (1 h del perfil = 30 s): pico de 150 RPS a las 19 h.
corrida "$ENTORNO-perfil-dia-habil" "$@" -perfil perfiles/dia-habil.csv -rps 150 -compresion 120 -semilla 1 -gracia 0

echo "$(date +%T) campaña $ENTORNO: FIN"
