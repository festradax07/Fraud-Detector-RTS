// Fraud-Engine — esqueleto inicial.
//
// Por ahora este server no implementa nada del mecanismo real (fase
// mandatoria, control de admisión, slack time). Es solo un servidor gRPC
// que compila, levanta, y responde algo fijo — el punto de partida para ir
// agregando cada pieza por separado y entender qué hace cada una.
package main

import (
	"context"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"

	pb "fd_rts/proto"
)

type fraudEngineServer struct {
	pb.UnimplementedFraudEngineServer
}

func (s *fraudEngineServer) EvaluateTransaction(ctx context.Context, req *pb.TransactionRequest) (*pb.EvaluationResponse, error) {
	start := time.Now()

	// TODO(paso 1): control de admisión — chequear cuánto deadline queda
	// antes de hacer cualquier trabajo (early admission drop).
	//
	// TODO(paso 2): fase mandatoria — blacklist, velocidad (Redis ZSET),
	// viaje imposible.
	//
	// TODO(paso 3): slack time — decidir si corre la fase opcional.
	//
	// TODO(paso 4): fase opcional — scoring de riesgo.

	log.Printf("tx_id=%s user_id=%s amount=%.2f -> stub OK", req.TxId, req.UserId, req.Amount)

	return &pb.EvaluationResponse{
		TxId:             req.TxId,
		IsFraud:          false,
		RejectionReason:  "",
		RiskScore:        0.0,
		IsDegraded:       false,
		ProcessingTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func main() {
	addr := ":50051"
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("no pude escuchar en %s: %v", addr, err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterFraudEngineServer(grpcServer, &fraudEngineServer{})

	log.Printf("fraud-engine (esqueleto) escuchando en %s", addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("error sirviendo: %v", err)
	}
}
