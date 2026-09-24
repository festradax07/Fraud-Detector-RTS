package main

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"fd_rts/internal/mandatoria"
	pb "fd_rts/proto"
)

// Coordenadas de referencia para los tests.
const (
	baLat, baLon       = -34.6037, -58.3816
	moscuLat, moscuLon = 55.7558, 37.6173
)

// txValida arma una request completa: con tarjeta y coordenadas.
func txValida(txID, card string, lat, lon float64) *pb.TransactionRequest {
	return &pb.TransactionRequest{TxId: txID, CardToken: card, Latitude: lat, Longitude: lon, Amount: 100}
}

// nuevoServerDePrueba arma el server con un Verificador propio, para que
// cada test arranque sin estado previo.
func nuevoServerDePrueba() *fraudEngineServer {
	return &fraudEngineServer{verificador: mandatoria.NuevoVerificador([]string{"tok-robada"})}
}

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

	s := nuevoServerDePrueba()

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ctx := context.Background()
			if c.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.timeout)
				defer cancel()
			}

			start := time.Now()
			_, err := s.EvaluateTransaction(ctx, txValida("t1", "tok-ok", baLat, baLon))
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

// evaluar llama al handler con el deadline normal de 200ms.
func evaluar(t *testing.T, s *fraudEngineServer, req *pb.TransactionRequest) (*pb.EvaluationResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	return s.EvaluateTransaction(ctx, req)
}

func TestRequestInvalida(t *testing.T) {
	casos := []struct {
		nombre string
		req    *pb.TransactionRequest
	}{
		{"sin card_token", txValida("t1", "", baLat, baLon)},
		{"sin coordenadas (0, 0)", txValida("t1", "tok-ok", 0, 0)},
		{"latitud fuera de rango", txValida("t1", "tok-ok", 95, baLon)},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			_, err := evaluar(t, nuevoServerDePrueba(), c.req)
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("código = %v, se esperaba InvalidArgument (err=%v)", got, err)
			}
		})
	}
}

// Cada escenario es una secuencia de transacciones sobre el MISMO server, y
// se verifica la decisión de cada una. Un fraude es código OK + is_fraud.
func TestFaseMandatoria(t *testing.T) {
	type paso struct {
		req        *pb.TransactionRequest
		wantMotivo string // "" = aprobada
	}
	escenarios := []struct {
		nombre string
		pasos  []paso
	}{
		{"compra normal: aprobada", []paso{
			{txValida("t1", "tok-ok", baLat, baLon), ""},
		}},
		{"tarjeta en blacklist", []paso{
			{txValida("t1", "tok-robada", baLat, baLon), motivoBlacklist},
		}},
		{"card testing: el 4.º intento se rechaza", []paso{
			{txValida("t1", "tok-ok", baLat, baLon), ""},
			{txValida("t2", "tok-ok", baLat, baLon), ""},
			{txValida("t3", "tok-ok", baLat, baLon), ""},
			{txValida("t4", "tok-ok", baLat, baLon), motivoCardTesting},
		}},
		{"viaje imposible: BA y al instante Moscú", []paso{
			{txValida("t1", "tok-ok", baLat, baLon), ""},
			{txValida("t2", "tok-ok", moscuLat, moscuLon), motivoViajeImposible},
		}},
		// El rechazo desde Moscú no mueve la tarjeta: la vuelta a BA se
		// compara contra BA (0 km/h) y se aprueba.
		{"un rechazo no mueve la tarjeta", []paso{
			{txValida("t1", "tok-ok", baLat, baLon), ""},
			{txValida("t2", "tok-ok", moscuLat, moscuLon), motivoViajeImposible},
			{txValida("t3", "tok-ok", baLat, baLon), ""},
		}},
		// Los intentos rechazados también cuentan para la velocidad.
		{"los rechazos suman intentos", []paso{
			{txValida("t1", "tok-ok", baLat, baLon), ""},
			{txValida("t2", "tok-ok", moscuLat, moscuLon), motivoViajeImposible},
			{txValida("t3", "tok-ok", moscuLat, moscuLon), motivoViajeImposible},
			{txValida("t4", "tok-ok", baLat, baLon), motivoCardTesting},
		}},
	}

	for _, e := range escenarios {
		t.Run(e.nombre, func(t *testing.T) {
			s := nuevoServerDePrueba()
			for i, p := range e.pasos {
				resp, err := evaluar(t, s, p.req)
				if err != nil {
					t.Fatalf("paso %d: error inesperado: %v", i, err)
				}
				wantFraude := p.wantMotivo != ""
				if resp.IsFraud != wantFraude || resp.RejectionReason != p.wantMotivo {
					t.Errorf("paso %d (%s): is_fraud=%v motivo=%q, se esperaba is_fraud=%v motivo=%q",
						i, p.req.TxId, resp.IsFraud, resp.RejectionReason, wantFraude, p.wantMotivo)
				}
			}
		})
	}
}
