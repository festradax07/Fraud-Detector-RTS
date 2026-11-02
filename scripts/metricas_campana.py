#!/usr/bin/env python3
"""Saca de Prometheus las métricas del lado del SERVIDOR para cada corrida
de una campaña (scripts/campana.sh): CPU y memoria por servicio, requests en
vuelo, tiempos por fase y reparto entre réplicas del motor.

    python3 scripts/metricas_campana.py resultados/campana-20261102 \
        local=http://localhost:9090 \
        aws-fase2=http://localhost:3000/api/datasources/proxy/uid/prometheus

(AWS: el Prometheus de la máquina frío, a través del túnel de SSM a Grafana.)

La ventana de cada corrida sale de su CSV (primera y última llegada
programada); se recortan los bordes para no mezclar el arranque y el final.
Deja <campaña>/analisis/servidor.csv, una fila por corrida. Solo biblioteca
estándar: se puede correr sin el entorno virtual del análisis.
"""
import csv
import json
import re
import statistics
import sys
import urllib.parse
import urllib.request
from pathlib import Path

RE_ESCALON = re.compile(r"^(?P<entorno>.+)-rps(?P<rps>\d+)-s(?P<semilla>\d+)\.csv$")
RECORTE_S = 5  # segundos que se descartan al principio y al final de cada corrida
PASO_S = 2

# nombre -> (consulta PromQL, cómo resumir la serie en la ventana)
CONSULTAS = {
    "cpu_motor_nucleos": ('sum(rate(process_cpu_seconds_total{job="fraud_engine"}[10s]))', "media"),
    "cpu_settlement_nucleos": ('sum(rate(process_cpu_seconds_total{job="settlement"}[10s]))', "media"),
    "cpu_eventos_nucleos": ('sum(rate(process_cpu_seconds_total{job="eventos"}[10s]))', "media"),
    "cpu_loadgen_nucleos": ('sum(rate(process_cpu_seconds_total{job="loadgen"}[10s]))', "media"),
    "mem_motor_mb": ('sum(process_resident_memory_bytes{job="fraud_engine"}) / 1e6', "max"),
    "en_vuelo_motor": ('sum(drts_en_vuelo{servicio="fraud_engine"})', "media"),
    "en_vuelo_motor_max": ('sum(drts_en_vuelo{servicio="fraud_engine"})', "max"),
    "handler_p99_ms": ('1000 * histogram_quantile(0.99, sum by (le) (rate(drts_latencia_segundos_bucket{servicio="fraud_engine"}[10s])))', "mediana"),
    "opcional_p99_ms": ('1000 * histogram_quantile(0.99, sum by (le) (rate(drts_fase_segundos_bucket{fase="opcional"}[10s])))', "mediana"),
    "mandatoria_p99_ms": ('1000 * histogram_quantile(0.99, sum by (le) (rate(drts_fase_segundos_bucket{fase="mandatoria"}[10s])))', "mediana"),
    "c_net_p99_ms": ('1000 * histogram_quantile(0.99, sum by (le) (rate(drts_fase_segundos_bucket{fase="c_net"}[10s])))', "mediana"),
    "drem_llegada_p50_ms": ('1000 * histogram_quantile(0.50, sum by (le) (rate(drts_drem_llegada_segundos_bucket[10s])))', "mediana"),
    "tardias_rps": ('sum(rate(drts_respuestas_tardias_total{servicio="fraud_engine"}[10s]))', "media"),
}
# Reparto entre réplicas: requests por segundo de cada instancia del motor.
REPARTO = 'sum by (instance) (rate(drts_requests_total{job="fraud_engine"}[10s]))'


def consultar(base, consulta, inicio, fin):
    params = urllib.parse.urlencode({"query": consulta, "start": inicio, "end": fin, "step": PASO_S})
    with urllib.request.urlopen(f"{base}/api/v1/query_range?{params}", timeout=30) as r:
        datos = json.load(r)
    if datos.get("status") != "success":
        raise RuntimeError(f"{consulta}: {datos}")
    return datos["data"]["result"]


def valores(serie):
    vs = []
    for _, v in serie["values"]:
        try:
            x = float(v)
        except ValueError:
            continue
        if x == x and x not in (float("inf"), float("-inf")):  # sin NaN ni infinitos
            vs.append(x)
    return vs


def resumir(vs, como):
    if not vs:
        return float("nan")
    return {"media": statistics.fmean, "max": max, "mediana": statistics.median}[como](vs)


def ventana(ruta):
    lo = hi = None
    with open(ruta, newline="") as f:
        for r in csv.DictReader(f):
            t = int(r["programada_ns"]) / 1e9
            lo = t if lo is None else min(lo, t)
            hi = t if hi is None else max(hi, t)
    return lo + RECORTE_S, hi - RECORTE_S


def main():
    campana = Path(sys.argv[1])
    bases = dict(a.split("=", 1) for a in sys.argv[2:])
    salida = campana / "analisis"
    salida.mkdir(exist_ok=True)

    filas = []
    for ruta in sorted(campana.glob("*.csv")):
        m = RE_ESCALON.match(ruta.name)
        if not m or m["entorno"] not in bases:
            continue
        base = bases[m["entorno"]]
        inicio, fin = ventana(ruta)
        fila = {"entorno": m["entorno"], "rps": int(m["rps"]), "semilla": int(m["semilla"])}
        for nombre, (q, como) in CONSULTAS.items():
            series = consultar(base, q, inicio, fin)
            fila[nombre] = resumir(valores(series[0]) if series else [], como)
        reparto = {s["metric"].get("instance", "?"): statistics.fmean(valores(s) or [0]) for s in consultar(base, REPARTO, inicio, fin)}
        total = sum(reparto.values()) or 1
        fila["replicas"] = len(reparto)
        fila["reparto_max_pct"] = 100 * max(reparto.values(), default=0) / total
        filas.append(fila)
        print(f"{fila['entorno']:<10} rps={fila['rps']:<4} s={fila['semilla']} cpu motor={fila['cpu_motor_nucleos']:.2f} "
              f"en vuelo={fila['en_vuelo_motor']:.1f} opcional p99={fila['opcional_p99_ms']:.1f}ms "
              f"réplicas={fila['replicas']} reparto máx={fila['reparto_max_pct']:.0f}%")

    with open(salida / "servidor.csv", "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=list(filas[0].keys()))
        w.writeheader()
        for fila in filas:
            w.writerow({k: (f"{v:.4f}" if isinstance(v, float) else v) for k, v in fila.items()})
    print(f"\n{len(filas)} corridas → {salida / 'servidor.csv'}")


if __name__ == "__main__":
    main()
