#!/bin/bash
# Carga de fondo para la demo con QR sobre AWS (Fase 2): el loadgen corre en
# la máquina borde y sube los RPS en escalera, para que la latencia que ven
# los celulares de la clase vaya creciendo durante la presentación.
#
#   scripts/demo-carga-aws.sh iniciar   # arranca la escalera (en segundo plano, en AWS)
#   scripts/demo-carga-aws.sh estado    # en qué escalón está
#   scripts/demo-carga-aws.sh parar     # corta la carga
#
# Se corre desde la notebook, después del terraform apply de la Fase 2: toma
# la máquina borde del output de Terraform y le habla por SSM (sin SSH).
#
# Perillas (variables de entorno):
#   ESCALONES  RPS de cada escalón       (default "30 60 90 130 200")
#   DURACION   cuánto dura cada escalón  (default 60s)
#
# Referencia (campaña del 2026-11-02, 2 motores c7i.large): ~70 RPS dentro del
# SLO; a 100 el P99 ronda 110ms; desde 130 empieza el quiebre y a 200, con 2
# motores, colapsa (lo que se arregla sumando motor-3, ver computo.tf). Después del
# último escalón la carga se queda en ese valor hasta que se la pare.
#
# Las tarjetas del loadgen son una por transacción: no tocan las tarjetas de
# los celulares, solo comparten la CPU de los motores.
set -euo pipefail

ESCALONES="${ESCALONES:-30 60 90 130 200}"
DURACION="${DURACION:-60s}"

cd "$(dirname "$0")/../infra/entornos/fase2"
BORDE=$(terraform output -json instancias 2>/dev/null | jq -r '.borde // empty' 2>/dev/null || true)
if [ -z "$BORDE" ]; then
  echo "No encuentro la máquina borde: ¿está levantada la Fase 2 (terraform apply)?" >&2
  exit 1
fi

# remoto <script>: lo corre como root en la máquina borde y muestra la salida.
remoto() {
  local id estado
  id=$(aws ssm send-command --instance-ids "$BORDE" --document-name AWS-RunShellScript \
    --comment "drts demo carga" \
    --parameters "{\"commands\":[\"echo $(printf '%s' "$1" | base64 -w0) | base64 -d | bash\"]}" \
    --query Command.CommandId --output text)
  for _ in $(seq 1 30); do
    estado=$(aws ssm get-command-invocation --command-id "$id" --instance-id "$BORDE" \
      --query Status --output text 2>/dev/null || echo Pending)
    case "$estado" in Pending|InProgress|Delayed) sleep 2 ;; *) break ;; esac
  done
  aws ssm get-command-invocation --command-id "$id" --instance-id "$BORDE" \
    --query StandardOutputContent --output text
}

case "${1:-}" in
iniciar)
  # El bucle corre en la máquina borde, desprendido de la sesión de SSM
  # (setsid): sigue aunque se cierre esta terminal. Su PID queda en un archivo
  # para poder pararlo; cada corrida usa un contenedor con nombre fijo.
  remoto "$(cat <<EOF
set -e
if [ -f /run/drts-demo-carga.pid ] && kill -0 "\$(cat /run/drts-demo-carga.pid)" 2>/dev/null; then
  echo "Ya hay carga corriendo (usá: parar)."; exit 0
fi
cd /opt/drts/infra/entornos/fase2/compose
cat > /tmp/drts-demo-carga.sh <<'BUCLE'
cd /opt/drts/infra/entornos/fase2/compose
ultimo=""
for r in $ESCALONES; do
  ultimo=\$r
  echo "\$(date +%T) escalón: \$r RPS durante $DURACION"
  docker compose -f borde.yml run --rm -T --name drts-demo-carga loadgen -rps \$r -duracion $DURACION -gracia 0 2>&1 | grep -E "^===|dentro del deadline" || true
done
while true; do
  echo "\$(date +%T) se mantiene: \$ultimo RPS"
  docker compose -f borde.yml run --rm -T --name drts-demo-carga loadgen -rps \$ultimo -duracion $DURACION -gracia 0 2>&1 | grep -E "^===|dentro del deadline" || true
done
BUCLE
setsid nohup bash /tmp/drts-demo-carga.sh > /var/log/drts-demo-carga.log 2>&1 < /dev/null &
echo \$! > /run/drts-demo-carga.pid
echo "Carga iniciada: escalones [$ESCALONES] RPS, $DURACION cada uno; después se mantiene en el último."
EOF
)"
  ;;
estado)
  remoto "$(cat <<'EOF'
if [ -f /run/drts-demo-carga.pid ] && kill -0 "$(cat /run/drts-demo-carga.pid)" 2>/dev/null; then
  echo "Corriendo. Últimas líneas:"; tail -n 6 /var/log/drts-demo-carga.log
else
  echo "No hay carga corriendo."
fi
EOF
)"
  ;;
parar)
  # Primero el bucle (para que no lance otro escalón), después el contenedor.
  remoto "$(cat <<'EOF'
if [ -f /run/drts-demo-carga.pid ]; then
  pkill -TERM -s "$(cat /run/drts-demo-carga.pid)" 2>/dev/null || true
  rm -f /run/drts-demo-carga.pid
fi
docker rm -f drts-demo-carga > /dev/null 2>&1 || true
echo "Carga parada."
EOF
)"
  ;;
*)
  echo "uso: $0 iniciar | estado | parar" >&2
  exit 2
  ;;
esac
