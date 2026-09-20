package main

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "fd_rts/proto"
)

// TestControlDeAdmision llama al handler directamente (sin red) con distintos
// deadlines y verifica el código gRPC que devuelve.
//
// Es un "table-driven test": una tabla de casos y un solo bucle que los
// corre. Es la forma idiomática de testear en Go — agregar un caso es agregar
// una fila.
func TestControlDeAdmision(t *testing.T) {
	casos := []struct {
		nombre string
		// timeout == 0 significa "sin deadline" (context.Background).
		timeout time.Duration
		want    codes.Code
	}{
		{"sin deadline", 0, codes.InvalidArgument},
		{"5ms, muy por debajo del mínimo", 5 * time.Millisecond, codes.DeadlineExceeded},
		{"30ms, justo debajo del mínimo", 30 * time.Millisecond, codes.DeadlineExceeded},
		{"200ms, el caso normal", 200 * time.Millisecond, codes.OK},
	}

	s := &fraudEngineServer{}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ctx := context.Background()
			if c.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.timeout)
				defer cancel()
			}

			start := time.Now()
			_, err := s.EvaluateTransaction(ctx, &pb.TransactionRequest{TxId: "t1"})
			elapsed := time.Since(start)

			// status.Code(nil) devuelve codes.OK, así que sirve también
			// para el caso sin error.
			if got := status.Code(err); got != c.want {
				t.Errorf("código = %v, se esperaba %v (err=%v)", got, c.want, err)
			}

			// Un rechazo por admisión tiene que ser casi instantáneo: si
			// tardara, no estaríamos ahorrando nada (load shedding).
			if c.want != codes.OK && elapsed > 5*time.Millisecond {
				t.Errorf("el rechazo tardó %v, se esperaba casi instantáneo", elapsed)
			}
		})
	}
}
