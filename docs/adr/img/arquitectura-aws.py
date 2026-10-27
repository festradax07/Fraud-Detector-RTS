"""Genera el esquema de la arquitectura propuesta en AWS (ADR-0029).

Diagrama como código: este script arma un SVG con los íconos oficiales de
AWS (los que trae la librería `diagrams`) y una disposición fija, para que
los grupos (VPC, subredes, instancias) queden donde tienen que estar. Si
cambia la arquitectura, se edita acá y se regenera.

    python3 -m venv /tmp/venv-diag && /tmp/venv-diag/bin/pip install diagrams
    /tmp/venv-diag/bin/python docs/adr/img/arquitectura-aws.py

Escribe arquitectura-aws.svg al lado de este archivo. Para el PNG:

    firefox --headless --no-remote --profile "$(mktemp -d)" \\
      --window-size=1900,1220 --screenshot docs/adr/img/arquitectura-aws.png \\
      "file://$PWD/docs/adr/img/arquitectura-aws.svg"

Solo usa `diagrams` por sus íconos: no necesita Graphviz.
"""

import base64
import os
from xml.sax.saxutils import escape

import diagrams

ICONOS = os.path.realpath(os.path.join(os.path.dirname(diagrams.__file__), "..", "resources"))
SALIDA = os.path.join(os.path.dirname(os.path.abspath(__file__)), "arquitectura-aws.svg")
ANCHO, ALTO = 1900, 1220

# Colores de cada camino (también en la leyenda).
CALIENTE = "#d6336c"  # dentro de los 200ms
DEMO = "#7048e8"      # cliente real por internet (modo demo)
FRIO = "#1c7ed6"      # cold path: sin deadline
OBS = "#868e96"       # observabilidad y gestión

partes = []


def icono(ruta, x, y, tam=56, titulo="", sub=""):
    """Ícono centrado en (x, y), con título y subtítulo debajo."""
    with open(os.path.join(ICONOS, ruta), "rb") as f:
        datos = base64.b64encode(f.read()).decode()
    partes.append(f'<image x="{x - tam / 2}" y="{y - tam / 2}" width="{tam}" height="{tam}" '
                  f'href="data:image/png;base64,{datos}"/>')
    yt = y + tam / 2 + 16
    if titulo:
        partes.append(f'<text x="{x}" y="{yt}" class="tit">{escape(titulo)}</text>')
    for i, linea in enumerate(sub.split("\n") if sub else []):
        partes.append(f'<text x="{x}" y="{yt + 15 + 13 * i}" class="sub">{escape(linea)}</text>')


def caja(x, y, w, h, etiqueta, borde, fondo="none", guiones="", radio=10, clase="caja"):
    dash = f' stroke-dasharray="{guiones}"' if guiones else ""
    partes.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{radio}" fill="{fondo}" '
                  f'stroke="{borde}" stroke-width="1.6"{dash}/>')
    if etiqueta:
        partes.append(f'<text x="{x + 12}" y="{y + 20}" class="{clase}" fill="{borde}">{escape(etiqueta)}</text>')


def flecha(puntos, color, ancho=2.6, guiones="", etiqueta="", pos=None, ancla="middle"):
    """Polilínea con punta de flecha al final. pos = (x, y) de la etiqueta."""
    pts = " ".join(f"{x},{y}" for x, y in puntos)
    dash = f' stroke-dasharray="{guiones}"' if guiones else ""
    marca = color.lstrip("#")
    partes.append(f'<polyline points="{pts}" fill="none" stroke="{color}" stroke-width="{ancho}"{dash} '
                  f'stroke-linejoin="round" marker-end="url(#p{marca})"/>')
    if etiqueta and pos:
        partes.append(f'<text x="{pos[0]}" y="{pos[1]}" class="flecha" fill="{color}" '
                      f'text-anchor="{ancla}">{escape(etiqueta)}</text>')


def nota(x, y, lineas, color, ancla="start", clase="nota"):
    for i, linea in enumerate(lineas):
        partes.append(f'<text x="{x}" y="{y + 15 * i}" class="{clase}" fill="{color}" '
                      f'text-anchor="{ancla}">{escape(linea)}</text>')


def qr(x, y, lado=34):
    """Un QR esquemático (decorativo)."""
    c = lado / 7
    patron = ["1111101", "1000101", "1011101", "1011100", "1000111", "0110101", "1101011"]
    partes.append(f'<rect x="{x - 3}" y="{y - 3}" width="{lado + 6}" height="{lado + 6}" fill="#fff" stroke="{DEMO}"/>')
    for i, fila in enumerate(patron):
        for j, v in enumerate(fila):
            if v == "1":
                partes.append(f'<rect x="{x + j * c}" y="{y + i * c}" width="{c}" height="{c}" fill="#212529"/>')


# --- Título y leyenda -------------------------------------------------------
nota(30, 42, ["D-RTS en AWS: arquitectura propuesta"], "#212529", clase="titulo")
nota(30, 66, ["Mismas imágenes que en Docker Compose · ECS sobre EC2 · una zona · efímero (Terraform crea, se mide, se destruye)"],
     "#495057")
for i, (color, guion, texto) in enumerate([
        (CALIENTE, "", "camino caliente: dentro de los 200ms"),
        (DEMO, "", "camino de la demo: cliente real por internet (QR)"),
        (FRIO, "8 5", "camino frío: eventos, sin deadline"),
        (OBS, "2 4", "observabilidad y gestión")]):
    x0 = 1150 + (i % 2) * 370
    y0 = 30 + (i // 2) * 26
    partes.append(f'<line x1="{x0}" y1="{y0}" x2="{x0 + 40}" y2="{y0}" stroke="{color}" stroke-width="3"'
                  + (f' stroke-dasharray="{guion}"' if guion else "") + "/>")
    nota(x0 + 50, y0 + 5, [texto], "#343a40")

# --- Fuera de AWS: clientes de la demo y CI/CD --------------------------------
icono("aws/general/mobile-client.png", 95, 200, 60, "Compañeros", "escanean el QR\ny compran desde el celular")
qr(150, 165)
icono("aws/general/internet-alt1.png", 95, 420, 60, "Internet", "50–300ms: latencia\ndel usuario, aparte")

caja(20, 690, 250, 460, "Fuera de AWS: CI/CD", OBS, "#f8f9fa", "4 4")
icono("onprem/ci/github-actions.png", 80, 770, 48, "GitHub Actions", "orquestador")
icono("onprem/vcs/github.png", 205, 770, 48, "GHCR", "imágenes públicas")
icono("aws/general/client.png", 80, 925, 48, "Notebook", "runner self-hosted\n(dev: Compose)")
icono("onprem/iac/terraform.png", 205, 925, 48, "Terraform", "crea y destruye\nel entorno")
nota(145, 1065, ["build una vez (en dev),", "promoción por digest a AWS,", "credenciales por OIDC (1h)"], OBS, ancla="middle")

# --- AWS --------------------------------------------------------------------
caja(300, 100, 1580, 1100, "AWS · us-east-1", "#232f3e", "#ffffff", radio=14, clase="aws")
icono("aws/security/identity-and-access-management-iam-role.png", 1380, 160, 40, "Rol OIDC", "sin claves guardadas")
icono("aws/storage/simple-storage-service-s3.png", 1500, 160, 40, "S3", "state de Terraform")
icono("aws/cost/budgets.png", 1620, 160, 40, "Budgets", "alarma de gasto")
icono("aws/management/systems-manager.png", 1745, 160, 40, "SSM", "acceso sin SSH")

caja(320, 240, 1540, 940, "VPC · una zona de disponibilidad (us-east-1a)", "#2f9e44", "#f4fbf5", radio=12)
icono("aws/network/internet-gateway.png", 320, 420, 46, "", "")
nota(320, 462, ["Internet", "Gateway"], "#2f9e44", ancla="middle")

# Subredes (equivalen a las redes del Compose, ADR-0023)
caja(370, 280, 290, 880, "Subred borde", "#5c940d", "#fbfdf6", "6 4")
caja(680, 280, 440, 880, "Subred aplicación", "#5c940d", "#fbfdf6", "6 4")
caja(1140, 280, 350, 880, "Subred datos", "#5c940d", "#fbfdf6", "6 4")
caja(1510, 280, 330, 880, "Subred observabilidad", "#5c940d", "#fbfdf6", "6 4")

# Borde: ALB público (demo) y host de clientes (loadgen + gateway)
icono("aws/network/elb-application-load-balancer.png", 515, 400, 56, "ALB público", "HTTPS · solo en modo demo\nlímite por IP + token del QR")
caja(395, 540, 240, 330, "EC2 clientes · c7i.large spot", "#e8590c", "#fff8f3")
icono("aws/compute/elastic-container-service-container.png", 515, 620, 50, "loadgen", "Poisson / perfil del día\nla carga que se MIDE")
icono("aws/compute/elastic-container-service-container.png", 515, 770, 50, "gateway demo", "HTTP/JSON → gRPC\npone el deadline de 200ms")

# Aplicación: ALB interno y las réplicas del motor
icono("aws/network/elb-application-load-balancer.png", 760, 690, 56, "ALB interno", "gRPC · balancea\npor request")
caja(860, 310, 240, 820, "ECS · motores (auto scaling)", "#e8590c", "#fff8f3", "5 3")
nota(980, 1115, ["las 3 réplicas hablan con Settlement y Redis"], "#e8590c", ancla="middle", clase="sub")
for i, y in enumerate([470, 700, 930]):
    caja(880, y - 115, 200, 210, f"EC2 motor {i + 1} · c7i.xlarge spot", "#e8590c", "#ffffff")
    icono("aws/compute/elastic-container-service-container.png", 980, y - 10, 50, "fraud-engine",
          "admisión · mandatoria\nslack · scoring")

# Datos: Settlement + Redis, y Kafka aparte
caja(1160, 310, 310, 430, "EC2 datos · c7i.large (on-demand)", "#e8590c", "#fff8f3")
icono("aws/compute/elastic-container-service-container.png", 1240, 420, 50, "settlement", "registro idempotente\naborta si pasó 200ms")
icono("onprem/inmemory/redis.png", 1390, 620, 52, "Redis", "blacklist · ventana ·\nposición · registros")
caja(1160, 800, 310, 330, "EC2 Kafka · t3.medium", "#1c7ed6", "#f3f8ff")
icono("onprem/queue/kafka.png", 1315, 925, 60, "Kafka (KRaft)", "1 broker, sin réplica\n(declarado: no es HA)")

# Observabilidad: consumidores + Prometheus + Grafana
caja(1530, 310, 290, 820, "EC2 frío · t3.large", "#1c7ed6", "#f3f8ff")
icono("aws/compute/elastic-container-service-container.png", 1600, 420, 44, "auditoría", "cruza motor\ncon Settlement")
icono("aws/compute/elastic-container-service-container.png", 1750, 420, 44, "alertas", "un aviso\npor fraude")
icono("aws/compute/elastic-container-service-container.png", 1675, 600, 44, "estadísticas", "en sombra (Welford)")
icono("onprem/monitoring/prometheus.png", 1600, 830, 52, "Prometheus", "scrape cada 1s")
icono("onprem/monitoring/grafana.png", 1750, 830, 52, "Grafana", "por túnel SSM\n(no público)")
nota(1675, 1010, ["créditos de CPU: no afectan", "al deadline (camino frío)"], "#1c7ed6", ancla="middle")

# ECS como plano de control de todo
icono("aws/compute/elastic-container-service.png", 1100, 160, 46, "Cluster ECS", "todas las instancias\nson su capacidad")

# --- Flechas: camino de la demo ------------------------------------------------
flecha([(95, 285), (95, 382)], DEMO)
flecha([(128, 420), (295, 420)], DEMO, etiqueta="HTTPS", pos=(210, 410))
flecha([(345, 410), (480, 400)], DEMO)
flecha([(545, 400), (620, 400), (620, 715), (515, 715), (515, 742)], DEMO, etiqueta="token del QR", pos=(612, 520), ancla="end")
flecha([(545, 765), (650, 765), (650, 705), (728, 700)], DEMO)

# --- Flechas: camino caliente --------------------------------------------------
flecha([(545, 615), (650, 615), (650, 680), (728, 683)], CALIENTE, 3.2, etiqueta="gRPC · 200ms", pos=(560, 605), ancla="start")
for y in [460, 690, 920]:
    flecha([(792, 690), (830, 690), (830, y), (950, y)], CALIENTE, 3.2)
flecha([(1080, 470), (1130, 470), (1130, 420), (1210, 420)], CALIENTE, 3.2, etiqueta="15ms", pos=(1170, 405))
flecha([(1300, 495), (1368, 596)], CALIENTE, 3.2, etiqueta="Lua", pos=(1352, 530))
flecha([(1080, 520), (1120, 520), (1120, 625), (1358, 625)], CALIENTE, 2.4,
       etiqueta="viaje 1 y 2", pos=(1252, 615), ancla="start")

# --- Flechas: camino frío ------------------------------------------------------
flecha([(1080, 940), (1280, 940)], FRIO, 2.4, "8 5", etiqueta="evaluada", pos=(1180, 930))
flecha([(1240, 505), (1240, 770), (1335, 770), (1335, 892)], FRIO, 2.4, "8 5", etiqueta="liquidada", pos=(1232, 720), ancla="end")
flecha([(1350, 900), (1500, 900), (1500, 420), (1572, 420)], FRIO, 2.4, "8 5")
flecha([(1500, 600), (1648, 600)], FRIO, 2.4, "8 5")
flecha([(1500, 420), (1500, 360), (1750, 360), (1750, 393)], FRIO, 2.4, "8 5")
flecha([(1650, 625), (1650, 690), (1480, 690), (1480, 620), (1420, 620)], FRIO, 2.0, "8 5", etiqueta="stats_vivo", pos=(1570, 682))

# --- Flechas: observabilidad y gestión -----------------------------------------
flecha([(1572, 830), (1500, 830), (1500, 1060), (1100, 1060)], OBS, 2.0, "2 4",
       etiqueta="scrape /metrics de todos los servicios", pos=(1300, 1052))
flecha([(1722, 830), (1630, 830)], OBS, 2.0, "2 4")
flecha([(205, 745), (205, 690), (292, 690), (292, 160), (1074, 160)], OBS, 2.0, "2 4", etiqueta="pull de imágenes (GHCR → ECS)", pos=(680, 152))
flecha([(232, 925), (280, 925), (280, 128), (1380, 128), (1380, 138)], OBS, 2.0, "2 4",
       etiqueta="Terraform / deploy vía OIDC", pos=(820, 121))

# --- Anotación del tiempo real -------------------------------------------------
caja(690, 975, 160, 120, "", CALIENTE, "#fff0f6", radio=8)
nota(770, 998, ["⏱ Borde del", "sistema: los 200ms", "se cuentan desde", "acá (loadgen o", "gateway)"], CALIENTE,
     ancla="middle", clase="notab")

svg = f"""<svg xmlns="http://www.w3.org/2000/svg" width="{ANCHO}" height="{ALTO}" viewBox="0 0 {ANCHO} {ALTO}">
<style>
  text {{ font-family: "Ubuntu", "Helvetica", "Arial", sans-serif; }}
  .titulo {{ font-size: 26px; font-weight: 700; }}
  .tit {{ font-size: 13px; font-weight: 700; text-anchor: middle; fill: #212529; }}
  .sub {{ font-size: 11px; text-anchor: middle; fill: #495057; }}
  .caja {{ font-size: 13px; font-weight: 700; }}
  .aws {{ font-size: 16px; font-weight: 700; }}
  .flecha {{ font-size: 12px; font-weight: 700; }}
  .nota {{ font-size: 13px; }}
  .notab {{ font-size: 12px; font-weight: 700; }}
</style>
<defs>
{"".join(f'<marker id="p{c.lstrip("#")}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" fill="{c}"/></marker>' for c in (CALIENTE, DEMO, FRIO, OBS))}
</defs>
<rect width="100%" height="100%" fill="#ffffff"/>
{chr(10).join(partes)}
</svg>
"""
with open(SALIDA, "w") as f:
    f.write(svg)
print("escrito", SALIDA)
