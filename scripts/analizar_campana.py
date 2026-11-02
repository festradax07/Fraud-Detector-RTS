#!/usr/bin/env python3
"""Analiza una campaña de medición (scripts/campana.sh) y arma las tablas y
los gráficos del informe y de la presentación.

    python3 scripts/analizar_campana.py resultados/campana-20261102

Lee los CSV por transacción del loadgen (<entorno>-rps<R>-s<S>.csv y
<entorno>-perfil-dia-habil.csv) y deja en <campaña>/analisis/:

  resumen.csv     una fila por corrida (entorno, rps, semilla, métricas)
  agregado.csv    una fila por entorno y RPS: mediana, mínimo y máximo entre semillas
  *.pdf, *.png    los gráficos (PDF para LaTeX, PNG para las diapositivas)

Métricas según el ADR-0028: se cuenta el PRIMER intento de cada transacción
(la columna categoria); una decisión a tiempo es PROCESSED con latencia
<= 200ms. Todo lo demás (EARLY_DROP, DEADLINE_MISS, AMBIGUOUS, ERROR) es un
incumplimiento: el cliente no recibió una decisión a tiempo. Las INVALID_ARGUMENT
(errores del cliente) no son transacciones válidas.
"""
import csv
import re
import statistics
import sys
from collections import defaultdict
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402

DEADLINE_US = 200_000
CATEGORIAS = ["PROCESSED", "AMBIGUOUS", "DEADLINE_MISS", "EARLY_DROP", "ERROR"]

# Paleta categórica de referencia (skill dataviz), en orden fijo. El color
# sigue al entorno, en todos los gráficos.
COLOR_ENTORNO = {"local": "#2a78d6", "aws-fase2": "#eb6834"}
NOMBRE_ENTORNO = {"local": "Notebook (Docker Compose)", "aws-fase2": "AWS Fase 2 (distribuido)"}
COLOR_CATEGORIA = dict(zip(CATEGORIAS, ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4"]))
TINTA, TINTA_2, GRILLA = "#0b0b0b", "#52514e", "#e4e3df"

RE_ESCALON = re.compile(r"^(?P<entorno>.+)-rps(?P<rps>\d+)-s(?P<semilla>\d+)\.csv$")
RE_PERFIL = re.compile(r"^(?P<entorno>.+)-perfil-dia-habil\.csv$")


def percentil(ordenados, p):
    """Percentil por rango más cercano (sin interpolar: es un valor observado)."""
    if not ordenados:
        return float("nan")
    k = max(0, min(len(ordenados) - 1, int(round(p / 100 * len(ordenados) + 0.5)) - 1))
    return ordenados[k]


def leer(ruta):
    filas = []
    with open(ruta, newline="") as f:
        for r in csv.DictReader(f):
            if r["categoria"] == "INVALID_ARGUMENT":
                continue
            filas.append(r)
    return filas


def metricas(filas):
    n = len(filas)
    lat = sorted(int(r["latencia_us"]) for r in filas)
    a_tiempo = sum(1 for r in filas if r["categoria"] == "PROCESSED" and int(r["latencia_us"]) <= DEADLINE_US)
    procesadas = [r for r in filas if r["categoria"] == "PROCESSED"]
    degradadas = sum(1 for r in procesadas if r["is_degraded"] == "true")
    m = {
        "n": n,
        "a_tiempo_pct": 100 * a_tiempo / n if n else float("nan"),
        "dmr_pct": 100 * (1 - a_tiempo / n) if n else float("nan"),
        "p50_ms": percentil(lat, 50) / 1000,
        "p99_ms": percentil(lat, 99) / 1000,
        "p999_ms": percentil(lat, 99.9) / 1000,
        "max_ms": lat[-1] / 1000 if lat else float("nan"),
        "degradadas_pct": 100 * degradadas / len(procesadas) if procesadas else 0.0,
    }
    for c in CATEGORIAS:
        m[c.lower() + "_pct"] = 100 * sum(1 for r in filas if r["categoria"] == c) / n if n else 0.0
    return m


def estilo(ax, titulo, xlab, ylab):
    ax.set_title(titulo, loc="left", fontsize=11, color=TINTA, pad=10)
    ax.set_xlabel(xlab, color=TINTA_2)
    ax.set_ylabel(ylab, color=TINTA_2)
    ax.grid(True, color=GRILLA, linewidth=0.8)
    ax.set_axisbelow(True)
    for lado in ("top", "right"):
        ax.spines[lado].set_visible(False)
    for lado in ("left", "bottom"):
        ax.spines[lado].set_color(GRILLA)
    ax.tick_params(colors=TINTA_2)


def guardar(fig, salida, nombre):
    # PDF para el informe (vectorial), PNG y JPG para las diapositivas.
    fig.tight_layout()
    for ext in ("pdf", "png", "jpg"):
        fig.savefig(salida / f"{nombre}.{ext}", dpi=200, facecolor="white", bbox_inches="tight", pad_inches=0.15)
    plt.close(fig)


def tabla_imagen(salida, nombre, titulo, encabezado, filas, nota=""):
    """Una tabla como imagen, con el mismo estilo que los gráficos."""
    alto = 0.6 + 0.3 * (len(filas) + 1)
    fig, ax = plt.subplots(figsize=(max(7, 1.05 * len(encabezado)), alto))
    ax.axis("off")
    t = ax.table(cellText=filas, colLabels=encabezado, loc="upper center", cellLoc="center")
    t.auto_set_font_size(False)
    t.set_fontsize(9)
    t.scale(1, 1.35)
    for (fila, _), celda in t.get_celld().items():
        celda.set_edgecolor(GRILLA)
        if fila == 0:
            celda.set_text_props(weight="bold", color=TINTA)
            celda.set_facecolor("#f3f2ee")
        else:
            celda.set_text_props(color=TINTA)
    ax.set_title(titulo, loc="left", fontsize=11, color=TINTA)
    if nota:
        fig.text(0.01, 0.02, nota, fontsize=8, color=TINTA_2, ha="left", va="bottom")
    guardar(fig, salida, nombre)


def linea_referencia(ax, y, texto, horizontal=True):
    if horizontal:
        ax.axhline(y, color=TINTA_2, linewidth=1, linestyle=(0, (4, 3)))
        ax.annotate(texto, xy=(1, y), xycoords=("axes fraction", "data"), xytext=(-4, 4),
                    textcoords="offset points", ha="right", va="bottom", fontsize=8, color=TINTA_2)
    else:
        ax.axvline(y, color=TINTA_2, linewidth=1, linestyle=(0, (4, 3)))
        ax.annotate(texto, xy=(y, 0), xycoords=("data", "axes fraction"), xytext=(4, 4),
                    textcoords="offset points", ha="left", va="bottom", fontsize=8, color=TINTA_2)


def main():
    campana = Path(sys.argv[1])
    salida = campana / "analisis"
    salida.mkdir(exist_ok=True)

    corridas = []          # (entorno, rps, semilla, metricas)
    perfiles = {}          # entorno -> filas
    latencias = {}         # (entorno, rps, semilla) -> latencias ordenadas (ms)
    for ruta in sorted(campana.glob("*.csv")):
        if (m := RE_ESCALON.match(ruta.name)):
            filas = leer(ruta)
            e, r, s = m["entorno"], int(m["rps"]), int(m["semilla"])
            corridas.append((e, r, s, metricas(filas)))
            latencias[(e, r, s)] = sorted(int(x["latencia_us"]) / 1000 for x in filas)
        elif (m := RE_PERFIL.match(ruta.name)):
            perfiles[m["entorno"]] = leer(ruta)
    if not corridas:
        sys.exit(f"no hay corridas en {campana}")

    # --- resumen.csv: una fila por corrida
    claves = list(corridas[0][3].keys())
    with open(salida / "resumen.csv", "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["entorno", "rps", "semilla"] + claves)
        for e, r, s, m in sorted(corridas, key=lambda c: (c[0], c[1], c[2])):
            w.writerow([e, r, s] + [f"{m[k]:.3f}" if isinstance(m[k], float) else m[k] for k in claves])

    # --- agregado.csv: mediana, mínimo y máximo entre semillas
    grupos = defaultdict(list)
    for e, r, s, m in corridas:
        grupos[(e, r)].append(m)
    agregado = {}
    with open(salida / "agregado.csv", "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["entorno", "rps", "corridas"] + [f"{k}_{a}" for k in claves if k != "n" for a in ("med", "min", "max")])
        for (e, r) in sorted(grupos):
            ms = grupos[(e, r)]
            fila = {k: (statistics.median(x[k] for x in ms), min(x[k] for x in ms), max(x[k] for x in ms))
                    for k in claves if k != "n"}
            agregado[(e, r)] = fila
            w.writerow([e, r, len(ms)] + [f"{v:.3f}" for k in fila for v in fila[k]])

    entornos = [e for e in COLOR_ENTORNO if any(k[0] == e for k in agregado)]

    # --- 1. Capacidad: % de decisiones a tiempo vs carga (la curva central)
    fig, ax = plt.subplots(figsize=(7, 4))
    for e in entornos:
        xs = sorted(r for (ee, r) in agregado if ee == e)
        med = [agregado[(e, r)]["a_tiempo_pct"][0] for r in xs]
        lo = [agregado[(e, r)]["a_tiempo_pct"][1] for r in xs]
        hi = [agregado[(e, r)]["a_tiempo_pct"][2] for r in xs]
        ax.fill_between(xs, lo, hi, color=COLOR_ENTORNO[e], alpha=0.18, linewidth=0)
        ax.plot(xs, med, color=COLOR_ENTORNO[e], linewidth=2, marker="o", markersize=5, label=NOMBRE_ENTORNO[e])
    linea_referencia(ax, 99, "SLO 99 %")
    ax.set_ylim(0, 102)
    estilo(ax, "Decisiones a tiempo (≤ 200 ms) según la carga", "carga ofrecida [RPS]", "% de transacciones")
    ax.legend(frameon=False, loc="lower left")
    guardar(fig, salida, "capacidad")

    # --- 2. Latencia: P50 y P99 vs carga (escala log), con el deadline
    fig, ax = plt.subplots(figsize=(7, 4))
    for e in entornos:
        xs = sorted(r for (ee, r) in agregado if ee == e)
        ax.plot(xs, [agregado[(e, r)]["p50_ms"][0] for r in xs], color=COLOR_ENTORNO[e], linewidth=2,
                marker="o", markersize=5, label=f"{NOMBRE_ENTORNO[e]}: P50")
        ax.plot(xs, [agregado[(e, r)]["p99_ms"][0] for r in xs], color=COLOR_ENTORNO[e], linewidth=2,
                linestyle=(0, (5, 2)), marker="s", markersize=5, label=f"{NOMBRE_ENTORNO[e]}: P99")
    linea_referencia(ax, 200, "deadline 200 ms")
    ax.set_yscale("log")
    estilo(ax, "Latencia vista por el cliente según la carga", "carga ofrecida [RPS]", "latencia [ms]")
    ax.legend(frameon=False, fontsize=8, loc="upper left")
    guardar(fig, salida, "latencia")

    # --- 3. Distribución acumulada en dos cargas (debajo y cerca del quiebre de la nube)
    for rps in (70, 130):
        claves_lat = [(e, rps, 1) for e in entornos if (e, rps, 1) in latencias]
        if not claves_lat:
            continue
        fig, ax = plt.subplots(figsize=(7, 4))
        for (e, r, s) in claves_lat:
            lat = latencias[(e, r, s)]
            ys = [100 * (i + 1) / len(lat) for i in range(len(lat))]
            ax.plot(lat, ys, color=COLOR_ENTORNO[e], linewidth=2, label=NOMBRE_ENTORNO[e])
        linea_referencia(ax, 200, "deadline", horizontal=False)
        ax.set_xscale("log")
        ax.set_ylim(0, 101)
        estilo(ax, f"Distribución acumulada de la latencia a {rps} RPS", "latencia [ms]", "% de transacciones ≤ x")
        ax.legend(frameon=False, loc="upper left")
        guardar(fig, salida, f"cdf-{rps}rps")

    # --- 4. Dónde se pierden: categorías por carga, un panel por entorno
    fig, axes = plt.subplots(1, len(entornos), figsize=(7 * len(entornos) / 1.4, 4), sharey=True, squeeze=False)
    for ax, e in zip(axes[0], entornos):
        xs = sorted(r for (ee, r) in agregado if ee == e)
        pos = range(len(xs))
        base = [0.0] * len(xs)
        for c in CATEGORIAS:
            vals = [agregado[(e, r)][c.lower() + "_pct"][0] for r in xs]
            if max(vals) == 0:
                continue
            ax.bar(pos, vals, bottom=base, color=COLOR_CATEGORIA[c], width=0.75,
                   edgecolor="white", linewidth=1, label=c)
            base = [b + v for b, v in zip(base, vals)]
        ax.set_xticks(list(pos), [str(r) for r in xs])
        estilo(ax, NOMBRE_ENTORNO[e], "carga ofrecida [RPS]", "% de transacciones" if e == entornos[0] else "")
    axes[0][-1].legend(frameon=False, fontsize=8, loc="upper left", bbox_to_anchor=(1.0, 1.0))
    guardar(fig, salida, "categorias")

    # --- 5. El día hábil: % a tiempo por hora del perfil
    if perfiles:
        fig, ax = plt.subplots(figsize=(7, 4))
        for e in entornos:
            if e not in perfiles:
                continue
            por_hora = defaultdict(list)
            for r in perfiles[e]:
                if r.get("hora_perfil"):
                    por_hora[int(float(r["hora_perfil"]))].append(r)
            horas = sorted(por_hora)
            ax.plot(horas, [metricas(por_hora[h])["a_tiempo_pct"] for h in horas], color=COLOR_ENTORNO[e],
                    linewidth=2, marker="o", markersize=4, label=NOMBRE_ENTORNO[e])
        linea_referencia(ax, 99, "SLO 99 %")
        ax.set_xticks(range(0, 25, 3))
        ax.set_ylim(0, 102)
        estilo(ax, "Día hábil comprimido (pico de 150 RPS a las 19 h)", "hora del perfil", "% decisiones a tiempo")
        ax.legend(frameon=False, loc="lower left")
        guardar(fig, salida, "perfil-dia")

    # --- 6. Goodput: decisiones a tiempo por segundo vs carga ofrecida
    fig, ax = plt.subplots(figsize=(7, 4))
    tope = max(r for (_, r) in agregado)
    ax.plot([0, tope], [0, tope], color=GRILLA, linewidth=1.5, linestyle=(0, (4, 3)))
    ax.annotate("ideal: todo a tiempo", xy=(tope, tope), xytext=(-4, -12), textcoords="offset points",
                ha="right", fontsize=8, color=TINTA_2)
    for e in entornos:
        xs = sorted(r for (ee, r) in agregado if ee == e)
        ax.plot(xs, [r * agregado[(e, r)]["a_tiempo_pct"][0] / 100 for r in xs], color=COLOR_ENTORNO[e],
                linewidth=2, marker="o", markersize=5, label=NOMBRE_ENTORNO[e])
    estilo(ax, "Goodput: decisiones a tiempo por segundo", "carga ofrecida [RPS]", "decisiones a tiempo [por s]")
    ax.legend(frameon=False, loc="upper left")
    guardar(fig, salida, "goodput")

    # --- Métricas del servidor (scripts/metricas_campana.py), si están
    servidor = defaultdict(list)
    ruta_srv = salida / "servidor.csv"
    if ruta_srv.exists():
        with open(ruta_srv, newline="") as f:
            for r in csv.DictReader(f):
                servidor[(r["entorno"], int(r["rps"]))].append(r)

    def srv(e, rps, campo):
        vs = [float(r[campo]) for r in servidor.get((e, rps), []) if r[campo] not in ("", "nan")]
        return statistics.median(vs) if vs else float("nan")

    def grafico_servidor(nombre, campo, titulo, ylab, referencia=None, log=False, leyenda="upper left"):
        fig, ax = plt.subplots(figsize=(7, 4))
        for e in entornos:
            xs = sorted(r for (ee, r) in servidor if ee == e)
            if xs:
                ax.plot(xs, [srv(e, r, campo) for r in xs], color=COLOR_ENTORNO[e], linewidth=2,
                        marker="o", markersize=5, label=NOMBRE_ENTORNO[e])
        for y, texto in (referencia or []):
            linea_referencia(ax, y, texto)
        if log:
            ax.set_yscale("log")
        estilo(ax, titulo, "carga ofrecida [RPS]", ylab)
        ax.legend(frameon=False, loc=leyenda)
        guardar(fig, salida, nombre)

    if servidor:
        grafico_servidor("cpu-motor", "cpu_motor_nucleos", "CPU usada por el motor (todas sus réplicas)",
                         "núcleos", [(4, "AWS: 2 réplicas × 2 vCPU"), (16, "notebook: 16 hilos (compartidos)")], leyenda="center left")
        grafico_servidor("en-vuelo", "en_vuelo_motor", "Transacciones en vuelo dentro del motor (promedio)",
                         "transacciones simultáneas", log=True)
        grafico_servidor("fase-opcional", "opcional_p99_ms", "Fase opcional (scoring): P99 del lado del servidor",
                         "ms", [(80, "WCET de diseño: 80 ms")], leyenda="center right")
        grafico_servidor("c-net", "c_net_p99_ms", "Red hacia Settlement (C_net): P99", "ms",
                         [(15, "reserva Settlement + red: 15 ms")])
        if any(ee == "aws-fase2" for (ee, _) in servidor):
            fig, ax = plt.subplots(figsize=(7, 4))
            xs = sorted(r for (ee, r) in servidor if ee == "aws-fase2")
            ax.plot(xs, [srv("aws-fase2", r, "reparto_max_pct") for r in xs], color=COLOR_ENTORNO["aws-fase2"],
                    linewidth=2, marker="o", markersize=5)
            linea_referencia(ax, 50, "reparto perfecto: 50 %")
            ax.set_ylim(40, 70)
            estilo(ax, "ALB: porcentaje de requests de la réplica más cargada", "carga ofrecida [RPS]", "%")
            guardar(fig, salida, "reparto-alb")

    # --- Tablas como imagen: una por entorno y la comparación lado a lado
    def fmt(v, suf="", dec=1):
        return "—" if v != v else f"{v:.{dec}f}{suf}"

    for e in entornos:
        xs = sorted(r for (ee, r) in agregado if ee == e)
        filas = []
        for r in xs:
            a = agregado[(e, r)]
            filas.append([str(r), fmt(a["a_tiempo_pct"][0], " %", 2), fmt(a["p50_ms"][0]), fmt(a["p99_ms"][0]),
                          fmt(a["max_ms"][0]), fmt(a["ambiguous_pct"][0], " %", 2),
                          fmt(a["deadline_miss_pct"][0], " %", 2), fmt(a["degradadas_pct"][0], " %"),
                          fmt(srv(e, r, "cpu_motor_nucleos"), "", 2), fmt(srv(e, r, "en_vuelo_motor"))])
        tabla_imagen(salida, f"tabla-{e}", f"Resultados: {NOMBRE_ENTORNO[e]}",
                     ["RPS", "a tiempo", "P50 ms", "P99 ms", "máx ms", "ambiguas", "vencidas", "degradadas",
                      "CPU motor", "en vuelo"], filas,
                     "Mediana de 3 corridas de 60 s (semillas 1-3). Latencia medida por el cliente; CPU en núcleos.")

    comunes = sorted({r for (e, r) in agregado if e == "local"} & {r for (e, r) in agregado if e == "aws-fase2"})
    if comunes:
        filas = []
        for r in comunes:
            lo, aw = agregado[("local", r)], agregado[("aws-fase2", r)]
            filas.append([str(r), fmt(lo["a_tiempo_pct"][0], " %", 2), fmt(aw["a_tiempo_pct"][0], " %", 2),
                          fmt(lo["p50_ms"][0]), fmt(aw["p50_ms"][0]), fmt(lo["p99_ms"][0]), fmt(aw["p99_ms"][0]),
                          fmt(srv("local", r, "cpu_motor_nucleos"), "", 2), fmt(srv("aws-fase2", r, "cpu_motor_nucleos"), "", 2)])
        tabla_imagen(salida, "tabla-comparacion", "Notebook vs AWS Fase 2, misma carga y mismas semillas",
                     ["RPS", "a tiempo\nnotebook", "a tiempo\nAWS", "P50 ms\nnotebook", "P50 ms\nAWS",
                      "P99 ms\nnotebook", "P99 ms\nAWS", "CPU motor\nnotebook", "CPU motor\nAWS"], filas,
                     "Mediana de 3 corridas por punto. Imagen sha-caf2c7e en los dos entornos.")

    tabla_imagen(salida, "tabla-recursos", "Recursos de cada entorno",
                 ["", "Notebook (Docker Compose)", "AWS Fase 2"],
                 [["CPU del motor", "i7-12650H, 16 hilos, compartidos", "2 × c7i.large (2 vCPU c/u)"],
                  ["Memoria", "31 GB, compartida", "4 GB por máquina"],
                  ["Resto del sistema", "en la misma máquina", "una máquina por rol"],
                  ["Total", "1 máquina", "10 vCPU, 24 GB, 5 máquinas"],
                  ["Red interna", "bridge de Docker", "VPC, una zona, ALB con TLS"],
                  ["Costo", "—", "≈ 0,48 USD por hora"]])

    # --- Tabla en consola (para revisar)
    print(f"{'entorno':<11}{'RPS':>5}{'a tiempo':>10}{'P50':>9}{'P99':>9}{'máx':>9}{'ambig.':>8}{'venc.':>8}{'degr.':>7}")
    for (e, r) in sorted(agregado):
        a = agregado[(e, r)]
        print(f"{e:<11}{r:>5}{a['a_tiempo_pct'][0]:>9.2f}%{a['p50_ms'][0]:>8.1f}ms{a['p99_ms'][0]:>7.1f}ms"
              f"{a['max_ms'][0]:>7.1f}ms{a['ambiguous_pct'][0]:>7.2f}%{a['deadline_miss_pct'][0]:>7.2f}%"
              f"{a['degradadas_pct'][0]:>6.1f}%")
    print(f"\ngráficos y tablas en {salida}")


if __name__ == "__main__":
    main()
