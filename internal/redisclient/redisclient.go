// Package redisclient crea el cliente de Redis con la configuración del hot
// path. Un solo lugar: Fraud-Engine, Settlement, el cargador de
// estadísticas y los tests usan exactamente lo mismo.
package redisclient

import "github.com/redis/go-redis/v9"

// Nuevo crea el cliente. NewClient no conecta todavía: arma un pool de
// conexiones que se abren a medida que se usan.
func Nuevo(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr: addr,
		// Sin esto go-redis IGNORA el deadline del ctx y usa sus propios
		// timeouts (3s por defecto): el deadline de 200ms no llegaría a Redis.
		ContextTimeoutEnabled: true,
		// Sin reintentos: con Redis caído, fallar en microsegundos y no quemar
		// el deadline entero esperando (load shedding también ante fallas).
		// Fail-closed igual, y el cliente puede reintentar con el mismo tx_id
		// sin que cuente doble (idempotencia).
		//
		// MaxRetries: -1 desactiva los reintentos de comando (0 = default 3).
		MaxRetries: -1,
		// DialerRetries cuenta INTENTOS totales, no reintentos (el código es
		// `for attempt < DialerRetries`), y 0 = default 5. 1 = un solo intento.
		DialerRetries: 1,
	})
}
