// Package estadisticas calcula media y desvío en una sola pasada (algoritmo
// de Welford). Lo usan cmd/cargar-stats (la línea base, desde el dataset) y
// el consumidor de estadísticas en vivo (modo sombra, desde los eventos).
package estadisticas

import "math"

// Acumulador lleva media y varianza de una serie sin guardar los valores.
//
// La fórmula ingenua var = E[x²] - E[x]² resta dos números grandes y
// parecidos y pierde precisión (con valores cerca de 1e9 da 0); Welford va
// corrigiendo la media de a un valor y no tiene ese problema.
//
// El valor cero es un acumulador vacío listo para usar.
type Acumulador struct {
	N     int
	Media float64
	m2    float64 // suma de cuadrados de las diferencias con la media
}

// Agregar suma un valor.
func (a *Acumulador) Agregar(x float64) {
	a.N++
	delta := x - a.Media
	a.Media += delta / float64(a.N)
	a.m2 += delta * (x - a.Media) // ojo: usa la media YA actualizada
}

// Desvio es el desvío estándar poblacional (0 si no hay valores).
func (a *Acumulador) Desvio() float64 {
	if a.N == 0 {
		return 0
	}
	return math.Sqrt(a.m2 / float64(a.N))
}
