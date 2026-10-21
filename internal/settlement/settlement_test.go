package settlement

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"fd_rts/internal/fallas"
	"fd_rts/internal/pruebas"
	"fd_rts/internal/redisclient"
	pb "fd_rts/proto"
)

// entorno es un Settlement real escuchando en un puerto TCP real, contra el
// Redis real, con un cliente gRPC real apuntándole. Nada simulado.
type entorno struct {
	cliente pb.SettlementClient
	srv     *Servidor
}

// levantarSettlement arranca el servicio REAL sobre rdb/prefijo en un puerto
// TCP real, con un cliente gRPC real apuntándole (internal/pruebas).
func levantarSettlement(t *testing.T, rdb *redis.Client, prefijo string) *entorno {
	t.Helper()
	srv := NuevoServidor(rdb, prefijo, nil, 0)
	conn := pruebas.ServirGRPC(t, func(g *grpc.Server) { pb.RegisterSettlementServer(g, srv) })
	return &entorno{cliente: pb.NewSettlementClient(conn), srv: srv}
}

// settlementDePrueba: Settlement real sobre el Redis real, con prefijo único.
// Si Redis no está, el test FALLA.
func settlementDePrueba(t *testing.T) *entorno {
	t.Helper()
	rdb, prefijo := pruebas.Redis(t)
	return levantarSettlement(t, rdb, prefijo)
}

// commit llama a CommitTransaction por red y devuelve también el trailer.
func (e *entorno) commit(t *testing.T, req *pb.SettlementRequest) (*pb.SettlementResponse, metadata.MD, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var trailer metadata.MD
	// grpc.Trailer(&md) es una "call option": le pide a gRPC que deje el
	// trailer de la respuesta en md.
	resp, err := e.cliente.CommitTransaction(ctx, req, grpc.Trailer(&trailer))
	return resp, trailer, err
}

// sinEmision: la request no trae emission_timestamp_ns.
const sinEmision = time.Duration(-1)

// reqSettle arma una request emitida hace "hace" (o sin emisión). La hora se
// toma AL LLAMAR: en una tabla de casos hay que llamarla dentro del subtest,
// no al armar la tabla (ver intento).
func reqSettle(txID string, fraude bool, hace time.Duration) *pb.SettlementRequest {
	r := &pb.SettlementRequest{
		TxId:       txID,
		UserId:     "u1",
		Amount:     100,
		Evaluation: &pb.EvaluationResponse{TxId: txID, IsFraud: fraude},
	}
	if hace != sinEmision {
		r.EmissionTimestampNs = time.Now().Add(-hace).UnixNano()
	}
	return r
}

// intento describe una request en una tabla de casos SIN armarla todavía.
//
// Trampa (encontrada por un test intermitente): la tabla se evalúa UNA vez,
// antes de correr los subtests. Con time.Now() adentro de la tabla, todos
// los casos capturan la hora de construcción; los últimos corren cientos de
// ms después (con -race y los paquetes en paralelo) y su "ahora" ya tiene más
// de 200ms: Settlement los aborta, con razón. La hora se toma en el subtest.
type intento struct {
	fraude bool
	hace   time.Duration
}

func (i intento) req(txID string) *pb.SettlementRequest { return reqSettle(txID, i.fraude, i.hace) }

func TestCommitTransaction(t *testing.T) {
	casos := []struct {
		nombre     string
		req        intento
		wantEstado pb.TxStatus
	}{
		{"aprobada: se compromete", intento{false, 0}, pb.TxStatus_STATUS_COMMITTED},
		{"fraude: se registra el rechazo", intento{true, 0}, pb.TxStatus_STATUS_REJECTED_FRAUD},
		// Emitida hace 300ms: ya se pasó el deadline global de 200ms.
		{"llegó tarde: se aborta, no se compromete", intento{false, 300 * time.Millisecond}, pb.TxStatus_STATUS_ABORTED_TIMEOUT},
		{"sin emission_timestamp: se compromete sin medir latencia", intento{false, sinEmision}, pb.TxStatus_STATUS_COMMITTED},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := settlementDePrueba(t)
			resp, trailer, err := e.commit(t, c.req.req("t1"))
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if resp.Status != c.wantEstado {
				t.Errorf("status = %v, se esperaba %v", resp.Status, c.wantEstado)
			}

			// El registro quedó en Redis con ese estado.
			guardado, err := e.srv.rdb.HGet(context.Background(), e.srv.ClaveRegistro("t1"), "estado").Result()
			if err != nil || guardado != c.wantEstado.String() {
				t.Errorf("registro en Redis: estado=%q err=%v, se esperaba %q", guardado, err, c.wantEstado.String())
			}

			// El trailer trae C_settle en microsegundos.
			v := trailer.Get(TrailerSettleUs)
			if len(v) != 1 {
				t.Fatalf("trailer %q = %v, se esperaba un valor", TrailerSettleUs, v)
			}
			if us, err := strconv.ParseInt(v[0], 10, 64); err != nil || us < 0 {
				t.Errorf("trailer %q = %q, se esperaba un entero >= 0", TrailerSettleUs, v[0])
			}
		})
	}
}

func TestLatenciaExtremoAExtremo(t *testing.T) {
	e := settlementDePrueba(t)
	resp, _, err := e.commit(t, reqSettle("t1", false, 50*time.Millisecond))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Emitida hace 50ms: la latencia tiene que ser 50ms y un poco (la red).
	if resp.EndToEndLatencyMs < 50 || resp.EndToEndLatencyMs > 100 {
		t.Errorf("latencia = %dms, se esperaba entre 50 y 100", resp.EndToEndLatencyMs)
	}
}

// Reintentar el mismo tx_id no crea un registro nuevo: HSET sobre la misma
// clave deja el registro igual.
func TestIdempotencia(t *testing.T) {
	e := settlementDePrueba(t)
	for i := 0; i < 3; i++ {
		if _, _, err := e.commit(t, reqSettle("t1", false, 0)); err != nil {
			t.Fatalf("intento %d: %v", i, err)
		}
	}
	claves, err := e.srv.rdb.Keys(context.Background(), e.srv.prefijo+"settle:*").Result()
	if err != nil || len(claves) != 1 {
		t.Errorf("claves = %v (err=%v), se esperaba exactamente 1", claves, err)
	}
}

func TestRequestInvalida(t *testing.T) {
	e := settlementDePrueba(t)
	casos := []struct {
		nombre string
		req    *pb.SettlementRequest
	}{
		{"sin tx_id", reqSettle("", false, 0)},
		{"sin evaluation", &pb.SettlementRequest{TxId: "t1"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			_, trailer, err := e.commit(t, c.req)
			if status.Code(err) != codes.InvalidArgument || fallas.Motivo(err) != fallas.RequestInvalida {
				t.Errorf("código=%v motivo=%q, se esperaba InvalidArgument / %s", status.Code(err), fallas.Motivo(err), fallas.RequestInvalida)
			}
			// El trailer viaja también con errores.
			if len(trailer.Get(TrailerSettleUs)) != 1 {
				t.Errorf("sin trailer en la respuesta de error")
			}
		})
	}
}

// Redis caído: sin registro no hay commit (fail-closed), y rápido.
func TestRedisCaido(t *testing.T) {
	// Puerto donde no escucha nadie: Redis caído.
	rdb := redisclient.Nuevo("localhost:1")
	t.Cleanup(func() { rdb.Close() })
	e := levantarSettlement(t, rdb, "test:")
	inicio := time.Now()
	_, _, err := e.commit(t, reqSettle("t1", false, 0))
	if status.Code(err) != codes.Unavailable || fallas.Motivo(err) != fallas.RedisNoDisponible {
		t.Errorf("código=%v motivo=%q, se esperaba Unavailable / %s (err=%v)", status.Code(err), fallas.Motivo(err), fallas.RedisNoDisponible, err)
	}
	if tardo := time.Since(inicio); tardo > 20*time.Millisecond {
		t.Errorf("tardó %v en fallar, se esperaba fallo rápido", tardo)
	}
}

// Idempotencia de verdad: el PRIMER resultado final de un tx_id gana. Un
// reintento no puede deshacer un commit (la plata ya se movió) ni
// convertir un rechazo en commit.
func TestReintentoNoPisaElResultado(t *testing.T) {
	casos := []struct {
		nombre     string
		primero    intento
		reintento  intento
		wantEstado pb.TxStatus
	}{
		// El caso de la ambigüedad: el motor no recibió la respuesta y el
		// cliente reintenta con el emission_timestamp original, que ahora
		// ya tiene más de 200ms.
		{"commit y reintento tardío: sigue COMMITTED",
			intento{false, 0},
			intento{false, 300 * time.Millisecond},
			pb.TxStatus_STATUS_COMMITTED},
		// Entre el intento y el reintento la tarjeta entró en blacklist.
		{"commit y reintento evaluado como fraude: sigue COMMITTED",
			intento{false, 0},
			intento{true, 0},
			pb.TxStatus_STATUS_COMMITTED},
		{"rechazo y reintento aprobado: sigue REJECTED_FRAUD",
			intento{true, 0},
			intento{false, 0},
			pb.TxStatus_STATUS_REJECTED_FRAUD},
		// ABORTED_TIMEOUT no es final: no se comprometió nada, así que un
		// reintento a tiempo sí puede comprometer.
		{"abortada y reintento a tiempo: COMMITTED",
			intento{false, 300 * time.Millisecond},
			intento{false, 0},
			pb.TxStatus_STATUS_COMMITTED},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := settlementDePrueba(t)
			if _, _, err := e.commit(t, c.primero.req("t1")); err != nil {
				t.Fatalf("primer intento: %v", err)
			}
			resp, _, err := e.commit(t, c.reintento.req("t1"))
			if err != nil {
				t.Fatalf("reintento: %v", err)
			}
			if resp.Status != c.wantEstado {
				t.Errorf("respuesta del reintento: %v, se esperaba %v", resp.Status, c.wantEstado)
			}
			guardado, _ := e.srv.rdb.HGet(context.Background(), e.srv.ClaveRegistro("t1"), "estado").Result()
			if guardado != c.wantEstado.String() {
				t.Errorf("en Redis: %q, se esperaba %q", guardado, c.wantEstado.String())
			}
		})
	}
}

// Con TTL, el registro vence solo; el reintento dentro del TTL sigue viendo
// el primer resultado final.
func TestRegistroConTTL(t *testing.T) {
	rdb, prefijo := pruebas.Redis(t)
	srv := NuevoServidor(rdb, prefijo, nil, 24*time.Hour)
	conn := pruebas.ServirGRPC(t, func(g *grpc.Server) { pb.RegisterSettlementServer(g, srv) })
	e := &entorno{cliente: pb.NewSettlementClient(conn), srv: srv}

	if _, _, err := e.commit(t, reqSettle("t1", false, 0)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	ttl, err := rdb.TTL(context.Background(), srv.ClaveRegistro("t1")).Result()
	if err != nil || ttl <= 23*time.Hour || ttl > 24*time.Hour {
		t.Errorf("TTL = %v (err=%v), se esperaba ~24h", ttl, err)
	}
}

// LeerRegistro solo devuelve resultados FINALES: la misma regla que
// registrarUnaVez. ABORTED_TIMEOUT no es final (un reintento a tiempo lo
// reemplaza), así que no sirve para responder un reintento (ADR-0027).
func TestLeerRegistro(t *testing.T) {
	casos := []struct {
		nombre string
		campos map[string]string
		ok     bool
		want   Registro
	}{
		{"no existe (HGETALL vacío)", map[string]string{}, false, Registro{}},
		{"abortada: no es final", map[string]string{"estado": "STATUS_ABORTED_TIMEOUT"}, false, Registro{}},
		{"estado desconocido", map[string]string{"estado": "OTRA_COSA"}, false, Registro{}},
		{"comprometida y degradada", map[string]string{"estado": "STATUS_COMMITTED", "risk_score": "0", "is_degraded": "1"},
			true, Registro{Estado: pb.TxStatus_STATUS_COMMITTED, Degradada: true}},
		{"rechazada", map[string]string{"estado": "STATUS_REJECTED_FRAUD", "rejection_reason": "RIESGO_ALTO", "risk_score": "0.9", "is_degraded": "0"},
			true, Registro{Estado: pb.TxStatus_STATUS_REJECTED_FRAUD, Motivo: "RIESGO_ALTO", Score: 0.9}},
	}
	for _, c := range casos {
		got, ok := LeerRegistro(c.campos)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: LeerRegistro = %+v, %v; se esperaba %+v, %v", c.nombre, got, ok, c.want, c.ok)
		}
	}
}
