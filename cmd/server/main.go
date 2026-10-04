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
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"fd_rts/internal/mandatoria"
	"fd_rts/internal/opcional"
	"fd_rts/internal/presupuesto"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/settlement"
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

// settlementAddr: dónde escucha Settlement (Etapa 5). Mismo criterio que
// redisAddr: pasa a variable de entorno en la Etapa 7.5.
const settlementAddr = "localhost:50052"

type fraudEngineServer struct {
	pb.UnimplementedFraudEngineServer
	verificador *mandatoria.Verificador
	// settle es el cliente gRPC de Settlement: desde la Etapa 5 el
	// Fraud-Engine es servidor (de los clientes) Y cliente (de Settlement).
	settle pb.SettlementClient
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

	// rechazar arma la respuesta de fraude y la manda a Settlement. Un
	// fraude NO es un error gRPC: el motor funcionó bien y decidió "no", así
	// que va con código OK y el motivo en el cuerpo. Es una closure: ve ctx,
	// req y start de esta función.
	rechazar := func(motivo string, score float64) (*pb.EvaluationResponse, error) {
		return s.liquidar(ctx, req, &pb.EvaluationResponse{
			TxId:            req.TxId,
			IsFraud:         true,
			RejectionReason: motivo,
			RiskScore:       score,
		}, start)
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
		return rechazar(motivoBlacklist, 0)
	}

	// 2. Control de velocidad (card testing).
	if res.Intentos > mandatoria.MaxIntentos {
		log.Printf("tx_id=%s FRAUDE %s: %d intentos en %v", req.TxId, motivoCardTesting, res.Intentos, mandatoria.VentanaVelocidad)
		return rechazar(motivoCardTesting, 0)
	}

	// 3. Viaje imposible.
	if res.HayPrevia && res.Kmh > mandatoria.VelocidadMaxKmh {
		log.Printf("tx_id=%s FRAUDE %s: %.0f km/h", req.TxId, motivoViajeImposible, res.Kmh)
		return rechazar(motivoViajeImposible, 0)
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
			return rechazar(motivoRiesgoAlto, riskScore)
		}
	}

	// Pasó todos los chequeos: recién ahora esta posición pasa a ser
	// confiable (viaje 2 a Redis). Va DESPUÉS de la opcional: un rechazo por
	// riesgo tampoco puede mover la tarjeta. Si no se puede guardar, no se
	// aprueba: la próxima transacción de esta tarjeta se compararía contra
	// una posición vieja y el chequeo de viaje imposible quedaría debilitado.
	//
	// Va ANTES de Settlement: la posición ya es confiable (pasó los
	// chequeos de fraude) aunque Settlement falle por infraestructura. Al
	// revés sería peor: Settlement comprometería la transacción y, si
	// después fallara este SET, le diríamos "error" al cliente sobre una
	// transacción ya comprometida.
	if err := v.ActualizarPosicion(ctx, req.CardToken, req.Latitude, req.Longitude, ahora); err != nil {
		return nil, fallaDeVerificacion(req.TxId, err)
	}

	log.Printf("tx_id=%s card=%s amount=%.2f score=%.3f degradada=%v -> APROBADA por el motor",
		req.TxId, req.CardToken, req.Amount, riskScore, degradada)

	return s.liquidar(ctx, req, &pb.EvaluationResponse{
		TxId:       req.TxId,
		IsFraud:    false,
		RiskScore:  riskScore,
		IsDegraded: degradada,
	}, start)
}

// liquidar manda la decisión a Settlement y arma la respuesta final.
//
// La llamada tiene su propio timeout: ReservaSettleNet (C_settle + C_net),
// lo que el slack ya le reserva. context.WithTimeout NUNCA alarga el
// deadline: si al ctx de la request le queda menos, gana el que vence
// primero. Así no nos quedamos esperando si Settlement se cuelga, y el
// deadline del cliente se sigue respetando.
//
// Política ante una falla de Settlement:
//   - Aprobada: fail-closed. Sin commit confirmado no se responde "aprobada":
//     error gRPC.
//   - Rechazada: la decisión de rechazar ya es la segura. Se responde igual
//     el rechazo y se deja en el log que se perdió el registro de auditoría.
func (s *fraudEngineServer) liquidar(ctx context.Context, req *pb.TransactionRequest, eval *pb.EvaluationResponse, start time.Time) (*pb.EvaluationResponse, error) {
	ctxSettle, cancel := context.WithTimeout(ctx, presupuesto.ReservaSettleNet)
	defer cancel()

	var trailer metadata.MD
	t0 := time.Now()
	sr, err := s.settle.CommitTransaction(ctxSettle, &pb.SettlementRequest{
		TxId:                req.TxId,
		UserId:              req.UserId,
		Amount:              req.Amount,
		Evaluation:          eval,
		EmissionTimestampNs: req.EmissionTimestampNs,
	}, grpc.Trailer(&trailer))
	total := time.Since(t0)

	// Medición de C_settle y C_net (ver internal/settlement).
	if cSettle, ok := leerCSettle(trailer); ok {
		log.Printf("tx_id=%s settlement: total=%v c_settle=%v c_net=%v", req.TxId, total, cSettle, total-cSettle)
	} else {
		log.Printf("tx_id=%s settlement: total=%v (sin trailer)", req.TxId, total)
	}

	eval.ProcessingTimeMs = time.Since(start).Milliseconds()

	if err != nil {
		if eval.IsFraud {
			log.Printf("tx_id=%s REGISTRO PERDIDO (rechazo respondido igual): %v", req.TxId, err)
			return eval, nil
		}
		return nil, fallaDeSettlement(req.TxId, err)
	}

	// Manda lo REGISTRADO, no la evaluación de este intento: en un reintento
	// (mismo tx_id) Settlement devuelve el primer resultado final, que puede
	// diferir si algo cambió entre medio (por ejemplo, la tarjeta entró en
	// la blacklist). Si la plata ya se comprometió, decirle "fraude" al
	// cliente sería mentirle.
	switch sr.Status {
	case pb.TxStatus_STATUS_COMMITTED:
		if eval.IsFraud {
			log.Printf("tx_id=%s REINTENTO: ya estaba COMPROMETIDA (esta evaluación daba %s)", req.TxId, eval.RejectionReason)
			eval.IsFraud, eval.RejectionReason = false, ""
		}
		log.Printf("tx_id=%s COMPROMETIDA latencia_e2e=%dms", req.TxId, sr.EndToEndLatencyMs)
		return eval, nil
	case pb.TxStatus_STATUS_REJECTED_FRAUD:
		if !eval.IsFraud {
			log.Printf("tx_id=%s REINTENTO: ya estaba RECHAZADA por %s", req.TxId, sr.Message)
			eval.IsFraud, eval.RejectionReason = true, sr.Message
		}
		return eval, nil
	case pb.TxStatus_STATUS_ABORTED_TIMEOUT:
		// Settlement vio que se pasó el deadline global: no comprometió.
		log.Printf("tx_id=%s ABORTADA por Settlement: latencia_e2e=%dms", req.TxId, sr.EndToEndLatencyMs)
		if eval.IsFraud {
			return eval, nil // el rechazo vale igual
		}
		return nil, status.Error(codes.DeadlineExceeded, "la transacción superó el deadline global y no se comprometió")
	default:
		return nil, status.Errorf(codes.Internal, "estado inesperado de Settlement: %v", sr.Status)
	}
}

// leerCSettle saca C_settle del trailer de Settlement. ok es false si no
// vino (por ejemplo, si la llamada ni siquiera llegó a Settlement).
func leerCSettle(trailer metadata.MD) (time.Duration, bool) {
	v := trailer.Get(settlement.TrailerSettleUs)
	if len(v) != 1 {
		return 0, false
	}
	us, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(us) * time.Microsecond, true
}

// fallaDeSettlement traduce el error de la llamada a Settlement. Ojo: un
// error de una llamada gRPC es un *status*, no el error de context
// original, así que errors.Is(err, context.DeadlineExceeded) NO funciona:
// hay que mirar status.Code(err).
func fallaDeSettlement(txID string, err error) error {
	log.Printf("tx_id=%s SIN COMMIT: %v", txID, err)
	if status.Code(err) == codes.DeadlineExceeded {
		return status.Error(codes.DeadlineExceeded, "se agotó el tiempo esperando a Settlement")
	}
	return status.Error(codes.Unavailable, "no se pudo comprometer la transacción")
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
	// ctx y no reintenta; ver redisclient.Nuevo).
	rdb := redisclient.Nuevo(redisAddr)
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

	// Cliente de Settlement. NewClient no conecta todavía; Connect() arranca
	// la conexión en segundo plano para que la primera transacción no pague
	// el costo de conectar (cold start). Si Settlement no está, no impide
	// arrancar: cada transacción que lo necesite falla rápido (fail-closed).
	connSettle, err := grpc.NewClient(settlementAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("cliente de Settlement: %v", err)
	}
	defer connSettle.Close()
	connSettle.Connect()

	grpcServer := grpc.NewServer()
	// La blacklist ya no está en el código: es el SET drts:blacklist en
	// Redis (se carga con SADD, ver docs/GUIA-GO.md).
	verificador := mandatoria.NuevoVerificador(rdb, "drts:")
	pb.RegisterFraudEngineServer(grpcServer, &fraudEngineServer{
		verificador: verificador,
		settle:      pb.NewSettlementClient(connSettle),
	})

	log.Printf("fraud-engine escuchando en %s", addr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("error sirviendo: %v", err)
	}
}
