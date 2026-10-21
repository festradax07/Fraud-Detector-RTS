// Package consumo es el bucle que comparten los consumidores de eventos
// (cmd/eventos): leer de Kafka, procesar y confirmar (ADR-0025).
//
// Entrega "al menos una vez": el offset (hasta dónde se leyó) se confirma
// recién DESPUÉS de procesar cada lote. Si el consumidor se cae a la mitad,
// al volver relee lo que no había confirmado. Por eso cada rol tiene que ser
// idempotente: procesar dos veces lo mismo no puede cambiar el resultado.
package consumo

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Manejador procesa un evento. Un error se cuenta y se loguea, y el
// consumidor sigue: un evento que no se puede procesar (por ejemplo, mal
// formado) no puede frenar a todos los que vienen detrás.
type Manejador func(ctx context.Context, r *kgo.Record) error

// Metricas de un consumidor. El lag (cuántos eventos le faltan leer) es la
// señal de que el cold path no da abasto.
type Metricas struct {
	procesados *prometheus.CounterVec
	errores    *prometheus.CounterVec
	lag        *prometheus.GaugeVec
}

// NuevasMetricas registra las métricas de consumo.
func NuevasMetricas(reg prometheus.Registerer, grupo string) *Metricas {
	fijas := prometheus.Labels{"grupo": grupo}
	m := &Metricas{
		procesados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_consumo_procesados_total", Help: "Eventos procesados, por topic.", ConstLabels: fijas,
		}, []string{"topic"}),
		errores: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_consumo_errores_total", Help: "Eventos que no se pudieron procesar, por topic.", ConstLabels: fijas,
		}, []string{"topic"}),
		lag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "drts_consumo_lag", Help: "Eventos que faltan leer, por topic y partición (marca de agua alta - próximo offset).", ConstLabels: fijas,
		}, []string{"topic", "particion"}),
	}
	reg.MustRegister(m.procesados, m.errores, m.lag)
	return m
}

// Config de un consumidor.
type Config struct {
	Brokers []string
	// Grupo: consumer group. Cada grupo recibe TODOS los eventos; dentro de
	// un grupo, las particiones se reparten entre sus consumidores.
	Grupo  string
	Topics []string
}

// Correr consume hasta que ctx se cancele. Un grupo nuevo empieza desde el
// principio de cada topic (la retención es de 7 días): la auditoría no se
// pierde lo que pasó antes de que arrancara.
func Correr(ctx context.Context, cfg Config, m *Metricas, manejar Manejador) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Grupo),
		kgo.ConsumeTopics(cfg.Topics...),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		// Confirmación manual, después de procesar (al menos una vez). Con
		// autocommit, franz-go confirmaría cada pocos segundos lo LEÍDO,
		// aunque todavía no se hubiera procesado.
		kgo.DisableAutoCommit(),
		// Sin rebalanceos mientras se procesa un lote: si las particiones
		// cambiaran de dueño a mitad de camino, otro consumidor podría
		// procesar lo mismo y la confirmación caería sobre algo que ya no es
		// nuestro. Se habilitan de nuevo después de confirmar.
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return err
	}
	defer cl.Close()

	for {
		f := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			break
		}
		f.EachError(func(topic string, particion int32, err error) {
			slog.Warn("consumo: error leyendo", "topic", topic, "particion", particion, "error", err)
		})
		f.EachPartition(func(p kgo.FetchTopicPartition) {
			for _, r := range p.Records {
				if err := manejar(ctx, r); err != nil {
					m.errores.WithLabelValues(r.Topic).Inc()
					slog.Warn("consumo: evento no procesado", "topic", r.Topic, "offset", r.Offset, "error", err)
					continue
				}
				m.procesados.WithLabelValues(r.Topic).Inc()
			}
			if n := len(p.Records); n > 0 {
				siguiente := p.Records[n-1].Offset + 1
				m.lag.WithLabelValues(p.Topic, strconv.Itoa(int(p.Partition))).Set(float64(p.HighWatermark - siguiente))
			}
		})
		if err := cl.CommitUncommittedOffsets(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("consumo: no se pudo confirmar el offset (se va a releer)", "error", err)
		}
		cl.AllowRebalance()
	}

	// Al apagar: confirmar lo procesado con un plazo propio (ctx ya venció).
	ctxFin, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = cl.CommitUncommittedOffsets(ctxFin)

	// Trampa (encontrada por un test que quedó colgado): el último
	// PollFetches dejó los rebalanceos BLOQUEADOS (BlockRebalanceOnPoll), y
	// Close tiene que abandonar el grupo, que también es un rebalanceo. Sin
	// esto, Close espera para siempre: un deadlock.
	cl.AllowRebalance()
	return err
}
