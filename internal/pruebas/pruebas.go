// Package pruebas tiene los helpers que comparten los tests: siempre con
// infraestructura REAL (ADR-0016). Solo lo importan archivos _test.go.
package pruebas

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"fd_rts/internal/config"
	"fd_rts/internal/redisclient"
)

// RedisAddr es el Redis real de los tests. REDIS_ADDR permite apuntar a otro
// (por ejemplo, desde un contenedor, donde Redis no está en localhost).
func RedisAddr() string { return config.Texto("REDIS_ADDR", "localhost:6379") }

var contador atomic.Int64

// Redis conecta al Redis real y devuelve el cliente y un prefijo de claves
// ÚNICO para este test, para no pisar datos del servicio ni de otros tests
// (que pueden correr en paralelo, incluso en otros paquetes). Al terminar,
// borra las claves del prefijo y cierra el cliente.
//
// Si Redis no está levantado, el test FALLA (no se saltea): un test
// salteado se ve "ok" y esconde que falta la infraestructura.
func Redis(t testing.TB) (*redis.Client, string) {
	t.Helper()
	rdb, err := redisclient.Conectar(context.Background(), RedisAddr())
	if err != nil {
		t.Fatalf("%v (levantalo con: docker start redis-drts)", err)
	}
	prefijo := fmt.Sprintf("test:%d:%d:%d:", os.Getpid(), time.Now().UnixNano(), contador.Add(1))

	// t.Cleanup corre al terminar el test, pase o falle (como defer, pero
	// registrado desde un helper).
	t.Cleanup(func() {
		ctx := context.Background()
		// SCAN recorre TODA la base y después filtra por el patrón: con COUNT
		// alto son menos viajes.
		iter := rdb.Scan(ctx, 0, prefijo+"*", 10000).Iterator()
		for iter.Next(ctx) {
			rdb.Del(ctx, iter.Val())
		}
		rdb.Close()
	})
	return rdb, prefijo
}

// ServirGRPC levanta un servidor gRPC real en un puerto TCP real
// (localhost:0: el sistema operativo elige uno libre), con los servicios que
// registre registrar, y devuelve una conexión de cliente. Todo se apaga al
// terminar el test.
func ServirGRPC(t testing.TB, registrar func(g *grpc.Server)) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	registrar(g)
	go g.Serve(lis) // Serve bloquea: va en su propia goroutine

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("cliente gRPC: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		g.Stop()
	})
	return conn
}

// KafkaBrokers es el Kafka real de los tests (KAFKA_BROKERS, por defecto el
// de "make kafka-up").
func KafkaBrokers() []string {
	return strings.Split(config.Texto("KAFKA_BROKERS", "localhost:9092"), ",")
}

// Kafka verifica que el Kafka real responda y crea un topic ÚNICO para este
// test por cada nombre pedido (con una partición, así el orden de lectura es
// el de escritura). Devuelve los brokers y los nombres reales, en el mismo
// orden. Al terminar, borra los topics.
//
// Si Kafka no está levantado, el test FALLA (no se saltea).
func Kafka(t testing.TB, nombres ...string) (brokers []string, topics []string) {
	t.Helper()
	brokers = KafkaBrokers()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatalf("cliente Kafka: %v", err)
	}
	adm := kadm.NewClient(cl)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := adm.ListBrokers(ctx); err != nil {
		cl.Close()
		t.Fatalf("Kafka no responde en %v (levantalo con: make kafka-up): %v", brokers, err)
	}

	// Guiones y no guiones bajos: Kafka avisa que mezclar "." y "_" en los
	// nombres puede hacer chocar sus métricas.
	base := fmt.Sprintf("test-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), contador.Add(1))
	for _, n := range nombres {
		topics = append(topics, base+"-"+n)
	}
	res, err := adm.CreateTopics(ctx, 1, 1, nil, topics...)
	if err == nil {
		err = res.Error()
	}
	if err != nil {
		cl.Close()
		t.Fatalf("creando topics de prueba: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		adm.DeleteTopics(ctx, topics...)
		cl.Close()
	})
	return brokers, topics
}

// LeerKafka consume n registros de topic desde el principio (Kafka real) y
// falla el test si no llegan en 10s.
func LeerKafka(t testing.TB, brokers []string, topic string, n int) []*kgo.Record {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("consumidor: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var recs []*kgo.Record
	for len(recs) < n {
		f := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("leídos %d de %d registros de %s antes del timeout", len(recs), n, topic)
		}
		recs = append(recs, f.Records()...)
	}
	return recs
}
