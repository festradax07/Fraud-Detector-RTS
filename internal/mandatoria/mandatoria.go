// Package mandatoria implementa la fase mandatoria (M_i) del Fraud-Engine:
// blacklist, control de velocidad y viaje imposible.
//
// Etapa 3: el estado vive en Redis. Los tres datos que necesita la fase se
// traen en UN solo viaje de red (TxPipeline).
package mandatoria

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
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

// ttlPosicion: después de este tiempo la última posición ya no puede generar
// un rechazo, porque a VelocidadMaxKmh se llega a cualquier punto de la
// Tierra (la distancia máxima es media circunferencia, πR ≈ 20.015km, o sea
// ≈ 25.02h). Sale de las constantes de arriba, no es un número elegido; se
// redondea hacia arriba a horas enteras (26h): más largo es lo conservador.
//
// Trampa de compilación: time.Duration(πR/800 * float64(time.Hour)) no
// compila ni siquiera como var, porque toda la expresión es constante y Go
// verifica en compilación que una constante no entera no se trunque.
// math.Ceil es una llamada a función, así que se evalúa al ejecutar.
var ttlPosicion = time.Duration(math.Ceil(math.Pi*radioTierraKm/VelocidadMaxKmh)) * time.Hour

// Posicion es la última ubicación confiable de una tarjeta: la de su última
// transacción APROBADA.
type Posicion struct {
	Lat, Lon float64
	Momento  time.Time
}

// Resultado es lo que la fase mandatoria necesita para decidir. Se arma con
// un solo viaje a Redis.
type Resultado struct {
	EnBlacklist bool
	// Intentos en la ventana, incluido el actual.
	Intentos int64
	// HayPrevia es false si la tarjeta no tiene posición confiable guardada
	// (primera compra, o pasó más de ttlPosicion).
	HayPrevia bool
	// Kmh es la velocidad implícita desde la posición previa. Solo tiene
	// sentido si HayPrevia.
	Kmh float64
	// StatsCategoria es el hash crudo de estadísticas de la categoría, para
	// la fase opcional (lo interpreta opcional.ParsearEstadisticas). Vacío
	// si la categoría no existe. Viaja en el mismo pipeline aunque después
	// la request se degrade: son unos bytes en el mismo viaje, y pedirlo
	// después costaría un viaje de red entero.
	StatsCategoria map[string]string
}

// Verificador ejecuta la fase mandatoria contra Redis.
//
// Ya no tiene mutex: el estado no vive en el proceso sino en Redis, que
// ejecuta los comandos de a uno. La concurrencia la ordena Redis.
type Verificador struct {
	rdb *redis.Client
	// prefijo separa las claves de este Verificador de cualquier otra cosa
	// en el mismo Redis ("drts:" en el server, uno único por test).
	prefijo string
}

// NuevoClienteRedis crea el cliente de Redis con la configuración del hot
// path. Un solo lugar: el server y los tests usan exactamente lo mismo.
//
// NewClient no conecta todavía: arma un pool de conexiones que se abren a
// medida que se usan.
func NuevoClienteRedis(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: addr,
		// Sin esto go-redis IGNORA el deadline del ctx y usa sus propios
		// timeouts (3s por defecto): el deadline de 200ms no llegaría a Redis.
		ContextTimeoutEnabled: true,
		// Sin reintentos: con Redis caído, fallar en microsegundos y no quemar
		// el deadline entero esperando (load shedding también ante fallas).
		// Fail-closed igual, y el cliente puede reintentar con el mismo tx_id
		// sin que cuente doble (idempotencia del ZSET).
		//
		// MaxRetries: -1 desactiva los reintentos de comando (0 = default 3).
		MaxRetries: -1,
		// DialerRetries cuenta INTENTOS totales, no reintentos (el código es
		// `for attempt < DialerRetries`), y 0 = default 5. 1 = un solo intento.
		DialerRetries: 1,
	})
}

// NuevoVerificador arma un Verificador sobre un cliente de Redis ya creado.
func NuevoVerificador(rdb *redis.Client, prefijo string) *Verificador {
	return &Verificador{rdb: rdb, prefijo: prefijo}
}

// Claves de Redis. Una función por clave para que el formato esté en un
// solo lugar.
func (v *Verificador) claveBlacklist() string             { return v.prefijo + "blacklist" }
func (v *Verificador) claveVelocidad(card string) string  { return v.prefijo + "vel:" + card }
func (v *Verificador) clavePosicion(card string) string   { return v.prefijo + "pos:" + card }
func (v *Verificador) claveStats(categoria string) string { return v.prefijo + "stats:" + categoria }

// Bloquear agrega una tarjeta a la blacklist (SADD).
func (v *Verificador) Bloquear(ctx context.Context, cardToken string) error {
	return v.rdb.SAdd(ctx, v.claveBlacklist(), cardToken).Err()
}

// GuardarEstadisticas escribe el hash de estadísticas de una categoría
// (HSET). Lo usan cmd/cargar-stats y los tests; el hot path solo lee.
func (v *Verificador) GuardarEstadisticas(ctx context.Context, categoria string, campos map[string]any) error {
	return v.rdb.HSet(ctx, v.claveStats(categoria), campos).Err()
}

// Consultar registra el intento y trae todo lo que la fase mandatoria
// necesita, en UN solo viaje a Redis:
//
//	SISMEMBER                                  → ¿blacklist?
//	ZREMRANGEBYSCORE + ZADD + ZCARD + EXPIRE   → intentos en la ventana
//	GET                                        → última posición confiable
//	HGETALL                                    → estadísticas de la categoría
//	                                             (para la fase opcional)
//
// Va en un TxPipeline (MULTI/EXEC): un solo viaje como cualquier pipeline,
// y además atómico, sin comandos de otros clientes intercalados.
//
// No decide nada: quien llama compara contra MaxIntentos y VelocidadMaxKmh.
// Un error significa que NO se pudo verificar (fail-closed: no aprobar).
func (v *Verificador) Consultar(ctx context.Context, txID, cardToken, categoria string, lat, lon float64, ahora time.Time) (Resultado, error) {
	claveVel := v.claveVelocidad(cardToken)

	// Scores en milisegundos: son double en Redis (exactos hasta ~9e15);
	// UnixNano (~1.8e18) perdería precisión, UnixMilli (~1.8e12) no.
	ahoraMs := ahora.UnixMilli()
	limiteMs := ahora.Add(-VentanaVelocidad).UnixMilli()

	pipe := v.rdb.TxPipeline()
	// Cada llamada sobre pipe NO va a Redis: se encola. Lo que devuelven son
	// "promesas" (*BoolCmd, *IntCmd...) que tienen valor recién después de
	// Exec.
	enBlacklist := pipe.SIsMember(ctx, v.claveBlacklist(), cardToken)
	// ZREMRANGEBYSCORE es inclusivo: borra también el de exactamente 10s.
	// Ventana = (ahora-10s, ahora], igual que en la Etapa 2.
	pipe.ZRemRangeByScore(ctx, claveVel, "-inf", strconv.FormatInt(limiteMs, 10))
	// Miembro = tx_id (los miembros de un ZSET son únicos: con el timestamp
	// como miembro, dos intentos en el mismo ms se fundirían en uno).
	pipe.ZAdd(ctx, claveVel, redis.Z{Score: float64(ahoraMs), Member: txID})
	intentos := pipe.ZCard(ctx, claveVel)
	// Si la tarjeta no vuelve, Redis borra la clave sola: adiós a la fuga
	// de memoria de la Etapa 2.
	pipe.Expire(ctx, claveVel, VentanaVelocidad)
	posGuardada := pipe.Get(ctx, v.clavePosicion(cardToken))
	stats := pipe.HGetAll(ctx, v.claveStats(categoria))

	// Recién acá viaja todo junto: 1 RTT.
	//
	// GET de una clave inexistente devuelve redis.Nil, y Exec lo reporta
	// como error. No es una falla: es "no hay posición previa".
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return Resultado{}, fmt.Errorf("pipeline de fase mandatoria: %w", err)
	}

	res := Resultado{
		EnBlacklist:    enBlacklist.Val(),
		Intentos:       intentos.Val(),
		StatsCategoria: stats.Val(), // HGETALL de una clave inexistente: map vacío
	}

	valor, err := posGuardada.Result()
	if errors.Is(err, redis.Nil) {
		return res, nil // primera compra: nada con qué comparar
	}
	if err != nil {
		return Resultado{}, fmt.Errorf("leyendo posición: %w", err)
	}

	previa, err := decodificarPosicion(valor)
	if err != nil {
		// Un valor corrupto no se ignora: sin posición confiable no se puede
		// verificar viaje imposible. Fail-closed.
		return Resultado{}, err
	}
	res.HayPrevia = true
	res.Kmh = VelocidadKmh(previa, lat, lon, ahora)
	return res, nil
}

// ActualizarPosicion guarda (lat, lon, ahora) como la última posición
// confiable de la tarjeta, con TTL ttlPosicion. Llamar SOLO con
// transacciones aprobadas (es el viaje 2: depende del resultado del 1).
func (v *Verificador) ActualizarPosicion(ctx context.Context, cardToken string, lat, lon float64, ahora time.Time) error {
	valor := codificarPosicion(Posicion{Lat: lat, Lon: lon, Momento: ahora})
	return v.rdb.Set(ctx, v.clavePosicion(cardToken), valor, ttlPosicion).Err()
}

// codificarPosicion arma el string "lat,lon,unixMilli" que se guarda en
// Redis. 'f' con precisión -1 = la mínima cantidad de dígitos que vuelve a
// dar exactamente el mismo float64 al parsear.
func codificarPosicion(p Posicion) string {
	return strconv.FormatFloat(p.Lat, 'f', -1, 64) + "," +
		strconv.FormatFloat(p.Lon, 'f', -1, 64) + "," +
		strconv.FormatInt(p.Momento.UnixMilli(), 10)
}

// decodificarPosicion es la inversa de codificarPosicion.
func decodificarPosicion(s string) (Posicion, error) {
	partes := strings.Split(s, ",")
	if len(partes) != 3 {
		return Posicion{}, fmt.Errorf("posición mal formada: %q", s)
	}
	lat, err := strconv.ParseFloat(partes[0], 64)
	if err != nil {
		return Posicion{}, fmt.Errorf("latitud mal formada en %q: %w", s, err)
	}
	lon, err := strconv.ParseFloat(partes[1], 64)
	if err != nil {
		return Posicion{}, fmt.Errorf("longitud mal formada en %q: %w", s, err)
	}
	ms, err := strconv.ParseInt(partes[2], 10, 64)
	if err != nil {
		return Posicion{}, fmt.Errorf("momento mal formado en %q: %w", s, err)
	}
	return Posicion{Lat: lat, Lon: lon, Momento: time.UnixMilli(ms)}, nil
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

// VelocidadKmh devuelve la velocidad (km/h) que implica ir desde previa
// hasta (lat, lon) en el instante ahora. Función pura (en la Etapa 2 vivía
// dentro de VelocidadDesdeUltima, mezclada con la lectura del map).
func VelocidadKmh(previa Posicion, lat, lon float64, ahora time.Time) float64 {
	distancia := DistanciaKm(previa.Lat, previa.Lon, lat, lon)

	// Desplazamiento dentro del margen de ruido: es "el mismo lugar".
	if distancia < DistanciaMinimaKm {
		return 0
	}

	// Mismo instante o timestamps desordenados: no se puede dividir, y hubo
	// desplazamiento real. Velocidad infinita (fail-closed: se rechaza).
	horas := ahora.Sub(previa.Momento).Hours()
	if horas <= 0 {
		return math.Inf(1)
	}

	return distancia / horas
}
