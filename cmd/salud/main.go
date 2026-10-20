// salud: chequeo de salud para contenedores. Consulta el servicio estándar
// grpc.health.v1.Health/Check de un servidor gRPC y termina con código 0 si
// está SERVING, o 1 si no (o si no responde).
//
//	salud -addr localhost:50051
//
// Existe porque las imágenes son distroless: no hay shell ni curl para
// escribir un health check. Docker Compose y AWS ECS corren este binario
// dentro del contenedor; Kubernetes/k3s no lo necesita (habla el protocolo
// de salud de gRPC de forma nativa).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "servidor gRPC a chequear")
	timeout := flag.Duration("timeout", time.Second, "máximo para responder")
	flag.Parse()

	if err := chequear(*addr, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "no sano:", err)
		os.Exit(1)
	}
}

func chequear(addr string, timeout time.Duration) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if r.Status != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("estado %v", r.Status)
	}
	return nil
}
