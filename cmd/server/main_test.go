package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"fd_rts/internal/mandatoria"
	"fd_rts/internal/opcional"
	pb "fd_rts/proto"
)

// Coordenadas de referencia para los tests.
const (
	baLat, baLon       = -34.6037, -58.3816
	moscuLat, moscuLon = 55.7558, 37.6173
)

// catPrueba tiene estadísticas cargadas en nuevoServerDePrueba: mediana de
// $100 (media_log = ln 100), desvío 1 en log, riesgo promedio (peso 1).
// Así, monto = 100·e^z da exactamente ese z, y $100 da z = 0 (score 0).
const catPrueba = "test_cat"

// montoConZ devuelve el monto que en catPrueba da exactamente ese z.
func montoConZ(z float64) float64 { return 100 * math.Exp(z) }

// txValida arma una request completa: con tarjeta, coordenadas, categoría
// conocida y un monto típico ($100, z = 0).
func txValida(txID, card string, lat, lon float64) *pb.TransactionRequest {
	return &pb.TransactionRequest{TxId: txID, CardToken: card, Latitude: lat, Longitude: lon, Amount: 100, MerchantCategory: catPrueba}
}

// nuevoServerDePrueba arma el server contra el Redis REAL, con un prefijo de
// claves único para que cada test arranque sin estado previo y no pise los
// datos del server ("drts:"). Carga "tok-robada" en la blacklist y borra
// todas sus claves al terminar.
//
// Si Redis no está levantado, el test FALLA (no se saltea).
func nuevoServerDePrueba(t *testing.T) *fraudEngineServer {
	t.Helper()
	rdb := mandatoria.NuevoClienteRedis(redisAddr)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		t.Fatalf("Redis no disponible en %s (levantalo con: docker start redis-drts): %v", redisAddr, err)
	}

	prefijo := fmt.Sprintf("test:%d:%d:", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		ctx := context.Background()
		iter := rdb.Scan(ctx, 0, prefijo+"*", 100).Iterator()
		for iter.Next(ctx) {
			rdb.Del(ctx, iter.Val())
		}
		rdb.Close()
	})

	v := mandatoria.NuevoVerificador(rdb, prefijo)
	if err := v.Bloquear(ctx, "tok-robada"); err != nil {
		t.Fatalf("Bloquear: %v", err)
	}
	err := v.GuardarEstadisticas(ctx, catPrueba, map[string]any{
		opcional.CampoMediaLog: math.Log(100), opcional.CampoDesvioLog: 1.0, opcional.CampoPeso: 1.0,
	})
	if err != nil {
		t.Fatalf("GuardarEstadisticas: %v", err)
	}
	return &fraudEngineServer{verificador: v}
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

	s := nuevoServerDePrueba(t)

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
	return evaluarCon(t, s, req, 200*time.Millisecond)
}

// evaluarCon es evaluar con un deadline a elección.
func evaluarCon(t *testing.T, s *fraudEngineServer, req *pb.TransactionRequest, timeout time.Duration) (*pb.EvaluationResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.EvaluateTransaction(ctx, req)
}

func TestRequestInvalida(t *testing.T) {
	casos := []struct {
		nombre string
		req    *pb.TransactionRequest
	}{
		{"sin tx_id", txValida("", "tok-ok", baLat, baLon)},
		{"sin card_token", txValida("t1", "", baLat, baLon)},
		{"sin coordenadas (0, 0)", txValida("t1", "tok-ok", 0, 0)},
		{"latitud fuera de rango", txValida("t1", "tok-ok", 95, baLon)},
		{"monto 0", conMonto(txValida("t1", "tok-ok", baLat, baLon), 0)},
		{"monto negativo", conMonto(txValida("t1", "tok-ok", baLat, baLon), -10)},
		{"monto NaN", conMonto(txValida("t1", "tok-ok", baLat, baLon), math.NaN())},
		{"sin categoría", conCategoria(txValida("t1", "tok-ok", baLat, baLon), "")},
		// Esta se detecta recién después del viaje 1 a Redis.
		{"categoría desconocida", conCategoria(txValida("t1", "tok-ok", baLat, baLon), "no_existe")},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			_, err := evaluar(t, nuevoServerDePrueba(t), c.req)
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
			s := nuevoServerDePrueba(t)
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

// Redis caído en el hot path: fail-closed. No se aprueba (ni se marca como
// fraude): el motor no pudo decidir y lo dice con un error gRPC.
func TestRedisCaidoEsFailClosed(t *testing.T) {
	// Puerto donde no hay nadie escuchando: simula Redis caído.
	rdb := mandatoria.NuevoClienteRedis("localhost:1")
	defer rdb.Close()
	s := &fraudEngineServer{verificador: mandatoria.NuevoVerificador(rdb, "test:")}

	inicio := time.Now()
	resp, err := evaluar(t, s, txValida("t1", "tok-ok", baLat, baLon))
	tardo := time.Since(inicio)

	if resp != nil {
		t.Errorf("se esperaba sin respuesta, llegó %+v", resp)
	}
	// Sin reintentos, Redis caído es Unavailable, no DeadlineExceeded: se
	// rechaza al instante en vez de esperar a que venza el deadline.
	if got := status.Code(err); got != codes.Unavailable {
		t.Errorf("código = %v, se esperaba Unavailable (err=%v)", got, err)
	}
	// "Rápido" relativo al deadline: un décimo de los 200ms.
	if tardo > 20*time.Millisecond {
		t.Errorf("tardó %v en fallar: se esperaba fallo rápido, no quemar el deadline", tardo)
	}
}

// conMonto devuelve la misma request con otro monto.
func conMonto(req *pb.TransactionRequest, monto float64) *pb.TransactionRequest {
	req.Amount = monto
	return req
}

// conCategoria devuelve la misma request con otra categoría.
func conCategoria(req *pb.TransactionRequest, cat string) *pb.TransactionRequest {
	req.MerchantCategory = cat
	return req
}

// La fase opcional corre o no según el slack, que depende del deadline con
// el que llega la request. En catPrueba: $100 → z = 0 → score 0, y
// 100·e^4 → z = 4 → score 1 − 0.25^(16/9) ≈ 0.915 (> 0.75).
func TestFaseOpcional(t *testing.T) {
	casos := []struct {
		nombre       string
		timeout      time.Duration
		monto        float64
		wantFraude   bool
		wantMotivo   string
		wantDegradad bool
		wantScore    float64
	}{
		{"200ms, monto típico: corre la opcional y aprueba", 200 * time.Millisecond, montoConZ(0), false, "", false, 0},
		{"200ms, monto anormalmente alto: RIESGO_ALTO", 200 * time.Millisecond, montoConZ(4), true, motivoRiesgoAlto, false, 1 - math.Pow(0.25, 16.0/9)},
		// z² mira para los dos lados: card testing con cargas chiquitas.
		{"200ms, monto anormalmente bajo: RIESGO_ALTO", 200 * time.Millisecond, montoConZ(-4), true, motivoRiesgoAlto, false, 1 - math.Pow(0.25, 16.0/9)},
		// D_rem ≈ 99ms tras la mandatoria: slack ≈ 4ms ≥ 0, entra justo.
		{"100ms: slack positivo, corre", 100 * time.Millisecond, montoConZ(0), false, "", false, 0},
		// Pasa la admisión (≥ 35ms) pero no hay slack para la opcional.
		// Monto anormal y aprobada igual: degradar es aceptar ese riesgo a
		// cambio de cumplir el deadline.
		{"60ms, monto anormalmente alto: degradada y aprobada", 60 * time.Millisecond, montoConZ(4), false, "", true, 0},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			s := nuevoServerDePrueba(t)
			inicio := time.Now()
			resp, err := evaluarCon(t, s, conMonto(txValida("t1", "tok-ok", baLat, baLon), c.monto), c.timeout)
			tardo := time.Since(inicio)
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if resp.IsFraud != c.wantFraude || resp.RejectionReason != c.wantMotivo || resp.IsDegraded != c.wantDegradad {
				t.Errorf("is_fraud=%v motivo=%q degradada=%v, se esperaba is_fraud=%v motivo=%q degradada=%v",
					resp.IsFraud, resp.RejectionReason, resp.IsDegraded, c.wantFraude, c.wantMotivo, c.wantDegradad)
			}
			if math.Abs(resp.RiskScore-c.wantScore) > 1e-9 {
				t.Errorf("risk_score = %v, se esperaba %v", resp.RiskScore, c.wantScore)
			}
			// El punto de todo el mecanismo: responder antes del deadline.
			if tardo > c.timeout {
				t.Errorf("tardó %v, más que el deadline de %v", tardo, c.timeout)
			}
		})
	}
}

// Un rechazo por riesgo alto tampoco mueve la tarjeta: si la posición se
// guardara antes de la opcional, la compra siguiente desde Moscú se
// compararía contra BA y daría viaje imposible.
func TestRechazoPorRiesgoNoMueveLaTarjeta(t *testing.T) {
	s := nuevoServerDePrueba(t)

	r1, err := evaluar(t, s, conMonto(txValida("t1", "tok-ok", baLat, baLon), montoConZ(4)))
	if err != nil || r1.RejectionReason != motivoRiesgoAlto {
		t.Fatalf("t1: se esperaba RIESGO_ALTO, llegó %+v (err=%v)", r1, err)
	}

	// Sin posición confiable previa, Moscú no tiene con qué compararse.
	r2, err := evaluar(t, s, txValida("t2", "tok-ok", moscuLat, moscuLon))
	if err != nil {
		t.Fatalf("t2: error inesperado: %v", err)
	}
	if r2.IsFraud {
		t.Errorf("t2: rechazada por %q: la posición del rechazo por riesgo se guardó", r2.RejectionReason)
	}
}
