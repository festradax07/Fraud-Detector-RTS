package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"fd_rts/internal/mandatoria"
	"fd_rts/internal/opcional"
	"fd_rts/internal/presupuesto"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/settlement"
	pb "fd_rts/proto"
)

// levantarSettlement pone a escuchar srv en un puerto TCP real
// (localhost:0: el sistema operativo elige uno libre) y devuelve un cliente
// gRPC real apuntándole. Se apaga solo al terminar el test.
func levantarSettlement(t *testing.T, srv pb.SettlementServer) pb.SettlementClient {
	t.Helper()
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	pb.RegisterSettlementServer(g, srv)
	go g.Serve(lis) // Serve bloquea: va en su propia goroutine

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("cliente de Settlement: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		g.Stop()
	})
	return pb.NewSettlementClient(conn)
}

// settlementLento es un Settlement real que tarda demora antes de atender:
// simula un Settlement sobrecargado o colgado.
type settlementLento struct {
	*settlement.Servidor
	demora time.Duration
}

func (s settlementLento) CommitTransaction(ctx context.Context, req *pb.SettlementRequest) (*pb.SettlementResponse, error) {
	select {
	case <-time.After(s.demora):
	case <-ctx.Done(): // si el cliente ya se fue, no seguir
		return nil, ctx.Err()
	}
	return s.Servidor.CommitTransaction(ctx, req)
}

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
	s, _, _ := nuevoServerConRedis(t)
	return s
}

// nuevoServerConRedis es nuevoServerDePrueba, pero devuelve también el
// cliente de Redis y el prefijo, para tests que arman su propio Settlement
// sobre los mismos datos.
func nuevoServerConRedis(t *testing.T) (*fraudEngineServer, *redis.Client, string) {
	t.Helper()
	rdb := redisclient.Nuevo(redisAddr)

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
	return &fraudEngineServer{verificador: v, settle: levantarSettlement(t, settlement.NuevoServidor(rdb, prefijo))}, rdb, prefijo
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
	rdb := redisclient.Nuevo("localhost:1")
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

// esperarEstado espera hasta que el canal gRPC llegue al estado want (o
// falla a 1s). Los estados de un canal: IDLE → CONNECTING → READY, o
// TRANSIENT_FAILURE si no pudo conectar (y reintenta con backoff).
func esperarEstado(t *testing.T, conn *grpc.ClientConn, want connectivity.State) {
	t.Helper()
	conn.Connect()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for st := conn.GetState(); st != want; st = conn.GetState() {
		// Bloquea hasta que el estado cambie (o venza ctx).
		if !conn.WaitForStateChange(ctx, st) {
			t.Fatalf("el canal no llegó a %v (quedó en %v)", want, st)
		}
	}
}

// estadoRegistrado lee de Redis el estado que Settlement registró para txID.
func estadoRegistrado(t *testing.T, s *fraudEngineServer, txID string) string {
	t.Helper()
	// El server de prueba y su Settlement comparten Redis y prefijo; el
	// prefijo se recupera de una clave conocida del Verificador.
	rdb := redisclient.Nuevo(redisAddr)
	defer rdb.Close()
	claves, err := rdb.Keys(context.Background(), "test:*:settle:"+txID).Result()
	if err != nil {
		t.Fatalf("buscando el registro: %v", err)
	}
	for _, c := range claves {
		if est, err := rdb.HGet(context.Background(), c, "estado").Result(); err == nil && s != nil {
			return est
		}
	}
	return ""
}

// Toda decisión pasa por Settlement y queda registrada.
func TestSettlementRegistraLaDecision(t *testing.T) {
	s := nuevoServerDePrueba(t)
	sufijo := fmt.Sprintf("%d", time.Now().UnixNano()) // tx_id únicos entre corridas

	if _, err := evaluar(t, s, txValida("ok-"+sufijo, "tok-ok", baLat, baLon)); err != nil {
		t.Fatalf("aprobada: %v", err)
	}
	if got := estadoRegistrado(t, s, "ok-"+sufijo); got != "STATUS_COMMITTED" {
		t.Errorf("aprobada: registrada como %q, se esperaba STATUS_COMMITTED", got)
	}

	if _, err := evaluar(t, s, txValida("bl-"+sufijo, "tok-robada", baLat, baLon)); err != nil {
		t.Fatalf("blacklist: %v", err)
	}
	if got := estadoRegistrado(t, s, "bl-"+sufijo); got != "STATUS_REJECTED_FRAUD" {
		t.Errorf("blacklist: registrada como %q, se esperaba STATUS_REJECTED_FRAUD", got)
	}
}

// Política ante fallas de Settlement: una aprobada sin commit es error
// (fail-closed); un rechazo se responde igual.
func TestSettlementFalla(t *testing.T) {
	casos := []struct {
		nombre string
		// settle arma el Settlement de este caso a partir del server de prueba.
		settle   func(t *testing.T, s *fraudEngineServer) pb.SettlementClient
		wantCode codes.Code // para la aprobada
		maxTardo time.Duration
	}{
		// Como en producción: el server hace Connect() al arrancar, así que
		// cuando llega la primera transacción el canal ya sabe que
		// Settlement está caído (TRANSIENT_FAILURE) y falla en µs. Un canal
		// recién creado, en cambio, espera el primer intento de conexión
		// (medido: ~9ms con "localhost", que pasa por DNS).
		{"Settlement caído", func(t *testing.T, _ *fraudEngineServer) pb.SettlementClient {
			conn, err := grpc.NewClient("localhost:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatalf("cliente: %v", err)
			}
			t.Cleanup(func() { conn.Close() })
			esperarEstado(t, conn, connectivity.TransientFailure)
			return pb.NewSettlementClient(conn)
		}, codes.Unavailable, 5 * time.Millisecond},
		// Tarda 100ms: el timeout propio (ReservaSettleNet = 15ms) corta
		// mucho antes que el deadline del cliente.
		{"Settlement lento", func(t *testing.T, s *fraudEngineServer) pb.SettlementClient {
			rdb := redisclient.Nuevo(redisAddr)
			t.Cleanup(func() { rdb.Close() })
			return levantarSettlement(t, settlementLento{settlement.NuevoServidor(rdb, "test:lento:"), 100 * time.Millisecond})
		}, codes.DeadlineExceeded, presupuesto.ReservaSettleNet + 10*time.Millisecond},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			s := nuevoServerDePrueba(t)
			s.settle = c.settle(t, s)

			// 90ms: pasa la admisión pero se degrada (sin slack), así el
			// scoring (~16ms) no ensucia la medición: queda casi solo lo que
			// se espera a Settlement.
			inicio := time.Now()
			_, err := evaluarCon(t, s, txValida("t1", "tok-ok", baLat, baLon), 90*time.Millisecond)
			tardo := time.Since(inicio)
			if got := status.Code(err); got != c.wantCode {
				t.Errorf("aprobada: código = %v, se esperaba %v (err=%v)", got, c.wantCode, err)
			}
			// Lo que se espera a Settlement está acotado: no se quema el
			// deadline del cliente.
			if tardo > c.maxTardo {
				t.Errorf("aprobada: tardó %v, se esperaba menos de %v", tardo, c.maxTardo)
			}

			resp, err := evaluar(t, s, txValida("t2", "tok-robada", baLat, baLon))
			if err != nil || !resp.IsFraud || resp.RejectionReason != motivoBlacklist {
				t.Errorf("rechazo: resp=%+v err=%v, se esperaba el rechazo igual", resp, err)
			}
		})
	}
}

// Una transacción que llega a Settlement después del deadline global no se
// compromete (STATUS_ABORTED_TIMEOUT), y al cliente no le llega "aprobada".
func TestTransaccionTardiaNoSeCompromete(t *testing.T) {
	s := nuevoServerDePrueba(t)
	req := txValida("t1", "tok-ok", baLat, baLon)
	// Emitida hace 300ms (por ejemplo, estuvo encolada en algún lado). El
	// ctx de esta llamada igual tiene 200ms: la admisión la deja pasar.
	req.EmissionTimestampNs = time.Now().Add(-300 * time.Millisecond).UnixNano()

	_, err := evaluar(t, s, req)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("código = %v, se esperaba DeadlineExceeded (err=%v)", status.Code(err), err)
	}
}

// settlementRespuestaPerdida es un Settlement real que COMPROMETE y después
// tarda en responder: reproduce la ambigüedad del timeout (el motor vence
// esperando, pero del otro lado ya se comprometió).
type settlementRespuestaPerdida struct {
	*settlement.Servidor
	demora time.Duration
}

func (s settlementRespuestaPerdida) CommitTransaction(ctx context.Context, req *pb.SettlementRequest) (*pb.SettlementResponse, error) {
	resp, err := s.Servidor.CommitTransaction(ctx, req)
	time.Sleep(s.demora) // la respuesta "se atrasa en la red"
	return resp, err
}

// La ambigüedad del timeout y cómo se resuelve: el cliente reintenta con el
// MISMO tx_id y el sistema converge al resultado real.
func TestAmbiguedadDelTimeout(t *testing.T) {
	s, rdb, prefijo := nuevoServerConRedis(t)
	normal := s.settle
	s.settle = levantarSettlement(t, settlementRespuestaPerdida{settlement.NuevoServidor(rdb, prefijo), 30 * time.Millisecond})

	// 1. Primer intento: el motor vence esperando la respuesta → error...
	_, err := evaluar(t, s, txValida("t1", "tok-ok", baLat, baLon))
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("primer intento: código = %v, se esperaba DeadlineExceeded", status.Code(err))
	}
	// ...pero del otro lado se comprometió.
	clave := prefijo + "settle:t1"
	if est, _ := rdb.HGet(context.Background(), clave, "estado").Result(); est != "STATUS_COMMITTED" {
		t.Fatalf("registro = %q, se esperaba STATUS_COMMITTED", est)
	}

	// 2. La tarjeta entra en la blacklist entre el intento y el reintento:
	// el reintento se va a evaluar como fraude.
	if err := s.verificador.Bloquear(context.Background(), "tok-ok"); err != nil {
		t.Fatalf("Bloquear: %v", err)
	}

	// 3. Reintento con el mismo tx_id: manda lo registrado. La plata ya se
	// comprometió, así que la respuesta es "aprobada".
	s.settle = normal
	resp, err := evaluar(t, s, txValida("t1", "tok-ok", baLat, baLon))
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if resp.IsFraud {
		t.Errorf("reintento: is_fraud=true (%s), se esperaba aprobada: ya estaba comprometida", resp.RejectionReason)
	}
	if est, _ := rdb.HGet(context.Background(), clave, "estado").Result(); est != "STATUS_COMMITTED" {
		t.Errorf("registro tras el reintento = %q, se esperaba STATUS_COMMITTED", est)
	}
}
