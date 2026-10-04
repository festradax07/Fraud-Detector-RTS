// Package settlement implementa el servicio Settlement: segundo servicio
// del hot path. Recibe la decisión del Fraud-Engine, registra el resultado
// (commit / rechazo / aborto por timeout) y devuelve la latencia de extremo
// a extremo. cmd/settlement solo lo conecta a la red.
//
// Vive en internal/ y no en cmd/settlement porque un paquete main no se
// puede importar: los tests del Fraud-Engine levantan este mismo servidor,
// real, en un puerto TCP real.
//
// Por ahora el registro va directo a Redis. En la Etapa 8 se suma el evento
// de auditoría asíncrono (Kafka, cold path).
package settlement

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"fd_rts/internal/presupuesto"
	pb "fd_rts/proto"
)

// TrailerSettleUs es la clave del trailer gRPC donde Settlement informa su
// tiempo interno de procesamiento (C_settle), en microsegundos. Con eso el
// Fraud-Engine separa el costo de la llamada en C_settle y C_net:
//
//	C_net = tiempo total de la llamada - C_settle
//
// Va en metadata (no en el .proto) para no cambiar el contrato. Es un
// TRAILER y no un header: el header viaja antes del cuerpo y el trailer
// después, y este dato se conoce recién al terminar. Además el trailer
// viaja también cuando la respuesta es un error.
//
// Las claves de metadata van en minúscula: HTTP/2 las normaliza así.
const TrailerSettleUs = "x-settle-us"

// NuevoServidor arma el servicio sobre un cliente de Redis. prefijo separa
// las claves ("drts:" en el servicio, uno único por test).
func NuevoServidor(rdb *redis.Client, prefijo string) *Servidor {
	return &Servidor{rdb: rdb, prefijo: prefijo}
}

// Servidor implementa el servicio Settlement del .proto.
type Servidor struct {
	pb.UnimplementedSettlementServer
	rdb *redis.Client
	// prefijo de claves: "drts:" en el servicio, uno único por test.
	prefijo string
}

// ClaveRegistro es la clave de Redis donde queda el registro de txID.
func (s *Servidor) ClaveRegistro(txID string) string {
	return s.prefijo + "settle:" + txID
}

// CommitTransaction registra el resultado de la transacción y devuelve la
// latencia de extremo a extremo. Informa su tiempo interno en el trailer
// TrailerSettleUs.
func (s *Servidor) CommitTransaction(ctx context.Context, req *pb.SettlementRequest) (*pb.SettlementResponse, error) {
	inicio := time.Now()

	// C_settle: al salir (por cualquier return, también los de error), se
	// informa el tiempo interno en el trailer. El defer corre antes de que
	// gRPC mande la respuesta, así que el trailer llega a tiempo.
	defer func() {
		// Si el cliente ya se fue (venció su timeout o canceló), no hay a
		// quién mandarle el trailer: gRPC devolvería "SendHeader called
		// multiple times". Ojo: que el cliente se haya ido NO deshace lo que
		// ya se hizo acá (ver "ambigüedad del timeout" en GUIA-GO).
		if ctx.Err() != nil {
			return
		}
		us := strconv.FormatInt(time.Since(inicio).Microseconds(), 10)
		if err := grpc.SetTrailer(ctx, metadata.Pairs(TrailerSettleUs, us)); err != nil {
			log.Printf("tx_id=%s no pude poner el trailer: %v", req.TxId, err)
		}
	}()

	if req.TxId == "" {
		return nil, status.Error(codes.InvalidArgument, "falta tx_id")
	}
	if req.Evaluation == nil {
		return nil, status.Error(codes.InvalidArgument, "falta evaluation")
	}

	estado := pb.TxStatus_STATUS_COMMITTED
	if req.Evaluation.IsFraud {
		estado = pb.TxStatus_STATUS_REJECTED_FRAUD
	}

	// Latencia de extremo a extremo: desde que el cliente emitió la
	// transacción hasta ahora. Compara relojes de dos procesos: vale porque
	// todo corre en la misma máquina (single-host, ver CLAUDE.md). Con
	// varios hosts haría falta sincronizar relojes.
	var latencia time.Duration
	if req.EmissionTimestampNs > 0 {
		latencia = inicio.Sub(time.Unix(0, req.EmissionTimestampNs))
		// Tiempo real duro: una respuesta correcta que llega tarde es una
		// falla. Si ya se pasó el deadline global, NO se compromete nada.
		if latencia > presupuesto.DeadlineGlobal {
			estado = pb.TxStatus_STATUS_ABORTED_TIMEOUT
		}
	}

	// Registro en Redis, con el script registrarUnaVez: si el tx_id ya tiene
	// un resultado final, gana ese (idempotencia de verdad).
	//
	// Sin TTL: es el registro de la transacción. En la Etapa 8 la auditoría
	// pasa a Kafka y se puede revisar si Redis tiene que guardarlo.
	campos := []any{
		"estado", estado.String(),
		"user_id", req.UserId,
		"amount", req.Amount,
		"is_fraud", req.Evaluation.IsFraud,
		"rejection_reason", req.Evaluation.RejectionReason,
		"risk_score", req.Evaluation.RiskScore,
		"is_degraded", req.Evaluation.IsDegraded,
		"latencia_us", latencia.Microseconds(),
		"registrado_ns", inicio.UnixNano(),
	}
	res, err := registrarUnaVez.Run(ctx, s.rdb, []string{s.ClaveRegistro(req.TxId)}, campos...).StringSlice()
	if err == nil && len(res) != 2 {
		err = fmt.Errorf("respuesta inesperada del script: %v", res)
	}
	if err != nil {
		log.Printf("tx_id=%s SIN REGISTRAR: %v", req.TxId, err)
		// Sin registro no hay commit: fail-closed.
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, "se agotó el deadline registrando la transacción")
		}
		return nil, status.Error(codes.Unavailable, "no se pudo registrar la transacción")
	}

	// Lo que quedó registrado (puede ser el resultado de un intento anterior).
	registrado := pb.TxStatus(pb.TxStatus_value[res[0]])
	motivo := res[1]
	if registrado != estado {
		log.Printf("tx_id=%s REINTENTO: se mantiene %s (esta evaluación daba %s)", req.TxId, registrado, estado)
	}

	log.Printf("tx_id=%s %s latencia_e2e=%v", req.TxId, registrado, latencia)
	// Message: para un rechazo, el motivo registrado (el motor lo necesita si
	// un reintento se evaluó distinto); si no, el nombre del estado.
	mensaje := registrado.String()
	if registrado == pb.TxStatus_STATUS_REJECTED_FRAUD {
		mensaje = motivo
	}
	return &pb.SettlementResponse{
		TxId:              req.TxId,
		Status:            registrado,
		EndToEndLatencyMs: latencia.Milliseconds(),
		Message:           mensaje,
	}, nil
}

// registrarUnaVez escribe el registro de una transacción salvo que ya tenga
// un resultado FINAL (COMMITTED o REJECTED_FRAUD): en ese caso gana el
// primero. ABORTED_TIMEOUT no es final (no se comprometió nada), así que un
// reintento a tiempo puede reemplazarlo. Devuelve {estado, rejection_reason}
// de lo que quedó registrado.
//
// Por qué un script y no HGET + HSET desde Go: "fijarse y después escribir"
// en dos comandos deja una ventana donde dos reintentos simultáneos se
// cuelan entre medio. Redis ejecuta un script Lua entero sin intercalar
// comandos de otros clientes: es atómico, y es un solo viaje de red.
//
// En Lua, HGET de un campo inexistente devuelve false (no nil).
var registrarUnaVez = redis.NewScript(fmt.Sprintf(`
local actual = redis.call('HGET', KEYS[1], 'estado')
if actual ~= %q and actual ~= %q then
  redis.call('HSET', KEYS[1], unpack(ARGV))
end
return {redis.call('HGET', KEYS[1], 'estado'), redis.call('HGET', KEYS[1], 'rejection_reason') or ''}
`, pb.TxStatus_STATUS_COMMITTED.String(), pb.TxStatus_STATUS_REJECTED_FRAUD.String()))
