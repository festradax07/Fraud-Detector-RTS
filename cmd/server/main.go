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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "fd_rts/proto"
)

// minBudget es el tiempo mínimo que tiene que quedar para que valga la pena
// procesar una transacción: WCET(fase mandatoria) + C_settle + C_net.
// ~35ms es un PUNTO DE PARTIDA de diseño, no un resultado medido: se
// recalibra midiendo en esta máquina (Etapa 10).
const minBudget = 35 * time.Millisecond

type fraudEngineServer struct {
	pb.UnimplementedFraudEngineServer
}

func (s *fraudEngineServer) EvaluateTransaction(ctx context.Context, req *pb.TransactionRequest) (*pb.EvaluationResponse, error) {
	start := time.Now()

	// Control de admisión (early admission drop). Va ANTES de cualquier
	// trabajo: si no hay tiempo, no gastamos CPU en algo que llegaría tarde.
	//
	// Todo cliente debe fijar un deadline. Si no lo hizo es un bug del
	// cliente (rompe el contrato de los 200ms), así que se rechaza en vez de
	// procesar "sin tiempo límite".
	deadline, ok := ctx.Deadline()
	if !ok {
		log.Printf("tx_id=%s rechazada: la llamada no trae deadline", req.TxId)
		return nil, status.Error(codes.InvalidArgument, "la llamada debe traer un deadline")
	}

	// D_rem = deadline - ahora. Si no alcanza para el trabajo mínimo, corte.
	remaining := time.Until(deadline)
	if remaining < minBudget {
		log.Printf("tx_id=%s EARLY DROP: quedan %v, mínimo %v", req.TxId, remaining, minBudget)
		return nil, status.Errorf(codes.DeadlineExceeded,
			"deadline insuficiente: quedan %v, se necesitan al menos %v", remaining, minBudget)
	}

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
