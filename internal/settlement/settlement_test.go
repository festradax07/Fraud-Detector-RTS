package settlement

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"fd_rts/internal/redisclient"
	pb "fd_rts/proto"
)

// redisAddr del Redis real de desarrollo.
const redisAddr = "localhost:6379"

// entorno es un Settlement real escuchando en un puerto TCP real, contra el
// Redis real, con un cliente gRPC real apuntándole. Nada simulado.
type entorno struct {
	cliente pb.SettlementClient
	srv     *Servidor
}

// levantarSettlement arranca el servicio en localhost:0 (el sistema operativo
// elige un puerto libre) con un prefijo de claves único. Todo se apaga y se
// limpia al terminar el test. Si Redis no está, el test FALLA.
func levantarSettlement(t *testing.T, redisAddr string) *entorno {
	t.Helper()
	rdb := redisclient.Nuevo(redisAddr)
	prefijo := fmt.Sprintf("test:%d:%d:", os.Getpid(), time.Now().UnixNano())
	srv := NuevoServidor(rdb, prefijo)

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	pb.RegisterSettlementServer(g, srv)
	// Serve bloquea, así que va en su propia goroutine (tema de la Etapa 6;
	// acá lo mínimo para tener el server corriendo durante el test).
	go g.Serve(lis)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("cliente: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		g.Stop()
		ctx := context.Background()
		iter := rdb.Scan(ctx, 0, prefijo+"*", 100).Iterator()
		for iter.Next(ctx) {
			rdb.Del(ctx, iter.Val())
		}
		rdb.Close()
	})
	return &entorno{cliente: pb.NewSettlementClient(conn), srv: srv}
}

// redisDisponible hace FALLAR el test si Redis no está levantado.
func redisDisponible(t *testing.T) {
	t.Helper()
	rdb := redisclient.Nuevo(redisAddr)
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis no disponible en %s (levantalo con: docker start redis-drts): %v", redisAddr, err)
	}
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

func reqSettle(txID string, fraude bool, emision time.Time) *pb.SettlementRequest {
	r := &pb.SettlementRequest{
		TxId:       txID,
		UserId:     "u1",
		Amount:     100,
		Evaluation: &pb.EvaluationResponse{TxId: txID, IsFraud: fraude},
	}
	if !emision.IsZero() {
		r.EmissionTimestampNs = emision.UnixNano()
	}
	return r
}

func TestCommitTransaction(t *testing.T) {
	redisDisponible(t)
	casos := []struct {
		nombre     string
		req        *pb.SettlementRequest
		wantEstado pb.TxStatus
	}{
		{"aprobada: se compromete", reqSettle("t1", false, time.Now()), pb.TxStatus_STATUS_COMMITTED},
		{"fraude: se registra el rechazo", reqSettle("t1", true, time.Now()), pb.TxStatus_STATUS_REJECTED_FRAUD},
		// Emitida hace 300ms: ya se pasó el deadline global de 200ms.
		{"llegó tarde: se aborta, no se compromete", reqSettle("t1", false, time.Now().Add(-300*time.Millisecond)), pb.TxStatus_STATUS_ABORTED_TIMEOUT},
		{"sin emission_timestamp: se compromete sin medir latencia", reqSettle("t1", false, time.Time{}), pb.TxStatus_STATUS_COMMITTED},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := levantarSettlement(t, redisAddr)
			resp, trailer, err := e.commit(t, c.req)
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
	redisDisponible(t)
	e := levantarSettlement(t, redisAddr)
	resp, _, err := e.commit(t, reqSettle("t1", false, time.Now().Add(-50*time.Millisecond)))
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
	redisDisponible(t)
	e := levantarSettlement(t, redisAddr)
	for i := 0; i < 3; i++ {
		if _, _, err := e.commit(t, reqSettle("t1", false, time.Now())); err != nil {
			t.Fatalf("intento %d: %v", i, err)
		}
	}
	claves, err := e.srv.rdb.Keys(context.Background(), e.srv.prefijo+"settle:*").Result()
	if err != nil || len(claves) != 1 {
		t.Errorf("claves = %v (err=%v), se esperaba exactamente 1", claves, err)
	}
}

func TestRequestInvalida(t *testing.T) {
	redisDisponible(t)
	e := levantarSettlement(t, redisAddr)
	casos := []struct {
		nombre string
		req    *pb.SettlementRequest
	}{
		{"sin tx_id", reqSettle("", false, time.Now())},
		{"sin evaluation", &pb.SettlementRequest{TxId: "t1"}},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			_, trailer, err := e.commit(t, c.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("código = %v, se esperaba InvalidArgument", status.Code(err))
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
	e := levantarSettlement(t, "localhost:1")
	inicio := time.Now()
	_, _, err := e.commit(t, reqSettle("t1", false, time.Now()))
	if status.Code(err) != codes.Unavailable {
		t.Errorf("código = %v, se esperaba Unavailable (err=%v)", status.Code(err), err)
	}
	if tardo := time.Since(inicio); tardo > 20*time.Millisecond {
		t.Errorf("tardó %v en fallar, se esperaba fallo rápido", tardo)
	}
}

// Idempotencia de verdad: el PRIMER resultado final de un tx_id gana. Un
// reintento no puede deshacer un commit (la plata ya se movió) ni
// convertir un rechazo en commit.
func TestReintentoNoPisaElResultado(t *testing.T) {
	redisDisponible(t)
	casos := []struct {
		nombre     string
		primero    *pb.SettlementRequest
		reintento  *pb.SettlementRequest
		wantEstado pb.TxStatus
	}{
		// El caso de la ambigüedad: el motor no recibió la respuesta y el
		// cliente reintenta con el emission_timestamp original, que ahora
		// ya tiene más de 200ms.
		{"commit y reintento tardío: sigue COMMITTED",
			reqSettle("t1", false, time.Now()),
			reqSettle("t1", false, time.Now().Add(-300*time.Millisecond)),
			pb.TxStatus_STATUS_COMMITTED},
		// Entre el intento y el reintento la tarjeta entró en blacklist.
		{"commit y reintento evaluado como fraude: sigue COMMITTED",
			reqSettle("t1", false, time.Now()),
			reqSettle("t1", true, time.Now()),
			pb.TxStatus_STATUS_COMMITTED},
		{"rechazo y reintento aprobado: sigue REJECTED_FRAUD",
			reqSettle("t1", true, time.Now()),
			reqSettle("t1", false, time.Now()),
			pb.TxStatus_STATUS_REJECTED_FRAUD},
		// ABORTED_TIMEOUT no es final: no se comprometió nada, así que un
		// reintento a tiempo sí puede comprometer.
		{"abortada y reintento a tiempo: COMMITTED",
			reqSettle("t1", false, time.Now().Add(-300*time.Millisecond)),
			reqSettle("t1", false, time.Now()),
			pb.TxStatus_STATUS_COMMITTED},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			e := levantarSettlement(t, redisAddr)
			if _, _, err := e.commit(t, c.primero); err != nil {
				t.Fatalf("primer intento: %v", err)
			}
			resp, _, err := e.commit(t, c.reintento)
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
