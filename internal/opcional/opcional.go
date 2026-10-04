// Package opcional implementa la fase opcional (O_i) del Fraud-Engine: el
// scoring de riesgo. Solo corre si hay slack (ver internal/presupuesto).
//
// Modelo: Z-score sobre el LOGARITMO del monto, ponderado por el riesgo de
// la categoría de comercio. Las estadísticas por categoría se calculan de
// antemano desde un dataset (cmd/cargar-stats) y viven en Redis.
package opcional

import (
	"fmt"
	"math"
	"strconv"
)

// UmbralRechazo: un score mayor a esto es fraude (REJECTED_FRAUD).
const UmbralRechazo = 0.75

// umbralSigmas: en una categoría de riesgo promedio (peso 1), un monto a 3
// desvíos de lo normal da exactamente UmbralRechazo. Es la regla de las 3
// sigmas, una convención estadística clásica: la única decisión de diseño
// de la fórmula.
const umbralSigmas = 3.0

// Estadisticas de una categoría de comercio, calculadas sobre ln(monto).
type Estadisticas struct {
	MediaLog  float64
	DesvioLog float64
	// Peso = tasa de fraude de la categoría / tasa de fraude global.
	// > 1: categoría más riesgosa que el promedio.
	Peso float64
}

// Campos del hash de Redis drts:stats:<categoria>. Los usa también
// cmd/cargar-stats, así el formato está en un solo lugar.
const (
	CampoMediaLog  = "media_log"
	CampoDesvioLog = "desvio_log"
	CampoPeso      = "peso"
)

// ParsearEstadisticas convierte el hash leído de Redis (HGETALL) en
// Estadisticas. ok es false si el hash está vacío: la categoría no existe.
// Un hash con datos inválidos es error (fail-closed: no se puntúa con
// estadísticas rotas).
func ParsearEstadisticas(h map[string]string) (e Estadisticas, ok bool, err error) {
	if len(h) == 0 {
		return Estadisticas{}, false, nil
	}
	campos := []struct {
		nombre string
		dst    *float64
	}{
		{CampoMediaLog, &e.MediaLog},
		{CampoDesvioLog, &e.DesvioLog},
		{CampoPeso, &e.Peso},
	}
	for _, c := range campos {
		v, err := strconv.ParseFloat(h[c.nombre], 64)
		if err != nil {
			return Estadisticas{}, false, fmt.Errorf("estadística %q inválida: %w", c.nombre, err)
		}
		*c.dst = v
	}
	// Con desvío 0 el z sería una división por cero.
	if e.DesvioLog <= 0 || e.Peso < 0 {
		return Estadisticas{}, false, fmt.Errorf("estadísticas fuera de rango: %+v", e)
	}
	return e, true, nil
}

// Puntuar devuelve el score de riesgo en [0, 1]:
//
//	z     = (ln(monto) - MediaLog) / DesvioLog
//	score = 1 - (1 - UmbralRechazo)^(Peso · z² / umbralSigmas²)
//
// Con Peso·z² = 9 el score da exactamente UmbralRechazo. z² mira para los
// dos lados: montos anormalmente altos y anormalmente bajos (card testing:
// cargas chiquitas para ver si la tarjeta anda).
//
// iteraciones agrega costo de CPU simulado (ver costoSimulado). monto tiene
// que ser > 0 (el handler lo valida antes).
func Puntuar(monto float64, e Estadisticas, iteraciones int) float64 {
	z := (math.Log(monto) - e.MediaLog) / e.DesvioLog
	exponente := e.Peso * z * z / (umbralSigmas * umbralSigmas)
	score := 1 - math.Pow(1-UmbralRechazo, exponente)
	return score * costoSimulado(monto, iteraciones)
}

// costoSimulado consume CPU proporcional a iteraciones y devuelve
// EXACTAMENTE 1. El Z-score real tarda microsegundos: sin este costo la
// fase opcional casi no cuesta nada y el mecanismo de slack no tendría qué
// ahorrar. Simula el costo de un modelo más pesado (como una red neuronal).
//
// Cada vuelta depende de la anterior: el CPU no puede paralelizarlas, así
// que el costo crece linealmente con iteraciones.
//
// Trampa (encontrada mirando el assembly): la primera versión arrancaba con
// acc = 1.0, y 1.0*0.999999 + 0.000001 da exactamente 1.0. El compilador
// probó que acc valía siempre 1.0 y BORRÓ el cálculo: el benchmark medía un
// loop vacío. Dos defensas:
//   - acc arranca de un valor que solo se conoce en ejecución (semilla).
//   - Se devuelve acc/acc: para un float finito y distinto de cero, IEEE
//     garantiza que x/x == 1 exacto, pero el compilador no puede probarlo
//     (acc podría ser NaN o 0), así que tiene que hacer el loop.
func costoSimulado(semilla float64, iteraciones int) float64 {
	acc := math.Abs(semilla) + 1 // > 0 siempre
	for i := 0; i < iteraciones; i++ {
		acc = acc*0.999999 + 0.000001 // positivo y finito en todo momento
	}
	return acc / acc
}
