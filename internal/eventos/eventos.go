// Package eventos publica los eventos del sistema en Kafka, al costado del
// camino de decisión (ADR-0019, ADR-0023).
//
// La regla central: publicar NUNCA bloquea ni hace fallar una transacción.
// Si Kafka está lento, caído o el buffer se llenó, el evento se descarta y se
// cuenta (drts_eventos_descartados_total). El cold path no puede afectar la
// respuesta al cliente.
//
// Los eventos van en protobuf (proto/eventos.proto): el mismo lenguaje de
// esquema que el contrato gRPC, y reusan sus mensajes.
package eventos

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	pb "fd_rts/proto"
)

// VersionEsquema va en cada evento. Se sube solo ante un cambio
// incompatible (los compatibles se hacen agregando campos nuevos).
const VersionEsquema = 1

// Topics de los eventos.
type Topics struct {
	Evaluadas  string // TransaccionEvaluada, clave = card_token
	Liquidadas string // TransaccionLiquidada, clave = tx_id
}

// TopicsPorDefecto son los que crea el servicio kafka-topics del Compose.
var TopicsPorDefecto = Topics{
	Evaluadas:  "drts.transacciones.evaluadas",
	Liquidadas: "drts.transacciones.liquidadas",
}

// Brokers convierte KAFKA_BROKERS ("host1:9092,host2:9092") en la lista de
// brokers. Vacío = Kafka deshabilitado (nil).
func Brokers(lista string) []string {
	var b []string
	for _, x := range strings.Split(lista, ",") {
		if x = strings.TrimSpace(x); x != "" {
			b = append(b, x)
		}
	}
	return b
}

// Parámetros del productor. PUNTOS DE PARTIDA de diseño, no mediciones:
const (
	// maxBuffer: eventos esperando ser entregados. Es la tasa por la ventana
	// de tolerancia: a 1000 eventos/s, ~10s de Kafka caído antes de empezar
	// a descartar (~3MB de memoria). El default de franz-go es 50.000.
	maxBuffer = 10_000
	// plazoEntrega: cuánto puede esperar un evento a ser entregado. El
	// default es ILIMITADO: con Kafka caído, el buffer se llenaría de eventos
	// que nunca salen y, una vez lleno, se descartaría todo aun después de
	// que Kafka vuelva. Con plazo, los vencidos fallan y liberan lugar.
	plazoEntrega = 30 * time.Second
)

// Productor publica eventos sin bloquear. Un *Productor nil es válido y no
// hace nada: es "Kafka deshabilitado".
type Productor struct {
	cliente     *kgo.Client
	topics      Topics
	publicados  *prometheus.CounterVec
	descartados *prometheus.CounterVec
}

// NuevoProductor crea el productor. Con brokers vacío devuelve nil (Kafka
// deshabilitado). No conecta todavía: si Kafka no está, el servicio arranca
// igual (el camino de decisión no depende del cold path).
func NuevoProductor(brokers []string, topics Topics, reg prometheus.Registerer) (*Productor, error) {
	if len(brokers) == 0 {
		return nil, nil
	}
	cliente, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.MaxBufferedRecords(maxBuffer),
		kgo.RecordDeliveryTimeout(plazoEntrega),
		// Por defecto franz-go ya es idempotente y espera la confirmación de
		// todas las réplicas (acks=all): un evento confirmado no se pierde ni
		// se duplica en el broker. Esa espera es asíncrona: no le cuesta nada
		// al deadline.
	)
	if err != nil {
		return nil, err
	}
	p := &Productor{
		cliente: cliente,
		topics:  topics,
		publicados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_eventos_publicados_total",
			Help: "Eventos confirmados por Kafka, por topic.",
		}, []string{"topic"}),
		descartados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_eventos_descartados_total",
			Help: "Eventos que NO llegaron a Kafka, por topic y motivo (buffer_lleno, entrega, serializacion). La transacción no se vio afectada.",
		}, []string{"topic", "motivo"}),
	}
	// Series en 0 desde el arranque para las combinaciones conocidas: un
	// contador con etiquetas no existe hasta su primer Inc, y en el
	// dashboard "no hubo descartes" se vería igual que "no se mide".
	for _, t := range []string{topics.Evaluadas, topics.Liquidadas} {
		p.publicados.WithLabelValues(t)
		for _, m := range []string{"buffer_lleno", "entrega", "serializacion"} {
			p.descartados.WithLabelValues(t, m)
		}
	}
	reg.MustRegister(p.publicados, p.descartados, prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "drts_eventos_en_buffer",
		Help: "Eventos esperando ser entregados a Kafka. Si se acerca al máximo, Kafka no da abasto o no está.",
	}, func() float64 { return float64(cliente.BufferedProduceRecords()) }))
	return p, nil
}

// PublicarEvaluada publica que el motor respondió una transacción.
func (p *Productor) PublicarEvaluada(ev *pb.TransaccionEvaluada) {
	if p == nil {
		return
	}
	ev.Version = VersionEsquema
	p.publicar(p.topics.Evaluadas, ev.CardToken, ev)
}

// PublicarLiquidada publica que Settlement registró una transacción.
func (p *Productor) PublicarLiquidada(ev *pb.TransaccionLiquidada) {
	if p == nil {
		return
	}
	ev.Version = VersionEsquema
	p.publicar(p.topics.Liquidadas, ev.TxId, ev)
}

func (p *Productor) publicar(topic, clave string, msg proto.Message) {
	valor, err := proto.Marshal(msg)
	if err != nil {
		p.descartados.WithLabelValues(topic, "serializacion").Inc()
		return
	}
	r := &kgo.Record{Topic: topic, Key: []byte(clave), Value: valor}

	// context.Background() A PROPÓSITO (la excepción a la regla de "siempre
	// el ctx de la request"): el evento no tiene que morir con la request.
	// Si el cliente corta, lo que pasó igual tiene que quedar registrado.
	//
	// TryProduce y no Produce: Produce BLOQUEA cuando el buffer está lleno;
	// TryProduce falla al instante con ErrMaxBuffered.
	p.cliente.TryProduce(context.Background(), r, func(r *kgo.Record, err error) {
		switch {
		case err == nil:
			p.publicados.WithLabelValues(r.Topic).Inc()
		case errors.Is(err, kgo.ErrMaxBuffered):
			p.descartados.WithLabelValues(r.Topic, "buffer_lleno").Inc()
		default:
			// Plazo de entrega vencido, topic inexistente, etc.
			p.descartados.WithLabelValues(r.Topic, "entrega").Inc()
		}
	})
}

// Cerrar intenta entregar lo que quedó en el buffer (como mucho hasta que
// venza ctx) y cierra la conexión. Va al final del apagado, después de que
// terminaron las transacciones en vuelo.
func (p *Productor) Cerrar(ctx context.Context) {
	if p == nil {
		return
	}
	if err := p.cliente.Flush(ctx); err != nil {
		slog.Warn("eventos: no se entregó todo el buffer al apagar", "pendientes", p.cliente.BufferedProduceRecords(), "error", err)
	}
	p.cliente.Close()
}
