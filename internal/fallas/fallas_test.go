package fallas

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNuevaYMotivo(t *testing.T) {
	err := Nueva(codes.DeadlineExceeded, EarlyDrop, "deadline insuficiente")

	// El código gRPC sigue diciendo la CLASE de error...
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("código = %v", status.Code(err))
	}
	// ...y el motivo dice CUÁL.
	if got := Motivo(err); got != EarlyDrop {
		t.Errorf("Motivo = %q, se esperaba %q", got, EarlyDrop)
	}
}

func TestMotivoSinDetalle(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
	}{
		// Lo que ve el cliente cuando vence SU propio timer: sin detalle.
		{"timeout del cliente", status.Error(codes.DeadlineExceeded, "context deadline exceeded")},
		{"error que no es de gRPC", errors.New("cualquier cosa")},
		{"error de context", context.DeadlineExceeded},
		{"sin error", nil},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Motivo(c.err); got != "" {
				t.Errorf("Motivo = %q, se esperaba \"\"", got)
			}
		})
	}
}
