package main

// Métricas de la demo y el estado de las tarjetas de los celulares, para el
// dashboard "D-RTS — Demo en vivo".
//
// La etiqueta "tarjeta" es una excepción a la regla de no poner
// identificadores en las etiquetas de Prometheus (cada valor distinto es una
// serie nueva; con millones de tarjetas lo tumbaría). Acá vale porque el
// conjunto está acotado: las tarjetas de los celulares de una clase, con un
// tope de maxTarjetasPanel.

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"fd_rts/internal/mandatoria"
	"fd_rts/internal/metricas"
)

// maxTarjetasPanel: tarjetas que se siguen una por una en el dashboard.
const maxTarjetasPanel = 500

// Estados de una tarjeta, como los observan sus compras.
const (
	estadoActiva    = "activa"
	estadoRevision  = "en revisión" // fraude detectado: el bloqueo viene por el camino frío
	estadoBloqueada = "bloqueada"   // una compra ya dio BLACKLIST
)

// NumeroDeFantasia es el número que se muestra para una tarjeta, en el
// celular y en el dashboard. Claramente no real ("DRTS ..."), único por
// tarjeta y siempre el mismo para el mismo token.
func NumeroDeFantasia(token string) string {
	h := fnv.New64a()
	h.Write([]byte(token))
	d := fmt.Sprintf("%012d", h.Sum64()%1_000_000_000_000)
	return "DRTS " + d[0:4] + " " + d[4:8] + " " + d[8:12]
}

type tarjeta struct {
	numero           string
	estado           string
	compras, fraudes int
	alta             time.Time // primera compra: ordena la tabla (las nuevas arriba)
}

type metricasDemo struct {
	compras  *prometheus.CounterVec
	latencia prometheus.Histogram
	porComp  *prometheus.GaugeVec // compras por tarjeta (etiquetas: tarjeta, estado)
	porFrau  *prometheus.GaugeVec // fraudes detectados por tarjeta
	porAlta  *prometheus.GaugeVec // momento de la primera compra (Unix ms)

	mu       sync.Mutex
	tarjetas map[string]*tarjeta // token → tarjeta
}

func nuevasMetricasDemo(reg prometheus.Registerer) *metricasDemo {
	m := &metricasDemo{
		compras: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_demo_compras_total",
			Help: "Compras de la demo, por escenario (botón), decisión y motivo.",
		}, []string{"escenario", "decision", "motivo"}),
		latencia: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "drts_demo_latencia_segundos",
			Help:    "Latencia del sistema de cada compra de la demo, medida en el borde (el gateway): lo que cuenta contra los 200ms.",
			Buckets: metricas.BucketsLatencia,
		}),
		porComp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "drts_demo_tarjeta_compras",
			Help: "Compras de cada tarjeta de la demo (número de fantasía) y su estado.",
		}, []string{"tarjeta", "estado"}),
		porFrau: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "drts_demo_tarjeta_fraudes",
			Help: "Fraudes detectados en cada tarjeta de la demo y su estado.",
		}, []string{"tarjeta", "estado"}),
		porAlta: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "drts_demo_tarjeta_alta_ms",
			Help: "Momento de la primera compra de cada tarjeta de la demo (Unix ms): el dashboard muestra las nuevas arriba.",
		}, []string{"tarjeta", "estado"}),
		tarjetas: map[string]*tarjeta{},
	}
	reg.MustRegister(m.compras, m.latencia, m.porComp, m.porFrau, m.porAlta)
	return m
}

// registrar anota una compra: contadores, latencia y el estado de la
// tarjeta (si es de un celular; la robada es de "otra persona").
func (m *metricasDemo) registrar(escenario, token string, res resultado) {
	if m == nil {
		return
	}
	m.compras.WithLabelValues(escenario, res.Decision, res.Motivo).Inc()
	m.latencia.Observe(res.SistemaMs / 1000)
	if !strings.HasPrefix(token, "qr-") {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tarjetas[token]
	if !ok {
		if len(m.tarjetas) >= maxTarjetasPanel {
			return
		}
		t = &tarjeta{numero: NumeroDeFantasia(token), estado: estadoActiva, alta: time.Now()}
		m.tarjetas[token] = t
	}
	anterior := t.estado
	t.compras++
	if res.Decision == "rechazada" {
		t.fraudes++
	}
	switch res.Motivo {
	case mandatoria.MotivoBlacklist:
		t.estado = estadoBloqueada
	case mandatoria.MotivoCardTesting, mandatoria.MotivoViajeImposible:
		if t.estado != estadoBloqueada {
			t.estado = estadoRevision
		}
	}
	// Una serie por (tarjeta, estado): al cambiar de estado se borra la vieja,
	// así la tabla del dashboard muestra una sola fila por tarjeta.
	if anterior != t.estado {
		m.porComp.DeleteLabelValues(t.numero, anterior)
		m.porFrau.DeleteLabelValues(t.numero, anterior)
		m.porAlta.DeleteLabelValues(t.numero, anterior)
	}
	m.porComp.WithLabelValues(t.numero, t.estado).Set(float64(t.compras))
	m.porFrau.WithLabelValues(t.numero, t.estado).Set(float64(t.fraudes))
	m.porAlta.WithLabelValues(t.numero, t.estado).Set(float64(t.alta.UnixMilli()))
}
