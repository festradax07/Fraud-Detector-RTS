// Settlement: binario del segundo servicio del hot path. La lógica está en
// internal/settlement; acá solo se conecta a Redis y a la red.
package main

import (
	"context"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"

	"fd_rts/internal/metricas"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/settlement"
	pb "fd_rts/proto"
)

// Fijos por ahora; en la Etapa 7.5 pasan a variables de entorno.
const (
	addr         = ":50052"
	redisAddr    = "localhost:6379"
	metricasAddr = ":2113" // /metrics para Prometheus (Etapa 7)
)

func main() {
	rdb := redisclient.Nuevo(redisAddr)
	defer rdb.Close()

	// Fail-closed desde el arranque, igual que el Fraud-Engine.
	ctxPing, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := rdb.Ping(ctxPing).Err()
	cancel()
	if err != nil {
		log.Fatalf("no pude conectar a Redis en %s: %v", redisAddr, err)
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("no pude escuchar en %s: %v", addr, err)
	}
	// Métricas (Etapa 7). Los estados y la latencia de extremo a extremo
	// salen de la respuesta: el handler no se toca.
	reg := metricas.NuevoRegistro()
	rpc := metricas.NuevoRPC(reg, "settlement")
	dominio := metricas.NuevoSettlement(reg)
	metricas.Servir(metricasAddr, reg)
	log.Printf("métricas en http://localhost%s/metrics", metricasAddr)

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(rpc.Interceptor(func(resp any) {
		if r, ok := resp.(*pb.SettlementResponse); ok {
			dominio.Registrado(r.Status.String(), time.Duration(r.EndToEndLatencyMs)*time.Millisecond)
		}
	})))
	pb.RegisterSettlementServer(grpcServer, settlement.NuevoServidor(rdb, "drts:"))

	log.Printf("settlement escuchando en %s", addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("error sirviendo: %v", err)
	}
}
