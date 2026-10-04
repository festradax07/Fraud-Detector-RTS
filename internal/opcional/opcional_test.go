package opcional

import (
	"fmt"
	"math"
	"testing"
)

// Estadísticas de prueba: mediana de $100 (media_log = ln 100), desvío 1 en
// log, riesgo promedio. Con esto, monto = 100·e^z da exactamente ese z.
var statsPrueba = Estadisticas{MediaLog: math.Log(100), DesvioLog: 1, Peso: 1}

func montoConZ(z float64) float64 { return 100 * math.Exp(z) }

func TestPuntuar(t *testing.T) {
	casos := []struct {
		nombre      string
		monto       float64
		stats       Estadisticas
		want        float64
		wantRechazo bool
	}{
		{"monto típico (z=0): sin riesgo", montoConZ(0), statsPrueba, 0, false},
		{"z=3, peso 1: justo el umbral, no lo supera", montoConZ(3), statsPrueba, 0.75, false},
		{"z=4, peso 1: se rechaza", montoConZ(4), statsPrueba, 1 - math.Pow(0.25, 16.0/9), true},
		// z² mira para los dos lados: monto anormalmente BAJO (card testing).
		{"z=-4: monto anormalmente bajo, se rechaza", montoConZ(-4), statsPrueba, 1 - math.Pow(0.25, 16.0/9), true},
		// Peso 3: categoría riesgosa, rechaza con |z| > √3 ≈ 1.73.
		{"z=2 en categoría riesgosa (peso 3)", montoConZ(2), Estadisticas{MediaLog: math.Log(100), DesvioLog: 1, Peso: 3}, 1 - math.Pow(0.25, 12.0/9), true},
		// Peso 0.25: categoría tranquila, z=4 ya no alcanza (necesita |z| > 6).
		{"z=4 en categoría tranquila (peso 0.25)", montoConZ(4), Estadisticas{MediaLog: math.Log(100), DesvioLog: 1, Peso: 0.25}, 1 - math.Pow(0.25, 4.0/9), false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			// El score no puede depender de cuánto CPU se quemó.
			for _, it := range []int{0, 1000, 100000} {
				got := Puntuar(c.monto, c.stats, it)
				if math.Abs(got-c.want) > 1e-9 {
					t.Errorf("iteraciones=%d: score = %v, se esperaba %v", it, got, c.want)
				}
				// Con tolerancia: 0.75 "exacto" no debe rechazarse por un
				// error de redondeo de exp/log.
				if rechazo := got > UmbralRechazo+1e-9; rechazo != c.wantRechazo {
					t.Errorf("iteraciones=%d: rechazo = %v (score %v), se esperaba %v", it, rechazo, got, c.wantRechazo)
				}
			}
		})
	}
}

func TestParsearEstadisticas(t *testing.T) {
	t.Run("hash completo", func(t *testing.T) {
		e, ok, err := ParsearEstadisticas(map[string]string{
			CampoMediaLog: "4.6", CampoDesvioLog: "0.45", CampoPeso: "2.44",
		})
		if err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if e != (Estadisticas{MediaLog: 4.6, DesvioLog: 0.45, Peso: 2.44}) {
			t.Errorf("e = %+v", e)
		}
	})
	t.Run("hash vacío: categoría desconocida", func(t *testing.T) {
		if _, ok, err := ParsearEstadisticas(map[string]string{}); ok || err != nil {
			t.Errorf("ok=%v err=%v, se esperaba ok=false sin error", ok, err)
		}
	})
	malos := []map[string]string{
		{CampoMediaLog: "4.6", CampoDesvioLog: "0.45"},                  // falta peso
		{CampoMediaLog: "x", CampoDesvioLog: "0.45", CampoPeso: "1"},    // no es número
		{CampoMediaLog: "4.6", CampoDesvioLog: "0", CampoPeso: "1"},     // desvío 0
		{CampoMediaLog: "4.6", CampoDesvioLog: "0.45", CampoPeso: "-1"}, // peso negativo
	}
	for i, h := range malos {
		if _, _, err := ParsearEstadisticas(h); err == nil {
			t.Errorf("caso %d %v: se esperaba error", i, h)
		}
	}
}

// BenchmarkPuntuar mide cuánto cuesta el scoring según las iteraciones de
// costo simulado. Correr con:
//
//	go test -run=^$ -bench=. -benchtime=2s ./internal/opcional/
//
// Ojo: lo que reporta (ns/op) es el PROMEDIO. El WCET es el PEOR caso, que
// puede ser bastante mayor (GC, scheduler, otro proceso usando el CPU).
func BenchmarkPuntuar(b *testing.B) {
	for _, it := range []int{0, 1_000, 100_000, 1_000_000, 10_000_000} {
		b.Run(fmt.Sprintf("iteraciones=%d", it), func(b *testing.B) {
			// b.Loop (Go 1.24+) maneja cuántas veces repetir y además evita
			// que el compilador elimine la llamada aunque no usemos el
			// resultado. Es el reemplazo moderno de `for i := 0; i < b.N; i++`.
			for b.Loop() {
				Puntuar(1500, statsPrueba, it)
			}
		})
	}
}
