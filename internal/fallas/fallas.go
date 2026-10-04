// Package fallas define los motivos de error del sistema y cómo viajan.
//
// Problema que resuelve: varias situaciones distintas llegan al cliente con
// el MISMO código gRPC. Un DeadlineExceeded puede ser un early drop (la
// admisión no dejó pasar la transacción), un timeout esperando a Settlement
// (resultado ambiguo: pudo haberse comprometido) o el propio timer del
// cliente. El cliente necesita distinguirlas sin parsear el texto del
// mensaje, que es frágil.
//
// Solución: el modelo de errores enriquecidos estándar de gRPC. El error
// lleva adjunto un detalle google.rpc.ErrorInfo con un Reason legible por
// máquina (el mismo mecanismo que usan las APIs de Google). El código gRPC
// sigue diciendo QUÉ CLASE de error es; el Reason dice CUÁL.
package fallas

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Dominio identifica de qué sistema viene el motivo (campo Domain de
// ErrorInfo).
const Dominio = "drts"

// Motivos. Son parte del contrato con los clientes: no cambiarlos a la
// ligera.
const (
	// Admisión (Etapa 1).
	SinDeadline = "SIN_DEADLINE"
	EarlyDrop   = "EARLY_DROP"

	// Request mal formada (validaciones).
	RequestInvalida      = "REQUEST_INVALIDA"
	CategoriaDesconocida = "CATEGORIA_DESCONOCIDA"

	// Redis en la fase mandatoria: no se pudo verificar.
	RedisTimeout      = "REDIS_TIMEOUT"
	RedisNoDisponible = "REDIS_NO_DISPONIBLE"

	// Settlement.
	// SettlementTimeout es AMBIGUO: la transacción pudo haberse comprometido
	// igual (Etapa 5). El cliente reintenta con el mismo tx_id.
	SettlementTimeout      = "SETTLEMENT_TIMEOUT"
	SettlementNoDisponible = "SETTLEMENT_NO_DISPONIBLE"
	AbortadaPorTimeout     = "ABORTADA_POR_TIMEOUT"
	EstadoInesperado       = "ESTADO_INESPERADO"
)

// Nueva arma un error gRPC con código, motivo (ErrorInfo.Reason) y mensaje
// para humanos.
func Nueva(codigo codes.Code, motivo, mensaje string) error {
	st := status.New(codigo, mensaje)
	conDetalle, err := st.WithDetails(&errdetails.ErrorInfo{Reason: motivo, Domain: Dominio})
	if err != nil {
		// Solo falla si el detalle no se puede serializar: no debería pasar.
		// Mejor devolver el error sin detalle que perderlo.
		return st.Err()
	}
	return conDetalle.Err()
}

// Motivo devuelve el Reason del ErrorInfo de dominio drts que trae err, o ""
// si no trae ninguno (por ejemplo, un DeadlineExceeded generado por el
// propio timer del cliente, que nunca pasó por el servidor).
func Motivo(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == Dominio {
			return info.Reason
		}
	}
	return ""
}
