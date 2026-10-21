package main

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func TestFraccionInterpola(t *testing.T) {
	p := perfil{{0, 0.2}, {10, 0.6}, {12, 0.6}, {12, 1.0}, {24, 0.0}}
	casos := []struct {
		hora, quiere float64
	}{
		{-1, 0.2},    // antes del primero: el extremo
		{0, 0.2},     // sobre un punto
		{5, 0.4},     // a mitad de camino entre 0.2 y 0.6
		{11, 0.6},    // meseta
		{11.99, 0.6}, // justo antes del escalón
		{12, 1.0},    // en el escalón: el valor de después del salto
		{18, 0.5},    // bajando de 1.0 a 0.0
		{30, 0.0},    // después del último: el extremo
	}
	for _, c := range casos {
		if got := p.fraccion(c.hora); math.Abs(got-c.quiere) > 1e-9 {
			t.Errorf("fraccion(%v) = %v, quiero %v", c.hora, got, c.quiere)
		}
	}
}

func TestParsearPerfil(t *testing.T) {
	bien := "# comentario\nhora,fraccion\n0, 0.1\n# otro\n12,1\n24,0.1\n"
	p, err := parsearPerfil(strings.NewReader(bien))
	if err != nil {
		t.Fatalf("perfil válido rechazado: %v", err)
	}
	if len(p) != 3 || p[1] != (punto{12, 1}) {
		t.Errorf("perfil leído = %v", p)
	}

	malos := map[string]string{
		"vacío":           "",
		"un solo punto":   "hora,fraccion\n0,1\n",
		"desordenado":     "hora,fraccion\n10,1\n5,1\n",
		"hora fuera":      "hora,fraccion\n0,1\n25,1\n",
		"fracción > 1":    "hora,fraccion\n0,1\n12,1.5\n",
		"fracción < 0":    "hora,fraccion\n0,-0.1\n12,1\n",
		"no numérico":     "hora,fraccion\n0,uno\n12,1\n",
		"columnas de más": "hora,fraccion\n0,1,2\n12,1,2\n",
	}
	for nombre, texto := range malos {
		if _, err := parsearPerfil(strings.NewReader(texto)); err == nil {
			t.Errorf("%s: se aceptó un perfil inválido", nombre)
		}
	}
}

// Sin perfil, sortearLlegada tiene que dar EXACTAMENTE la misma secuencia que
// sumar intervalos exponenciales: así las corridas de antes de existir los
// perfiles siguen siendo reproducibles con la misma semilla.
func TestSortearLlegadaSinPerfilEsLaDeAntes(t *testing.T) {
	a := rand.New(rand.NewPCG(7, 7))
	b := rand.New(rand.NewPCG(7, 7))
	var t1, t2 time.Duration
	for i := 0; i < 1000; i++ {
		t1 = sortearLlegada(a, t1, time.Hour, 300, nil)
		t2 += intervaloPoisson(b, 300)
		if t1 != t2 {
			t.Fatalf("llegada %d: %v vs %v", i, t1, t2)
		}
	}
}

// Con perfil, la cantidad de llegadas en cada tramo tiene que ser la integral
// de λ(t): con tasa pico 1000/s, 100s al 25% (≈25.000) y 100s al 100%
// (≈100.000). Un Poisson con media m tiene desvío √m (~0,6% acá): un margen
// del 2% no falla por azar.
func TestSortearLlegadaThinning(t *testing.T) {
	const pico = 1000.0
	mitad := 100 * time.Second
	fraccionEn := func(t time.Duration) float64 {
		if t < mitad {
			return 0.25
		}
		return 1
	}
	r := rand.New(rand.NewPCG(1, 1))
	var antes, despues int
	for t0 := time.Duration(0); ; {
		t0 = sortearLlegada(r, t0, 2*mitad, pico, fraccionEn)
		if t0 >= 2*mitad {
			break
		}
		if t0 < mitad {
			antes++
		} else {
			despues++
		}
	}
	for _, c := range []struct {
		nombre      string
		got, quiero int
	}{{"tramo al 25%", antes, 25_000}, {"tramo al 100%", despues, 100_000}} {
		if math.Abs(float64(c.got-c.quiero)) > 0.02*float64(c.quiero) {
			t.Errorf("%s: %d llegadas, esperaba ~%d", c.nombre, c.got, c.quiero)
		}
	}
}

// Con fracción 0 no hay llegadas, y el loop tiene que terminar en el límite
// (no quedarse buscando un candidato que nunca se acepta).
func TestSortearLlegadaFraccionCeroTermina(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 1))
	cero := func(time.Duration) float64 { return 0 }
	if got := sortearLlegada(r, 0, 10*time.Second, 1000, cero); got < 10*time.Second {
		t.Errorf("con fracción 0 se aceptó una llegada en %v", got)
	}
}
