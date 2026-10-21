package main

// Perfil de carga diario (ADR-0026): la tasa de llegadas cambia a lo largo
// del día. Es un proceso de Poisson NO homogéneo, de tasa λ(t) = pico ×
// fracción(hora), con la hora del día comprimida (-compresion 60: 1 hora del
// perfil = 1 minuto real).

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"
)

// punto del perfil: a la hora `hora` (0 a 24), la tasa es `fraccion` × pico.
type punto struct {
	hora, fraccion float64
}

// perfil es la curva del día: puntos ordenados por hora. Entre dos puntos se
// interpola linealmente; dos puntos con la MISMA hora forman un escalón (un
// salto brusco de la tasa, por ejemplo un pico súbito).
type perfil []punto

// leerPerfil lee un CSV "hora,fraccion" (con encabezado). Las líneas que
// empiezan con # son comentarios.
func leerPerfil(ruta string) (perfil, error) {
	f, err := os.Open(ruta)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parsearPerfil(f)
}

func parsearPerfil(r io.Reader) (perfil, error) {
	lector := csv.NewReader(r)
	lector.Comment = '#'
	lector.FieldsPerRecord = 2

	if _, err := lector.Read(); err != nil { // encabezado
		return nil, fmt.Errorf("perfil vacío o sin encabezado: %w", err)
	}
	var p perfil
	for {
		reg, err := lector.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		hora, err1 := strconv.ParseFloat(strings.TrimSpace(reg[0]), 64)
		frac, err2 := strconv.ParseFloat(strings.TrimSpace(reg[1]), 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("línea %v: hora y fracción tienen que ser números", reg)
		}
		if hora < 0 || hora > 24 {
			return nil, fmt.Errorf("hora %v fuera de 0..24", hora)
		}
		if frac < 0 || frac > 1 {
			return nil, fmt.Errorf("fracción %v fuera de 0..1 (es relativa al pico)", frac)
		}
		if len(p) > 0 && hora < p[len(p)-1].hora {
			return nil, fmt.Errorf("las horas tienen que estar en orden: %v después de %v", hora, p[len(p)-1].hora)
		}
		p = append(p, punto{hora, frac})
	}
	if len(p) < 2 {
		return nil, errors.New("el perfil necesita al menos dos puntos")
	}
	return p, nil
}

// fraccion devuelve la fracción del pico a la hora h, interpolando entre los
// puntos vecinos. Antes del primero y después del último, se mantiene el
// valor del extremo.
func (p perfil) fraccion(h float64) float64 {
	if h <= p[0].hora {
		return p[0].fraccion
	}
	for i := 1; i < len(p); i++ {
		a, b := p[i-1], p[i]
		if h < b.hora {
			return a.fraccion + (b.fraccion-a.fraccion)*(h-a.hora)/(b.hora-a.hora)
		}
		// h == b.hora en un escalón (a.hora == b.hora): sigue el loop y
		// queda el valor de DESPUÉS del salto.
	}
	return p[len(p)-1].fraccion
}

// sortearLlegada devuelve el instante (desde el inicio de la corrida) de la
// próxima llegada después de t, o un valor >= limite si no hay más.
//
// Sin perfil (fraccionEn == nil) es Poisson homogéneo: un intervalo
// exponencial de tasa rps. Con perfil usa THINNING (Lewis y Shedler, 1979):
// se generan candidatos a la tasa máxima (el pico) y cada uno se acepta con
// probabilidad λ(t)/λmax = fracción(t). Lo que queda es exactamente un
// proceso de Poisson de tasa λ(t), rampas incluidas.
//
// Sin perfil no se sortea nada extra: con la misma semilla, las llegadas son
// idénticas a las de antes de existir los perfiles.
func sortearLlegada(r *rand.Rand, t, limite time.Duration, rps float64, fraccionEn func(time.Duration) float64) time.Duration {
	for {
		t += intervaloPoisson(r, rps)
		if fraccionEn == nil || t >= limite {
			return t
		}
		if r.Float64() < fraccionEn(t) {
			return t
		}
		// Candidato descartado: se sigue buscando. Con fracción 0 el loop
		// avanza hasta el límite sin aceptar ninguno.
	}
}
