// Settlement: binario del segundo servicio del hot path. La lógica está en
// internal/settlement; acá solo se conecta a Redis y a la red.
package main

import (
	"context"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"

	"fd_rts/internal/redisclient"
	"fd_rts/internal/settlement"
	pb "fd_rts/proto"
)

// Fijos por ahora; en la Etapa 7.5 pasan a variables de entorno.
const (
	addr      = ":50052"
	redisAddr = "localhost:6379"
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
	grpcServer := grpc.NewServer()
	pb.RegisterSettlementServer(grpcServer, settlement.NuevoServidor(rdb, "drts:"))

	log.Printf("settlement escuchando en %s", addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("error sirviendo: %v", err)
	}
}
