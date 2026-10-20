package main

import (
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// Servidor de salud real en un puerto real: SERVING → sano; NOT_SERVING o
// nadie escuchando → no sano.
func TestChequear(t *testing.T) {
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	s := health.NewServer()
	healthpb.RegisterHealthServer(g, s)
	go g.Serve(lis)
	t.Cleanup(g.Stop)

	if err := chequear(lis.Addr().String(), time.Second); err != nil {
		t.Errorf("SERVING: %v, se esperaba sano", err)
	}
	s.Shutdown() // NOT_SERVING
	if err := chequear(lis.Addr().String(), time.Second); err == nil {
		t.Errorf("NOT_SERVING: se esperaba no sano")
	}
	if err := chequear("localhost:1", 200*time.Millisecond); err == nil {
		t.Errorf("nadie escuchando: se esperaba no sano")
	}
}
