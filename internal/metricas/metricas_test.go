package metricas

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"fd_rts/internal/fallas"
)

var info = &grpc.UnaryServerInfo{FullMethod: "/transactions.FraudEngine/EvaluateTransaction"}

func TestInterceptor(t *testing.T) {
	reg := NuevoRegistro()
	r := NuevoRPC(reg, "prueba")
	var respuestas []any
	icp := r.Interceptor(func(resp any) { respuestas = append(respuestas, resp) })

	// Los "handlers" son funciones comunes: lo mismo que gRPC le pasa al
	// interceptor.
	ok := func(ctx context.Context, req any) (any, error) { return "respuesta", nil }
	drop := func(ctx context.Context, req any) (any, error) {
		return nil, fallas.Nueva(codes.DeadlineExceeded, fallas.EarlyDrop, "x")
	}

	ctx := context.Background()
	icp(ctx, nil, info, ok)
	icp(ctx, nil, info, ok)
	icp(ctx, nil, info, drop)

	if got := testutil.ToFloat64(r.requests.WithLabelValues("OK", "")); got != 2 {
		t.Errorf("requests OK = %v, se esperaba 2", got)
	}
	// El motivo sale del ErrorInfo: el early drop queda separado.
	if got := testutil.ToFloat64(r.requests.WithLabelValues("DeadlineExceeded", fallas.EarlyDrop)); got != 1 {
		t.Errorf("requests EARLY_DROP = %v, se esperaba 1", got)
	}
	if got := testutil.CollectAndCount(r.latencia); got != 1 {
		t.Errorf("series de latencia = %d, se esperaba 1", got)
	}
	// alResponder solo ve las exitosas.
	if len(respuestas) != 2 {
		t.Errorf("alResponder llamado %d veces, se esperaba 2", len(respuestas))
	}
	// Terminadas todas: nada en vuelo.
	if got := testutil.ToFloat64(r.enVuelo); got != 0 {
		t.Errorf("en vuelo = %v al terminar, se esperaba 0", got)
	}
	if got := testutil.ToFloat64(r.tardias); got != 0 {
		t.Errorf("tardías = %v, se esperaba 0", got)
	}
}

// Mientras el handler corre, la request cuenta como "en vuelo".
func TestEnVuelo(t *testing.T) {
	r := NuevoRPC(NuevoRegistro(), "prueba")
	icp := r.Interceptor(nil)
	var adentro float64
	icp(context.Background(), nil, info, func(ctx context.Context, req any) (any, error) {
		adentro = testutil.ToFloat64(r.enVuelo)
		return nil, nil
	})
	if adentro != 1 {
		t.Errorf("en vuelo durante el handler = %v, se esperaba 1", adentro)
	}
}

// Respuesta tardía: el handler termina con el contexto ya vencido (el
// cliente ya se fue). El servidor "lo hizo bien", pero llegó tarde.
func TestRespuestaTardia(t *testing.T) {
	r := NuevoRPC(NuevoRegistro(), "prueba")
	icp := r.Interceptor(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	icp(ctx, nil, info, func(ctx context.Context, req any) (any, error) {
		time.Sleep(10 * time.Millisecond) // trabaja más que el deadline
		return "llegué tarde", nil
	})

	if got := testutil.ToFloat64(r.tardias); got != 1 {
		t.Errorf("tardías = %v, se esperaba 1", got)
	}
	// Para el contador de requests, fue un OK: por eso hace falta la otra.
	if got := testutil.ToFloat64(r.requests.WithLabelValues("OK", "")); got != 1 {
		t.Errorf("requests OK = %v, se esperaba 1", got)
	}
}

func TestMotorDecisiones(t *testing.T) {
	m := NuevoMotor(NuevoRegistro())
	m.Decision(false, "", false)
	m.Decision(false, "", true)
	m.Decision(true, "RIESGO_ALTO", false)

	casos := []struct {
		labels []string
		want   float64
	}{
		{[]string{"aprobada", "", "no"}, 1},
		{[]string{"aprobada", "", "si"}, 1},
		{[]string{"rechazada", "RIESGO_ALTO", "no"}, 1},
	}
	for _, c := range casos {
		if got := testutil.ToFloat64(m.decisiones.WithLabelValues(c.labels...)); got != c.want {
			t.Errorf("decisiones%v = %v, se esperaba %v", c.labels, got, c.want)
		}
	}
}

// Con receptor nil, los métodos no hacen nada (y no hacen panic).
func TestMotorNil(t *testing.T) {
	var m *Motor
	m.DRemLlegada(time.Millisecond)
	m.Slack(time.Millisecond)
	m.Fase(FaseMandatoria, time.Millisecond)
	m.Decision(true, "X", false)
	var s *Settlement
	s.Registrado("STATUS_COMMITTED", time.Millisecond)
}

// Cuánto le agrega el interceptor a cada request. En un sistema de tiempo
// real, instrumentar no puede comerse el presupuesto que se quiere medir.
//
//	go test -run=^$ -bench=Interceptor ./internal/metricas/
func BenchmarkInterceptor(b *testing.B) {
	r := NuevoRPC(NuevoRegistro(), "prueba")
	icp := r.Interceptor(func(any) {})
	ctx := context.Background()
	nada := func(ctx context.Context, req any) (any, error) { return nil, nil }
	for b.Loop() {
		icp(ctx, nil, info, nada)
	}
}
