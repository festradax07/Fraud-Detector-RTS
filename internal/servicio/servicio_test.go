package servicio

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pb "fd_rts/proto"
)

// settlementLento es un servicio gRPC real que tarda demora en responder.
type settlementLento struct {
	pb.UnimplementedSettlementServer
	demora time.Duration
	llego  chan struct{} // avisa cuando la request ya está adentro
	addr   string
	unaVez sync.Once
}

// conexionDe abre una conexión gRPC extra al servidor del test (la de la
// salud: durante el drenaje, las conexiones existentes siguen vivas).
func conexionDe(t *testing.T, srv *settlementLento) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(srv.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("cliente: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func (s *settlementLento) CommitTransaction(ctx context.Context, req *pb.SettlementRequest) (*pb.SettlementResponse, error) {
	s.unaVez.Do(func() { close(s.llego) }) // cerrar un canal dos veces hace panic
	time.Sleep(s.demora)
	return &pb.SettlementResponse{TxId: req.TxId, Status: pb.TxStatus_STATUS_COMMITTED}, nil
}

// arrancar levanta Correr con un servicio lento en un puerto real y devuelve
// un cliente, la función que "manda SIGTERM" (cancela el ctx) y el canal con
// lo que devolvió Correr.
func arrancar(t *testing.T, demora, espera time.Duration) (pb.SettlementClient, *settlementLento, context.CancelFunc, <-chan error) {
	t.Helper()
	return arrancarCon(t, demora, Plazos{Espera: espera})
}

func arrancarCon(t *testing.T, demora time.Duration, plazos Plazos) (pb.SettlementClient, *settlementLento, context.CancelFunc, <-chan error) {
	t.Helper()
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := grpc.NewServer()
	srv := &settlementLento{demora: demora, llego: make(chan struct{}), addr: lis.Addr().String()}
	pb.RegisterSettlementServer(g, srv)

	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan error, 1)
	salud := NuevaSalud(g)
	go func() { fin <- Correr(ctx, g, lis, salud, nil, plazos) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("cliente: %v", err)
	}
	t.Cleanup(func() { conn.Close(); cancel() })
	return pb.NewSettlementClient(conn), srv, cancel, fin
}

// Llega la señal con una request en vuelo: la request TERMINA bien, y recién
// después se apaga el servidor.
func TestApagadoOrdenado(t *testing.T) {
	cliente, srv, apagar, fin := arrancar(t, 100*time.Millisecond, 2*time.Second)

	resultado := make(chan error, 1)
	go func() {
		_, err := cliente.CommitTransaction(context.Background(), &pb.SettlementRequest{TxId: "en-vuelo"})
		resultado <- err
	}()
	<-srv.llego // la request ya está siendo atendida
	apagar()    // "SIGTERM"

	if err := <-resultado; err != nil {
		t.Errorf("la request en vuelo falló: %v (se esperaba que terminara bien)", err)
	}
	if err := <-fin; err != nil {
		t.Errorf("Correr devolvió %v, se esperaba nil", err)
	}

	// Ya apagado: no acepta requests nuevas.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := cliente.CommitTransaction(ctx, &pb.SettlementRequest{TxId: "tarde"}); status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("después de apagar: código %v, se esperaba que no la atienda", status.Code(err))
	}
}

// Si las requests en vuelo no terminan en el plazo, Correr vuelve igual: el
// proceso no puede quedarse colgado esperando (el orquestador lo mataría con
// SIGKILL). El handler de este test duerme sin mirar el ctx a propósito: es
// el caso en que ni Stop puede cortarlo.
func TestApagadoForzado(t *testing.T) {
	cliente, srv, apagar, fin := arrancar(t, 2*time.Second, 50*time.Millisecond)

	go cliente.CommitTransaction(context.Background(), &pb.SettlementRequest{TxId: "eterna"})
	<-srv.llego
	inicio := time.Now()
	apagar()

	if err := <-fin; err == nil {
		t.Errorf("Correr devolvió nil, se esperaba error de apagado forzado")
	}
	if tardo := time.Since(inicio); tardo > 500*time.Millisecond {
		t.Errorf("tardó %v en apagar, se esperaba ~50ms (el plazo)", tardo)
	}
}

// El servicio de salud responde SERVING mientras atiende. Al llegar la
// señal pasa a NOT_SERVING y, durante el aviso, el servicio SIGUE atendiendo:
// es la ventana para que el balanceador lo saque de la rotación.
func TestSaludAlApagar(t *testing.T) {
	cliente, srv, apagar, fin := arrancarCon(t, 0, Plazos{Aviso: 200 * time.Millisecond, Espera: 2 * time.Second})
	salud := healthpb.NewHealthClient(conexionDe(t, srv))

	if st := estadoSalud(t, salud); st != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("antes de apagar: %v, se esperaba SERVING", st)
	}

	apagar()
	time.Sleep(50 * time.Millisecond) // dentro del aviso
	if st := estadoSalud(t, salud); st != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("durante el aviso: %v, se esperaba NOT_SERVING", st)
	}
	// Durante el aviso se sigue atendiendo normal.
	if _, err := cliente.CommitTransaction(context.Background(), &pb.SettlementRequest{TxId: "durante-el-aviso"}); err != nil {
		t.Errorf("durante el aviso la request falló: %v", err)
	}
	if err := <-fin; err != nil {
		t.Errorf("Correr: %v", err)
	}
}

func estadoSalud(t *testing.T, c healthpb.HealthClient) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return r.Status
}
