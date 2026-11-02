# Fase 2: el sistema distribuido

El mismo sistema, ahora **partido en máquinas**: cada rol en su EC2, los
motores detrás de un balanceador interno y los servicios encontrándose por
DNS privado. **Efímero:** se crea para la sesión y se destruye al terminar.
Si no llega a andar, la [Fase 1](../fase1/README.md) (una sola máquina) es el
plan B.

```
                 Internet (celulares, puerto 80)
                     │
 ┌── VPC 10.30.0.0/16 ┼──────────────────────────────────────────────────┐
 │                   ▼                                                    │
 │  borde (c7i.large)        gateway + loadgen                           │
 │     │ gRPC + TLS (autoridad propia) → motor.drts.internal:443         │
 │     ▼                                                                  │
 │  ALB interno (2 zonas)    reparte CADA request (ADR-0030)              │
 │     │ gRPC (h2c) :50051, health check grpc.health.v1                   │
 │     ├──► motor-1 (c7i.large)                                           │
 │     └──► motor-2 (c7i.large)                                           │
 │              │ Redis :6379, Settlement :50052                          │
 │              ▼                                                         │
 │  datos (c7i.large)        Redis + Settlement + cargar-stats            │
 │                                                                        │
 │  frio (t3.large)          Kafka :9092, consumidores, Prometheus,       │
 │                           Grafana (fuera del camino de decisión)       │
 └────────────────────────────────────────────────────────────────────────┘
 zona privada drts.internal: motor (→ ALB), motor-1, motor-2, redis,
 settlement, kafka, borde, datos, frio
```

| Archivo | Qué tiene |
|---|---|
| `versiones.tf` | Terraform, los providers (AWS y `tls`) y las etiquetas (`entorno=fase2`) |
| `variables.tf` | las entradas: la etiqueta de las imágenes, tipos de instancia, token, horas máximas |
| `red.tf` | la VPC, una subred por rol (más una segunda zona para el ALB) y la ruta a internet |
| `seguridad.tf` | un security group por rol con la matriz de flujos (ADR-0023) y el rol de IAM |
| `certificados.tf` | la autoridad propia y el certificado de `motor.drts.internal`, importado en ACM |
| `balanceo.tf` | el ALB interno, el target group gRPC y el listener HTTPS |
| `dns.tf` | la zona privada `drts.internal` y sus nombres |
| `datos.tf` | el bucket efímero con el dataset |
| `computo.tf` | las 5 EC2 |
| `arranque.sh.tftpl` | el script de arranque, el mismo para todas: cambia qué rol levanta |
| `compose/` | un compose por rol (`borde`, `motor`, `datos`, `frio`), Prometheus, y `local.yml` para probarlos juntos en la notebook |
| `outputs.tf` | la URL del presentador, los comandos de SSM, el estado de los motores |

## Las imágenes: de GitHub, no se construyen acá

Las publica GitHub Actions en GHCR en cada push
([`.github/workflows/imagenes.yml`](../../../.github/workflows/imagenes.yml)).
Las máquinas solo las bajan. Se despliega una **etiqueta fija**,
`sha-<commit>`: es el mismo artefacto que corre en la notebook (ADR-0018).
Antes de un apply, el build de ese commit tiene que haber terminado bien
(pestaña *Actions*), y `git_ref` tiene que apuntar a una rama que contenga
ese commit (las máquinas clonan el repo para los compose y la configuración).

## Una vez: requisitos en la notebook

Los mismos que la Fase 1: el dataset en `data/`, el plugin de SSM y
`terraform.tfvars` (copiando `terraform.tfvars.example`).

## Probar los roles en la notebook (sin gastar)

```sh
docker compose --profile sistema --profile carga --profile demo build   # imágenes locales drts/*:dev
cd infra/entornos/fase2/compose
export DRTS_TAG=dev DRTS_IMAGENES=drts DRTS_MOTOR=motor-1.drts.internal:50051 \
       DRTS_MOTOR_CA= DEMO_TOKEN=demo-local DEMO_PUERTO=8080 DRTS_CERTS=/tmp/sin-certs
docker compose -p drts-fase2 -f datos.yml -f motor.yml -f frio.yml -f borde.yml -f local.yml up -d --wait
# ... probar ...
docker compose -p drts-fase2 -f datos.yml -f motor.yml -f frio.yml -f borde.yml -f local.yml down -v
```

## Una sesión

```sh
cd infra/entornos/fase2
terraform init           # la primera vez
terraform plan           # revisar: ~69 recursos
terraform apply
```

Cada máquina tarda **~5 minutos** en estar lista (instala Docker, baja las
imágenes, levanta su rol). Para seguir una:

```sh
eval "$(terraform output -json ver_arranque | jq -r .borde)"   # o motor-1, datos, frio...
eval "$(terraform output -raw salud_motores)"                  # los dos "healthy" en el ALB
```

Cuando las cinco dicen **LISTO** y los motores están sanos:

```sh
terraform output -raw url_presentador          # abrirla en el proyector: el QR
eval "$(terraform output -raw tunel_grafana)"  # Grafana en http://localhost:3000
```

Una corrida de carga, desde el borde:

```sh
eval "$(terraform output -json entrar | jq -r .borde)"
sudo -i
cd /opt/drts/infra/entornos/fase2/compose
docker compose -f borde.yml run --rm loadgen -rps 200 -duracion 60s
```

## Carga de fondo para la demo

Mientras la clase compra desde los celulares, el loadgen sube la carga en
escalera para que la latencia se vea crecer. Desde la raíz del repo, en la
notebook:

```sh
scripts/demo-carga-aws.sh iniciar   # 30 → 60 → 90 → 110 → 130 RPS, 2 min cada uno
scripts/demo-carga-aws.sh estado    # en qué escalón está
scripts/demo-carga-aws.sh parar     # corta la carga
```

Corre en la máquina borde, desprendida de la sesión: sigue aunque se cierre
la terminal, hasta el `parar`. Se ajusta con `ESCALONES="20 50 80"` y
`DURACION=90s`. Referencia medida: con 2 motores, ~70 RPS dentro del SLO;
a 100 el P99 ronda 110ms; desde 130 empieza el quiebre.

## Al terminar

```sh
terraform destroy        # borra TODO: máquinas, ALB, DNS, red, bucket
```

**Red de seguridad:** cada máquina se apaga y se borra sola a las
`horas_maximas` (4 por defecto). **El ALB no**: cobra por hora hasta el
`destroy`. No te olvides.

## Qué cuesta (por hora, mientras existe)

| Qué | Cuánto |
|---|---|
| 4 × c7i.large (borde, 2 motores, datos) | ~$0,36 |
| 1 × t3.large (frío) | ~$0,08 |
| ALB | ~$0,02–0,03 más el tráfico |
| 5 IPs públicas | ~$0,025 |
| Zona privada de Route 53 | $0,50 al mes, gratis si se borra antes de las 12 h |

Una sesión de 3–4 horas: **menos de $2**. Sin NAT Gateway (las subredes son
públicas; ver `red.tf`) y sin dominio (autoridad propia, ADR-0030).

## Seguridad

- **Sin SSH**: se entra por SSM.
- **Desde internet, solo el puerto de la demo** del borde. Todo lo demás
  sigue la matriz de `seguridad.tf`: por ejemplo, a Redis solo llegan el
  motor y la máquina frío.
- **TLS verificado** entre los clientes y el ALB, con una autoridad que se
  genera en cada apply. Las claves privadas viven en el state local (que no
  se versiona).
- **Red host:** los contenedores usan la red de su máquina, así que llegan
  a las credenciales del rol de IAM (el hop limit de IMDSv2 no los frena).
  El rol solo permite SSM y leer el dataset de esta sesión.
- **Las subredes son públicas** por costo; en producción, aplicación y
  datos irían en subredes privadas con NAT o endpoints de VPC.
