// Fraud-Engine: decide si una transacción se aprueba o rechaza bajo un
// deadline duro de 200ms. Implementa el control de admisión (Etapa 1), la
// fase mandatoria contra Redis (Etapas 2 y 3) y la fase opcional según el
// slack time (Etapa 4).
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"fd_rts/internal/mandatoria"
	"fd_rts/internal/opcional"
	"fd_rts/internal/presupuesto"
	pb "fd_rts/proto"
)

// Motivos de rechazo por fraude (campo rejection_reason). Son códigos fijos
// para que el cliente y Settlement puedan decidir según el motivo; el
// detalle (cuántos intentos, cuántos km/h) va al log.
const (
	motivoBlacklist      = "BLACKLIST"
	motivoCardTesting    = "CARD_TESTING"
	motivoViajeImposible = "VIAJE_IMPOSIBLE"
	motivoRiesgoAlto     = "RIESGO_ALTO" // fase opcional: score > umbral
)

// iteracionesScoring fija el costo de CPU SIMULADO del scoring (el Z-score
// real tarda ~40ns; esto simula un modelo más pesado). Medido en esta
// máquina (Etapa 4): 10M ≈ 16ms de media, ≈ 19ms de máximo, cómodo dentro
// de WCET(O) = 80ms. Es una perilla para las Etapas 6 y 10, no un valor del
// modelo.
const iteracionesScoring = 10_000_000

// redisAddr es fijo por ahora. En la Etapa 7.5 pasa a una variable de
// entorno (REDIS_ADDR).
const redisAddr = "localhost:6379"

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
	if !presupuesto.Admitir(remaining) {
		log.Printf("tx_id=%s EARLY DROP: quedan %v, mínimo %v", req.TxId, remaining, presupuesto.MinParaAdmitir)
		return nil, status.Errorf(codes.DeadlineExceeded,
			"deadline insuficiente: quedan %v, se necesitan al menos %v", remaining, presupuesto.MinParaAdmitir)
	}

	// Validación de la request: sin tarjeta o sin coordenadas no hay con qué
	// evaluar. Es un error del cliente (InvalidArgument), no un fraude.
	//
	// tx_id es obligatorio desde la Etapa 3: es el miembro del ZSET de
	// velocidad, y vacío haría que todos esos intentos se fundieran en uno.
	if req.TxId == "" {
		return nil, status.Error(codes.InvalidArgument, "falta tx_id")
	}
	if req.CardToken == "" {
		return nil, status.Error(codes.InvalidArgument, "falta card_token")
	}
	if !mandatoria.CoordenadasValidas(req.Latitude, req.Longitude) {
		return nil, status.Errorf(codes.InvalidArgument,
			"coordenadas inválidas o ausentes: (%v, %v)", req.Latitude, req.Longitude)
	}
	// Desde la Etapa 4: el scoring usa ln(monto), que no existe para montos
	// <= 0. Se escribe !(x > 0) y no x <= 0 a propósito: NaN no es mayor ni
	// menor que nada, así que !(NaN > 0) es true y también se rechaza.
	if !(req.Amount > 0) {
		return nil, status.Errorf(codes.InvalidArgument, "amount inválido: %v", req.Amount)
	}
	if req.MerchantCategory == "" {
		return nil, status.Error(codes.InvalidArgument, "falta merchant_category")
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

	// Viaje 1 a Redis: registra el intento (ANTES de cualquier rechazo: la
	// velocidad cuenta todos los intentos) y trae blacklist, intentos y
	// posición previa, todo junto. Va con el ctx de la request: el deadline
	// del cliente llega hasta Redis.
	res, err := v.Consultar(ctx, req.TxId, req.CardToken, req.MerchantCategory, req.Latitude, req.Longitude, ahora)
	if err != nil {
		return nil, fallaDeVerificacion(req.TxId, err)
	}

	// Estadísticas de la categoría (llegaron en el mismo viaje). Una
	// categoría sin estadísticas es un error del cliente y se rechaza
	// (fail-closed: sin estadísticas no hay cómo puntuar). Se rechaza
	// aunque después la request fuera a degradarse.
	//
	// Nota: el intento ya quedó registrado en la ventana de velocidad (el
	// ZADD iba en el mismo pipeline). Es aceptable: contar de más es el lado
	// seguro.
	stats, conocida, err := opcional.ParsearEstadisticas(res.StatsCategoria)
	if err != nil {
		return nil, fallaDeVerificacion(req.TxId, err) // datos corruptos en Redis
	}
	if !conocida {
		return nil, status.Errorf(codes.InvalidArgument, "merchant_category desconocida: %q", req.MerchantCategory)
	}

	// 1. Blacklist.
	if res.EnBlacklist {
		log.Printf("tx_id=%s FRAUDE %s", req.TxId, motivoBlacklist)
		return rechazar(motivoBlacklist), nil
	}

	// 2. Control de velocidad (card testing).
	if res.Intentos > mandatoria.MaxIntentos {
		log.Printf("tx_id=%s FRAUDE %s: %d intentos en %v", req.TxId, motivoCardTesting, res.Intentos, mandatoria.VentanaVelocidad)
		return rechazar(motivoCardTesting), nil
	}

	// 3. Viaje imposible.
	if res.HayPrevia && res.Kmh > mandatoria.VelocidadMaxKmh {
		log.Printf("tx_id=%s FRAUDE %s: %.0f km/h", req.TxId, motivoViajeImposible, res.Kmh)
		return rechazar(motivoViajeImposible), nil
	}

	// --- Fase opcional (O_i) ---
	// D_rem se recalcula ACÁ: lo que importa es cuánto queda ahora, con la
	// mandatoria y el viaje a Redis ya pagados.
	dRem := time.Until(deadline)
	var riskScore float64
	degradada := !presupuesto.CorreOpcional(dRem)

	if degradada {
		// Sin slack: se responde solo con la mandatoria. Se acepta el riesgo
		// de no haber corrido el scoring a cambio de cumplir el deadline.
		log.Printf("tx_id=%s DEGRADADA: quedan %v, slack %v", req.TxId, dRem, presupuesto.Slack(dRem))
	} else {
		riskScore = opcional.Puntuar(req.Amount, stats, iteracionesScoring)
		if riskScore > opcional.UmbralRechazo {
			log.Printf("tx_id=%s FRAUDE %s: score %.3f", req.TxId, motivoRiesgoAlto, riskScore)
			resp := rechazar(motivoRiesgoAlto)
			resp.RiskScore = riskScore
			return resp, nil
		}
	}

	// Aprobada: recién ahora esta posición pasa a ser confiable (viaje 2 a
	// Redis, solo para aprobadas). Va DESPUÉS de la opcional: un rechazo por
	// riesgo tampoco puede mover la tarjeta. Si no se puede guardar, no se
	// aprueba: la próxima transacción de esta tarjeta se compararía contra
	// una posición vieja y el chequeo de viaje imposible quedaría debilitado.
	if err := v.ActualizarPosicion(ctx, req.CardToken, req.Latitude, req.Longitude, ahora); err != nil {
		return nil, fallaDeVerificacion(req.TxId, err)
	}

	log.Printf("tx_id=%s card=%s amount=%.2f score=%.3f degradada=%v -> APROBADA",
		req.TxId, req.CardToken, req.Amount, riskScore, degradada)

	return &pb.EvaluationResponse{
		TxId:             req.TxId,
		IsFraud:          false,
		RejectionReason:  "",
		RiskScore:        riskScore,
		IsDegraded:       degradada,
		ProcessingTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

// fallaDeVerificacion traduce un error de Redis a un código gRPC. En los
// dos casos es fail-closed: no se aprueba lo que no se pudo verificar. No es
// un fraude (is_fraud): es que el motor NO PUDO decidir.
func fallaDeVerificacion(txID string, err error) error {
	log.Printf("tx_id=%s SIN VERIFICAR: %v", txID, err)
	// Se acabó el deadline de la request mientras esperábamos a Redis.
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "se agotó el deadline verificando la transacción")
	}
	// Redis caído o inalcanzable.
	return status.Error(codes.Unavailable, "no se pudo verificar la transacción")
}

func main() {
	// Cliente de Redis (configuración del hot path: respeta el deadline del
	// ctx y no reintenta; ver mandatoria.NuevoClienteRedis).
	rdb := mandatoria.NuevoClienteRedis(redisAddr)
	defer rdb.Close()

	// PING al arrancar: si Redis no está, el server no arranca. Fail-closed
	// desde el inicio: mejor no atender que atender sin poder verificar.
	//
	// Acá SÍ va context.Background(): esto no es el hot path, no hay
	// request entrante de la que heredar un deadline. Le ponemos uno propio
	// para no quedarnos colgados si Redis no responde.
	ctxPing, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := rdb.Ping(ctxPing).Err()
	cancel()
	if err != nil {
		log.Fatalf("no pude conectar a Redis en %s: %v", redisAddr, err)
	}
	log.Printf("conectado a Redis en %s", redisAddr)

	addr := ":50051"
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("no pude escuchar en %s: %v", addr, err)
	}

	grpcServer := grpc.NewServer()
	// La blacklist ya no está en el código: es el SET drts:blacklist en
	// Redis (se carga con SADD, ver docs/GUIA-GO.md).
	verificador := mandatoria.NuevoVerificador(rdb, "drts:")
	pb.RegisterFraudEngineServer(grpcServer, &fraudEngineServer{verificador: verificador})

	log.Printf("fraud-engine escuchando en %s", addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("error sirviendo: %v", err)
	}
}
