package main

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"fd_rts/internal/fallas"
)

// Los errores de la tabla son errores gRPC reales, armados igual que los
// arma el servidor (fallas.Nueva) o que los ve el cliente cuando vence su
// propio timer (status sin detalle).
func TestClasificar(t *testing.T) {
	casos := []struct {
		nombre     string
		err        error
		wantCat    string
		wantMotivo string
	}{
		{"respuesta OK", nil, catProcesada, ""},
		{"early drop", fallas.Nueva(codes.DeadlineExceeded, fallas.EarlyDrop, "x"), catEarlyDrop, fallas.EarlyDrop},
		// Mismo código que el early drop: solo el motivo los distingue.
		{"timeout de Settlement: ambigua", fallas.Nueva(codes.DeadlineExceeded, fallas.SettlementTimeout, "x"), catAmbigua, fallas.SettlementTimeout},
		{"timer del cliente: deadline miss", status.Error(codes.DeadlineExceeded, "context deadline exceeded"), catDeadline, "TIMEOUT_DEL_CLIENTE"},
		{"Redis caído: error con motivo", fallas.Nueva(codes.Unavailable, fallas.RedisNoDisponible, "x"), catError, fallas.RedisNoDisponible},
		{"error sin motivo: el código", status.Error(codes.Unavailable, "x"), catError, "Unavailable"},
		{"error que no es gRPC", errors.New("x"), catError, "Unknown"},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			cat, motivo := clasificar(c.err)
			if cat != c.wantCat || motivo != c.wantMotivo {
				t.Errorf("clasificar = (%q, %q), se esperaba (%q, %q)", cat, motivo, c.wantCat, c.wantMotivo)
			}
		})
	}
}

// Propiedades de un proceso de Poisson de tasa λ, verificadas sobre 200.000
// intervalos sorteados:
//   - la media de los intervalos es 1/λ;
//   - el coeficiente de variación (desvío / media) de una exponencial es 1:
//     los intervalos son MUY irregulares (ráfagas y huecos), no un metrónomo.
func TestIntervaloPoisson(t *testing.T) {
	const rps = 350.0
	const n = 200_000
	r := rand.New(rand.NewPCG(1, 1))

	var suma, sumaCuadrados float64
	for i := 0; i < n; i++ {
		x := intervaloPoisson(r, rps).Seconds()
		suma += x
		sumaCuadrados += x * x
	}
	media := suma / n
	desvio := math.Sqrt(sumaCuadrados/n - media*media)

	if want := 1 / rps; math.Abs(media-want)/want > 0.01 {
		t.Errorf("media = %v, se esperaba %v (±1%%)", media, want)
	}
	if cv := desvio / media; math.Abs(cv-1) > 0.02 {
		t.Errorf("coeficiente de variación = %.3f, se esperaba 1 (exponencial)", cv)
	}
}

// Con la misma semilla, la misma secuencia de llegadas: corridas
// reproducibles.
func TestIntervaloPoissonReproducible(t *testing.T) {
	a := rand.New(rand.NewPCG(42, 42))
	b := rand.New(rand.NewPCG(42, 42))
	for i := 0; i < 100; i++ {
		if x, y := intervaloPoisson(a, 50), intervaloPoisson(b, 50); x != y {
			t.Fatalf("intervalo %d: %v != %v", i, x, y)
		}
	}
}

func TestPercentil(t *testing.T) {
	ms := func(xs ...int) []time.Duration {
		var d []time.Duration
		for _, x := range xs {
			d = append(d, time.Duration(x)*time.Millisecond)
		}
		return d
	}
	datos := ms(1, 2, 3, 4, 5, 6, 7, 8, 9, 10) // ya ordenados

	casos := []struct {
		q    float64
		want time.Duration
	}{
		{0.5, 5 * time.Millisecond}, // nearest-rank: ceil(0.5·10) = 5.º
		{0.9, 9 * time.Millisecond},
		{0.99, 10 * time.Millisecond}, // ceil(9.9) = 10.º
		{1, 10 * time.Millisecond},    // P100 = el máximo
	}
	for _, c := range casos {
		if got := percentil(datos, c.q); got != c.want {
			t.Errorf("percentil(%v) = %v, se esperaba %v", c.q, got, c.want)
		}
	}
	if got := percentil(nil, 0.5); got != 0 {
		t.Errorf("percentil de lista vacía = %v, se esperaba 0", got)
	}
}
