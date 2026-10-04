package main

import (
	"math"
	"testing"
)

// Welford tiene que dar lo mismo que el cálculo directo en dos pasadas
// (primero la media, después las diferencias), que es exacto pero necesita
// guardar todos los valores.
func TestAcumuladorWelford(t *testing.T) {
	valores := []float64{2.1, 4.7, 3.3, 9.8, 0.5, 4.4, 4.4, 7.0}
	fraudes := []bool{false, true, false, false, true, false, false, false}

	var a acumulador
	for i, x := range valores {
		a.agregar(x, fraudes[i])
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

	if a.n != len(valores) || a.fraudes != 2 {
		t.Errorf("n=%d fraudes=%d, se esperaba n=%d fraudes=2", a.n, a.fraudes, len(valores))
	}
	if math.Abs(a.media-media) > 1e-12 || math.Abs(a.desvio()-desvio) > 1e-12 {
		t.Errorf("media=%v desvio=%v, se esperaba media=%v desvio=%v", a.media, a.desvio(), media, desvio)
	}
}

// El caso donde la fórmula ingenua E[x²] − E[x]² falla: valores grandes y
// muy parecidos. La varianza real es 1/4 (± 0.5 alrededor de 1e9); la
// ingenua resta dos números de ~1e18 y pierde todos los dígitos.
func TestWelfordEsEstable(t *testing.T) {
	var a acumulador
	var suma, sumaCuadrados float64
	for i := 0; i < 1000; i++ {
		x := 1e9 + float64(i%2) - 0.5 // alterna 1e9-0.5 y 1e9+0.5
		a.agregar(x, false)
		suma += x
		sumaCuadrados += x * x
	}
	n := float64(a.n)
	ingenua := sumaCuadrados/n - (suma/n)*(suma/n)

	if got := a.desvio() * a.desvio(); math.Abs(got-0.25) > 1e-6 {
		t.Errorf("Welford: varianza = %v, se esperaba 0.25", got)
	}
	// No es un requisito: documenta POR QUÉ se usa Welford.
	t.Logf("varianza con la fórmula ingenua: %v (la real es 0.25)", ingenua)
}
