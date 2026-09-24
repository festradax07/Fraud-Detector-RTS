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

	"fd_rts/internal/mandatoria"
	pb "fd_rts/proto"
)

// Motivos de rechazo por fraude (campo rejection_reason). Son códigos fijos
// para que el cliente y Settlement puedan decidir según el motivo; el
// detalle (cuántos intentos, cuántos km/h) va al log.
const (
	motivoBlacklist      = "BLACKLIST"
	motivoCardTesting    = "CARD_TESTING"
	motivoViajeImposible = "VIAJE_IMPOSIBLE"
)

// minBudget es el tiempo mínimo que tiene que quedar para que valga la pena
// procesar una transacción: WCET(fase mandatoria) + C_settle + C_net.
// ~35ms es un PUNTO DE PARTIDA de diseño, no un resultado medido: se
// recalibra midiendo en esta máquina (Etapa 10).
const minBudget = 35 * time.Millisecond

type fraudEngineServer struct {
	pb.UnimplementedFraudEngineServer
	verificador *mandatoria.Verificador
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

	// Validación de la request: sin tarjeta o sin coordenadas no hay con qué
	// evaluar. Es un error del cliente (InvalidArgument), no un fraude.
	if req.CardToken == "" {
		return nil, status.Error(codes.InvalidArgument, "falta card_token")
	}
	if !mandatoria.CoordenadasValidas(req.Latitude, req.Longitude) {
		return nil, status.Errorf(codes.InvalidArgument,
			"coordenadas inválidas o ausentes: (%v, %v)", req.Latitude, req.Longitude)
	}

	// rechazar arma la respuesta de fraude. Un fraude NO es un error gRPC:
	// el motor funcionó bien y decidió "no", así que va con código OK y el
	// motivo en el cuerpo. Es una closure: ve req y start de esta función.
	rechazar := func(motivo string) *pb.EvaluationResponse {
		return &pb.EvaluationResponse{
			TxId:             req.TxId,
			IsFraud:          true,
			RejectionReason:  motivo,
			ProcessingTimeMs: time.Since(start).Milliseconds(),
		}
	}

	// --- Fase mandatoria (M_i) ---
	v := s.verificador
	ahora := start

	// Se registra ANTES de cualquier rechazo: el control de velocidad cuenta
	// todos los intentos, también los que después se rechazan.
	intentos := v.RegistrarIntento(req.CardToken, ahora)

	// 1. Blacklist: el más barato, va primero.
	if v.EnBlacklist(req.CardToken) {
		log.Printf("tx_id=%s FRAUDE %s", req.TxId, motivoBlacklist)
		return rechazar(motivoBlacklist), nil
	}

	// 2. Control de velocidad (card testing).
	if intentos > mandatoria.MaxIntentos {
		log.Printf("tx_id=%s FRAUDE %s: %d intentos en %v", req.TxId, motivoCardTesting, intentos, mandatoria.VentanaVelocidad)
		return rechazar(motivoCardTesting), nil
	}

	// 3. Viaje imposible.
	kmh, hayPrevia := v.VelocidadDesdeUltima(req.CardToken, req.Latitude, req.Longitude, ahora)
	if hayPrevia && kmh > mandatoria.VelocidadMaxKmh {
		log.Printf("tx_id=%s FRAUDE %s: %.0f km/h", req.TxId, motivoViajeImposible, kmh)
		return rechazar(motivoViajeImposible), nil
	}

	// Aprobada: recién ahora esta posición pasa a ser confiable.
	v.ActualizarPosicion(req.CardToken, req.Latitude, req.Longitude, ahora)

	// TODO(etapa 4): slack time — decidir si corre la fase opcional
	// (scoring de riesgo). Hasta entonces, la respuesta es solo M_i.

	log.Printf("tx_id=%s card=%s amount=%.2f -> APROBADA", req.TxId, req.CardToken, req.Amount)

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
	// Blacklist de ejemplo, fija en el código. En la Etapa 3 pasa a ser un
	// SET en Redis.
	verificador := mandatoria.NuevoVerificador([]string{"tok-robada"})
	pb.RegisterFraudEngineServer(grpcServer, &fraudEngineServer{verificador: verificador})

	log.Printf("fraud-engine escuchando en %s", addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("error sirviendo: %v", err)
	}
}
