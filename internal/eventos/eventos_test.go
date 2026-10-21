package eventos

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	"fd_rts/internal/pruebas"
	pb "fd_rts/proto"
)

func TestPublicarYLeer(t *testing.T) {
	brokers, topics := pruebas.Kafka(t, "evaluadas", "liquidadas")
	p, err := NuevoProductor(brokers, Topics{Evaluadas: topics[0], Liquidadas: topics[1]}, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("productor: %v", err)
	}

	p.PublicarEvaluada(&pb.TransaccionEvaluada{
		TxId: "t1", CardToken: "tok-1", Amount: 100, MerchantCategory: "grocery_pos",
		Evaluacion: &pb.EvaluationResponse{TxId: "t1", IsFraud: true, RejectionReason: "BLACKLIST"},
	})
	p.PublicarLiquidada(&pb.TransaccionLiquidada{TxId: "t1", Estado: pb.TxStatus_STATUS_REJECTED_FRAUD})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.Cerrar(ctx) // entrega lo que quedó en el buffer

	r := pruebas.LeerKafka(t, brokers, topics[0], 1)[0]
	var ev pb.TransaccionEvaluada
	if err := proto.Unmarshal(r.Value, &ev); err != nil {
		t.Fatalf("decodificar: %v", err)
	}
	if string(r.Key) != "tok-1" {
		t.Errorf("clave = %q, se esperaba el card_token (orden por tarjeta)", r.Key)
	}
	if ev.Version != VersionEsquema || ev.TxId != "t1" || ev.Evaluacion.GetRejectionReason() != "BLACKLIST" {
		t.Errorf("evento = %+v", &ev)
	}

	r = pruebas.LeerKafka(t, brokers, topics[1], 1)[0]
	var liq pb.TransaccionLiquidada
	if err := proto.Unmarshal(r.Value, &liq); err != nil {
		t.Fatalf("decodificar: %v", err)
	}
	if string(r.Key) != "t1" || liq.Estado != pb.TxStatus_STATUS_REJECTED_FRAUD {
		t.Errorf("clave=%q evento=%+v", r.Key, &liq)
	}

	if got := testutil.ToFloat64(p.publicados.WithLabelValues(topics[0])); got != 1 {
		t.Errorf("publicados = %v, se esperaba 1", got)
	}
}

// LA propiedad de tiempo real: con Kafka caído, publicar no bloquea. Se
// publican más eventos de los que entran en el buffer: los que no entran se
// descartan y se cuentan, y ninguna llamada espera.
func TestKafkaCaidoNoBloquea(t *testing.T) {
	p, err := NuevoProductor([]string{"localhost:1"}, Topics{Evaluadas: "x", Liquidadas: "y"}, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("productor: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		p.Cerrar(ctx)
	}()

	const n = 2 * maxBuffer
	var peor time.Duration
	inicio := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()
		p.PublicarEvaluada(&pb.TransaccionEvaluada{TxId: "t", CardToken: "tok"})
		if d := time.Since(t0); d > peor {
			peor = d
		}
	}
	total := time.Since(inicio)

	// "No bloquea" con margen holgado: una llamada que esperara a Kafka
	// tardaría lo que tarda un intento de conexión o el plazo de entrega.
	if peor > 10*time.Millisecond {
		t.Errorf("la llamada más lenta tardó %v: publicar no puede bloquear", peor)
	}
	// Ningún evento se pierde sin quedar contado: cada uno está en el
	// buffer, o descartado por buffer lleno, o descartado por entrega.
	//
	// Trampa (encontrada por un test intermitente): la primera versión leía
	// la métrica apenas terminaba el bucle y a veces daba 9997 en vez de
	// 10.000. franz-go avisa los descartes en callbacks ASÍNCRONOS: algunos
	// todavía no habían corrido. Se espera a que la contabilidad cierre.
	var lleno, entrega, enBuffer float64
	limite := time.Now().Add(2 * time.Second)
	for {
		lleno = testutil.ToFloat64(p.descartados.WithLabelValues("x", "buffer_lleno"))
		entrega = testutil.ToFloat64(p.descartados.WithLabelValues("x", "entrega"))
		enBuffer = float64(p.cliente.BufferedProduceRecords())
		if lleno+entrega+enBuffer == n || time.Now().After(limite) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lleno+entrega+enBuffer != n {
		t.Errorf("contabilidad: buffer_lleno=%v + entrega=%v + en_buffer=%v != %d publicados", lleno, entrega, enBuffer, n)
	}
	if lleno < n/2-1000 {
		t.Errorf("buffer_lleno = %v: con el doble del buffer, cerca de la mitad tendría que descartarse", lleno)
	}
	t.Logf("%d publicaciones con Kafka caído en %v (peor llamada %v): buffer_lleno=%v entrega=%v en_buffer=%v", n, total, peor, lleno, entrega, enBuffer)
}

// Sin brokers, Kafka está deshabilitado: el productor es nil y no hace nada.
func TestDeshabilitado(t *testing.T) {
	p, err := NuevoProductor(nil, TopicsPorDefecto, prometheus.NewRegistry())
	if p != nil || err != nil {
		t.Fatalf("p=%v err=%v, se esperaba nil, nil", p, err)
	}
	p.PublicarEvaluada(&pb.TransaccionEvaluada{}) // no hace panic
	p.PublicarLiquidada(&pb.TransaccionLiquidada{})
	p.Cerrar(context.Background())
}

func TestBrokers(t *testing.T) {
	casos := map[string]int{"": 0, "kafka:29092": 1, "a:1, b:2 ,": 2}
	for entrada, want := range casos {
		if got := len(Brokers(entrada)); got != want {
			t.Errorf("Brokers(%q): %d, se esperaba %d", entrada, got, want)
		}
	}
}
