// Package pruebas tiene los helpers que comparten los tests: siempre con
// infraestructura REAL (ADR-0016). Solo lo importan archivos _test.go.
package pruebas

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
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
