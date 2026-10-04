// Package metricas instrumenta los servicios del D-RTS con Prometheus.
//
// Dos niveles:
//   - Por RPC (RPC + Interceptor): tasa, errores por motivo, latencia,
//     requests en vuelo y respuestas tardías. Lo pone un interceptor de
//     gRPC, sin tocar la lógica de negocio.
//   - Del dominio (Motor, en el Fraud-Engine): D_rem al llegar, slack,
//     duración de cada fase. Esto pasa ADENTRO del handler, así que se
//     registra ahí, en pocos puntos.
//
// Convenciones de Prometheus: unidades base (segundos, no ms), contadores
// terminados en _total, nombres en snake_case con prefijo drts_. Las
// etiquetas (labels) tienen valores ACOTADOS (códigos gRPC, motivos de
// internal/fallas): una etiqueta con valores sin límite (por ejemplo,
// tx_id) crearía una serie por valor y tiraría abajo a Prometheus.
package metricas

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"fd_rts/internal/fallas"
)

// BucketsLatencia (segundos): los límites caen donde el sistema toma
// decisiones, no a ojo. Prometheus calcula percentiles interpolando DENTRO
// de cada tramo, así que conviene tener un límite justo en cada umbral:
// 35ms (admisión), 95ms (umbral del slack: 15 + 80), 200ms (deadline).
var BucketsLatencia = []float64{.005, .01, .02, .035, .05, .08, .095, .15, .2, .25}

// BucketsFase (segundos): las fases miden de 0.1ms a decenas de ms. Tramos
// finos por debajo de 1ms (lo medido para Redis y Settlement) y límites en
// los presupuestos: 15ms (C_settle+C_net), 20ms (WCET M), 80ms (WCET O).
var BucketsFase = []float64{.0001, .00025, .0005, .001, .002, .005, .01, .015, .02, .035, .08, .2}

// BucketsSlack (segundos): el slack puede ser negativo. El límite clave es
// 0: de un lado corre la opcional, del otro se degrada.
var BucketsSlack = []float64{-.15, -.1, -.05, -.02, -.01, 0, .01, .02, .05, .1, .15}

// NuevoRegistro crea un registro de métricas propio, con las métricas del
// runtime de Go (GC, goroutines, memoria) y del proceso (CPU, archivos).
//
// Propio y no el global (prometheus.DefaultRegisterer): registrar dos veces
// la misma métrica en el global hace panic, y los tests crean varios
// servers en el mismo proceso.
func NuevoRegistro() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// Servir expone reg en http://addr/metrics, en segundo plano. Si no puede
// escuchar, lo loguea y sigue: sin métricas el servicio funciona igual.
func Servir(addr string, reg *prometheus.Registry) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("métricas: no pude servir en %s: %v", addr, err)
		}
	}()
	return srv
}

// RPC son las métricas por llamada de un servicio gRPC.
type RPC struct {
	requests *prometheus.CounterVec
	latencia prometheus.Histogram
	enVuelo  prometheus.Gauge
	tardias  prometheus.Counter
}

// NuevoRPC registra las métricas por RPC de un servicio (etiqueta fija
// servicio="fraud_engine" | "settlement").
func NuevoRPC(reg prometheus.Registerer, servicio string) *RPC {
	fijas := prometheus.Labels{"servicio": servicio}
	r := &RPC{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "drts_requests_total",
			Help:        "Requests atendidas, por código gRPC y motivo (ErrorInfo.Reason; vacío si OK).",
			ConstLabels: fijas,
		}, []string{"codigo", "motivo"}),
		latencia: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:        "drts_latencia_segundos",
			Help:        "Latencia del handler, medida del lado del servidor.",
			ConstLabels: fijas,
			Buckets:     BucketsLatencia,
		}),
		enVuelo: prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        "drts_en_vuelo",
			Help:        "Requests siendo atendidas en este momento. Sin cola, todas compiten por el CPU a la vez.",
			ConstLabels: fijas,
		}),
		tardias: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "drts_respuestas_tardias_total",
			Help:        "Requests que terminaron con el contexto ya vencido o cancelado: el cliente ya no esperaba la respuesta.",
			ConstLabels: fijas,
		}),
	}
	reg.MustRegister(r.requests, r.latencia, r.enVuelo, r.tardias)
	return r
}

// Interceptor devuelve un interceptor unario de gRPC: código que gRPC corre
// alrededor de CADA llamada, antes y después del handler, sin que el
// handler se entere.
//
// alResponder (opcional) recibe la respuesta de las llamadas exitosas, para
// métricas que dependen del contenido (decisiones, estados).
func (r *RPC) Interceptor(alResponder func(resp any)) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		r.enVuelo.Inc()
		defer r.enVuelo.Dec()

		inicio := time.Now()
		resp, err := handler(ctx, req) // acá corre el handler de verdad
		r.latencia.Observe(time.Since(inicio).Seconds())

		r.requests.WithLabelValues(status.Code(err).String(), fallas.Motivo(err)).Inc()

		// El servidor NO se entera de un DEADLINE_MISS: para él la request
		// "salió bien", aunque el cliente ya se fue. Esta es la forma de
		// verlo de este lado: terminar con el contexto ya vencido.
		if ctx.Err() != nil {
			r.tardias.Inc()
		}

		if err == nil && alResponder != nil {
			alResponder(resp)
		}
		return resp, err
	}
}

// Motor son las métricas del dominio del Fraud-Engine. Todos los métodos
// aceptan un receptor nil y no hacen nada: así el handler funciona igual si
// se arma sin métricas (por ejemplo, en tests).
type Motor struct {
	decisiones  *prometheus.CounterVec
	dremLlegada prometheus.Histogram
	slack       prometheus.Histogram
	fase        *prometheus.HistogramVec
}

// Fases medidas por separado.
const (
	FaseMandatoria = "mandatoria" // viaje 1 a Redis + chequeos
	FaseOpcional   = "opcional"   // scoring
	FaseSettlement = "settlement" // llamada completa a Settlement
	FaseCSettle    = "c_settle"   // adentro de Settlement (trailer)
	FaseCNet       = "c_net"      // red + gRPC = settlement - c_settle
)

// NuevoMotor registra las métricas de dominio del Fraud-Engine.
func NuevoMotor(reg prometheus.Registerer) *Motor {
	m := &Motor{
		decisiones: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_decisiones_total",
			Help: "Decisiones respondidas: aprobada/rechazada, motivo del rechazo y si fue degradada (sin fase opcional).",
		}, []string{"decision", "motivo", "degradada"}),
		dremLlegada: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "drts_drem_llegada_segundos",
			Help:    "D_rem (deadline - ahora) al llegar la request, antes de la admisión. Si baja, hay espera antes del handler.",
			Buckets: BucketsLatencia,
		}),
		slack: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "drts_slack_segundos",
			Help:    "Slack S_i al decidir si corre la fase opcional. Negativo = degradada.",
			Buckets: BucketsSlack,
		}),
		fase: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "drts_fase_segundos",
			Help:    "Duración de cada fase de una transacción.",
			Buckets: BucketsFase,
		}, []string{"fase"}),
	}
	reg.MustRegister(m.decisiones, m.dremLlegada, m.slack, m.fase)
	return m
}

// DRemLlegada registra el D_rem con el que llegó una request.
func (m *Motor) DRemLlegada(d time.Duration) {
	if m != nil {
		m.dremLlegada.Observe(d.Seconds())
	}
}

// Slack registra el slack al decidir la fase opcional.
func (m *Motor) Slack(s time.Duration) {
	if m != nil {
		m.slack.Observe(s.Seconds())
	}
}

// Fase registra cuánto tardó una fase.
func (m *Motor) Fase(fase string, d time.Duration) {
	if m != nil {
		m.fase.WithLabelValues(fase).Observe(d.Seconds())
	}
}

// Decision registra la decisión respondida al cliente.
func (m *Motor) Decision(fraude bool, motivo string, degradada bool) {
	if m == nil {
		return
	}
	decision := "aprobada"
	if fraude {
		decision = "rechazada"
	}
	deg := "no"
	if degradada {
		deg = "si"
	}
	m.decisiones.WithLabelValues(decision, motivo, deg).Inc()
}

// Settlement son las métricas de dominio del servicio Settlement.
type Settlement struct {
	estados  *prometheus.CounterVec
	latencia prometheus.Histogram
}

// NuevoSettlement registra las métricas de dominio de Settlement.
func NuevoSettlement(reg prometheus.Registerer) *Settlement {
	s := &Settlement{
		estados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_settlement_estados_total",
			Help: "Resultados registrados por Settlement (COMMITTED, REJECTED_FRAUD, ABORTED_TIMEOUT).",
		}, []string{"estado"}),
		latencia: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "drts_latencia_extremo_a_extremo_segundos",
			Help:    "Desde la emisión en el cliente hasta que llega a Settlement (emission_timestamp_ns).",
			Buckets: BucketsLatencia,
		}),
	}
	reg.MustRegister(s.estados, s.latencia)
	return s
}

// Registrado anota un resultado de Settlement y su latencia de extremo a
// extremo (latencia <= 0 = la request no traía emission_timestamp).
func (s *Settlement) Registrado(estado string, latencia time.Duration) {
	if s == nil {
		return
	}
	s.estados.WithLabelValues(estado).Inc()
	if latencia > 0 {
		s.latencia.Observe(latencia.Seconds())
	}
}
