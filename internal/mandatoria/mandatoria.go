// Package mandatoria implementa la fase mandatoria (M_i) del Fraud-Engine:
// blacklist, control de velocidad y viaje imposible.
//
// Etapa 2: todo el estado vive en memoria. En la Etapa 3 se migra a Redis.
package mandatoria

import (
	"math"
	"sync"
	"time"
)

// Reglas del control de velocidad (card testing): más de MaxIntentos
// intentos de la misma tarjeta dentro de VentanaVelocidad es fraude.
const (
	VentanaVelocidad = 10 * time.Second
	MaxIntentos      = 3
)

// Regla de viaje imposible: moverse más rápido que esto entre dos compras
// de la misma tarjeta es fraude.
const VelocidadMaxKmh = 800.0

// DistanciaMinimaKm: por debajo de esto el desplazamiento se considera ruido
// de geolocalización (IP o dirección del comercio: precisión de ciudad) y no
// se evalúa velocidad. Sin esto, dos compras en el mismo shopping con 2s y
// 1km de diferencia darían 1800 km/h. 50km es un PUNTO DE PARTIDA de diseño,
// no un valor medido.
const DistanciaMinimaKm = 50.0

// radioTierraKm es el radio medio de la Tierra (constante física).
const radioTierraKm = 6371.0

// posicion es la última ubicación confiable de una tarjeta: la de su última
// transacción APROBADA.
type posicion struct {
	lat, lon float64
	momento  time.Time
}

// Verificador guarda el estado que la fase mandatoria necesita recordar
// entre requests.
type Verificador struct {
	// blacklist es un set: la clave es el card_token, el valor siempre true.
	// Se carga una sola vez en NuevoVerificador y después solo se lee, por
	// eso no necesita mutex: leer un map desde varias goroutines es seguro
	// mientras nadie escriba.
	blacklist map[string]bool

	// mu protege a intentos y posiciones. El server gRPC atiende cada
	// request en su propia goroutine, y estos maps se leen Y escriben en
	// cada request.
	mu sync.Mutex
	// intentos guarda, por card_token, los timestamps de los intentos
	// dentro de la ventana (aprobados Y rechazados: el card testing son
	// justamente muchos intentos fallidos).
	intentos map[string][]time.Time
	// posiciones guarda, por card_token, la última posición confiable.
	// Solo se actualiza con transacciones aprobadas: si guardáramos la de
	// un intento rechazado, el atacante "movería" la tarjeta a su ubicación
	// y su próximo intento desde ahí pasaría el chequeo.
	posiciones map[string]posicion
}

// NuevoVerificador arma un Verificador con las tarjetas bloqueadas dadas.
func NuevoVerificador(tarjetasBloqueadas []string) *Verificador {
	bl := make(map[string]bool, len(tarjetasBloqueadas))
	for _, t := range tarjetasBloqueadas {
		bl[t] = true
	}
	return &Verificador{
		blacklist:  bl,
		intentos:   make(map[string][]time.Time),
		posiciones: make(map[string]posicion),
	}
}

// RegistrarIntento anota un intento de la tarjeta en el instante ahora y
// devuelve cuántos intentos hay dentro de la ventana, incluido este.
// Quien llama decide: más de MaxIntentos es fraude.
//
// ahora viene de afuera (en vez de llamar a time.Now() acá) para que los
// tests controlen el tiempo y el resultado sea determinista.
func (v *Verificador) RegistrarIntento(cardToken string, ahora time.Time) int {
	v.mu.Lock()
	defer v.mu.Unlock()

	limite := ahora.Add(-VentanaVelocidad)

	// Filtrado in-place: recientes comparte el array de fondo con el slice
	// original, así que no se aloca memoria nueva para descartar los viejos.
	historial := v.intentos[cardToken]
	recientes := historial[:0]
	for _, t := range historial {
		if t.After(limite) {
			recientes = append(recientes, t)
		}
	}
	recientes = append(recientes, ahora)

	v.intentos[cardToken] = recientes
	return len(recientes)
}

// EnBlacklist dice si la tarjeta está bloqueada. Lookup O(1).
//
// Si la clave no existe, el map devuelve el zero value de bool (false), así
// que no hace falta el "comma ok": "no está" y "no bloqueada" son lo mismo.
func (v *Verificador) EnBlacklist(cardToken string) bool {
	return v.blacklist[cardToken]
}

// CoordenadasValidas dice si (lat, lon) es una ubicación usable.
//
// En proto3 un double no enviado llega como 0, indistinguible de un 0 real.
// Por eso (0, 0) exacto se trata como "no vino": es océano abierto en el
// Golfo de Guinea, ningún comercio real está ahí. Sin este chequeo, una
// compra sin coordenadas "viajaría" hasta ahí y daría viaje imposible.
func CoordenadasValidas(lat, lon float64) bool {
	if lat == 0 && lon == 0 {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// DistanciaKm calcula la distancia sobre la superficie terrestre entre dos
// puntos (grados decimales) con la fórmula de Haversine:
//
//	a = sin²(Δφ/2) + cos φ1 · cos φ2 · sin²(Δλ/2)
//	d = 2R · asin(√a)
//
// Es una función pura: mismo input, mismo output, sin estado ni reloj.
func DistanciaKm(lat1, lon1, lat2, lon2 float64) float64 {
	// math trabaja en radianes, las coordenadas vienen en grados.
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	dPhi := (lat2 - lat1) * math.Pi / 180
	dLambda := (lon2 - lon1) * math.Pi / 180

	sinDPhi := math.Sin(dPhi / 2)
	sinDLambda := math.Sin(dLambda / 2)
	a := sinDPhi*sinDPhi + math.Cos(phi1)*math.Cos(phi2)*sinDLambda*sinDLambda

	return 2 * radioTierraKm * math.Asin(math.Sqrt(a))
}

// VelocidadDesdeUltima devuelve la velocidad (km/h) que implica ir desde la
// última posición confiable de la tarjeta hasta (lat, lon) en el instante
// ahora. hayPrevia es false si la tarjeta no tiene posición guardada: la
// primera compra no tiene con qué compararse.
//
// Solo consulta, no modifica nada. Quien llama decide: más de
// VelocidadMaxKmh es fraude.
func (v *Verificador) VelocidadDesdeUltima(cardToken string, lat, lon float64, ahora time.Time) (kmh float64, hayPrevia bool) {
	v.mu.Lock()
	ultima, ok := v.posiciones[cardToken]
	v.mu.Unlock()
	// El cálculo va fuera del lock: ya tenemos una copia de ultima (es un
	// struct por valor), no hace falta bloquear a otras requests mientras
	// hacemos trigonometría.

	if !ok {
		return 0, false
	}

	distancia := DistanciaKm(ultima.lat, ultima.lon, lat, lon)

	// Desplazamiento dentro del margen de ruido: es "el mismo lugar".
	if distancia < DistanciaMinimaKm {
		return 0, true
	}

	// Mismo instante o timestamps desordenados: no se puede dividir, y hubo
	// desplazamiento real. Velocidad infinita (fail-closed: se rechaza).
	horas := ahora.Sub(ultima.momento).Hours()
	if horas <= 0 {
		return math.Inf(1), true
	}

	return distancia / horas, true
}

// ActualizarPosicion guarda (lat, lon, ahora) como la última posición
// confiable de la tarjeta. Llamar SOLO con transacciones aprobadas.
func (v *Verificador) ActualizarPosicion(cardToken string, lat, lon float64, ahora time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.posiciones[cardToken] = posicion{lat: lat, lon: lon, momento: ahora}
}
