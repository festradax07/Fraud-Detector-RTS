package estadisticas

import (
	"math"
	"testing"
)

// Welford tiene que dar lo mismo que el cálculo directo en dos pasadas
// (primero la media, después las diferencias), que es exacto pero necesita
// guardar todos los valores.
func TestAcumuladorWelford(t *testing.T) {
	valores := []float64{2.1, 4.7, 3.3, 9.8, 0.5, 4.4, 4.4, 7.0}

	var a Acumulador
	for _, x := range valores {
		a.Agregar(x)
	}

	var suma float64
	for _, x := range valores {
		suma += x
	}
	media := suma / float64(len(valores))
	var sc float64
	for _, x := range valores {
		sc += (x - media) * (x - media)
	}
	desvio := math.Sqrt(sc / float64(len(valores)))

	if a.N != len(valores) {
		t.Errorf("N=%d, se esperaba %d", a.N, len(valores))
	}
	if math.Abs(a.Media-media) > 1e-12 || math.Abs(a.Desvio()-desvio) > 1e-12 {
		t.Errorf("media=%v desvio=%v, se esperaba media=%v desvio=%v", a.Media, a.Desvio(), media, desvio)
	}
}

// El caso donde la fórmula ingenua E[x²] − E[x]² falla: valores grandes y
// muy parecidos. La varianza real es 1/4 (± 0.5 alrededor de 1e9); la
// ingenua resta dos números de ~1e18 y pierde todos los dígitos.
func TestWelfordEsEstable(t *testing.T) {
	var a Acumulador
	var suma, sumaCuadrados float64
	for i := 0; i < 1000; i++ {
		x := 1e9 + float64(i%2) - 0.5 // alterna 1e9-0.5 y 1e9+0.5
		a.Agregar(x)
		suma += x
		sumaCuadrados += x * x
	}
	n := float64(a.N)
	ingenua := sumaCuadrados/n - (suma/n)*(suma/n)

	if got := a.Desvio() * a.Desvio(); math.Abs(got-0.25) > 1e-6 {
		t.Errorf("Welford: varianza = %v, se esperaba 0.25", got)
	}
	// No es un requisito: documenta POR QUÉ se usa Welford.
	t.Logf("varianza con la fórmula ingenua: %v (la real es 0.25)", ingenua)
}
