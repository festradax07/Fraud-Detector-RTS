// Package auditoria cruza lo que respondió el motor (TransaccionEvaluada)
// con lo que registró Settlement (TransaccionLiquidada) y detecta
// discrepancias (ADR-0025). Hace VISIBLE, por ejemplo, la ambigua
// comprometida de ADR-0013: el cliente recibió error, pero la transacción
// quedó comprometida. No la corrige: conciliar está fuera de alcance.
package auditoria

import (
	"sync"
	"time"

	pb "fd_rts/proto"
)

// Resultado del cruce de una transacción.
type Resultado string

const (
	// Consistente: lo que respondió el motor coincide con lo registrado.
	Consistente Resultado = "CONSISTENTE"
	// NoComprometida: el cliente recibió error y no se comprometió nada.
	// Coherente: el cliente puede reintentar.
	NoComprometida Resultado = "NO_COMPROMETIDA"
	// AmbiguaComprometida: el cliente recibió error, pero quedó COMMITTED.
	// El cliente cree que falló una transacción que se cobró.
	AmbiguaComprometida Resultado = "AMBIGUA_COMPROMETIDA"
	// RegistroPerdido: se respondió un rechazo, pero Settlement no lo
	// registró (ADR-0012: el rechazo se responde igual).
	RegistroPerdido Resultado = "REGISTRO_PERDIDO"
	// EventoPerdido: falta el evento de un lado (por ejemplo, se descartó
	// con Kafka caído, ADR-0024).
	EventoPerdido Resultado = "EVENTO_PERDIDO"
	// Inconsistente: la decisión contradice lo registrado. No debería
	// pasar nunca (el motor obedece al registro, ADR-0013).
	Inconsistente Resultado = "INCONSISTENTE"
)

// Resultados es la lista completa, en orden de gravedad creciente.
var Resultados = []Resultado{Consistente, NoComprometida, AmbiguaComprometida, RegistroPerdido, EventoPerdido, Inconsistente}

// EsDiscrepancia dice si el resultado merece atención.
func (r Resultado) EsDiscrepancia() bool {
	return r != Consistente && r != NoComprometida
}

// Clasificar decide qué pasó con una transacción a partir de sus eventos.
// evaluadas puede tener varias (reintentos del mismo tx_id): cuenta la
// última, que es lo que el cliente terminó recibiendo. liquidada puede ser
// nil. Función pura.
func Clasificar(evaluadas []*pb.TransaccionEvaluada, liquidada *pb.TransaccionLiquidada) Resultado {
	ev := ultima(evaluadas)
	switch {
	case ev == nil:
		return EventoPerdido // hay registro, pero no sabemos qué respondió el motor

	case ev.Evaluacion == nil: // el motor no respondió una decisión (error)
		if liquidada != nil && liquidada.Estado == pb.TxStatus_STATUS_COMMITTED {
			return AmbiguaComprometida
		}
		return NoComprometida

	case liquidada == nil:
		if ev.Evaluacion.IsFraud {
			return RegistroPerdido
		}
		// Una aprobada solo se responde con el commit confirmado: si no hay
		// registro, se perdió el evento de Settlement.
		return EventoPerdido

	case ev.Evaluacion.IsFraud:
		// Un rechazo vale aunque Settlement lo haya abortado por tarde.
		if liquidada.Estado == pb.TxStatus_STATUS_REJECTED_FRAUD || liquidada.Estado == pb.TxStatus_STATUS_ABORTED_TIMEOUT {
			return Consistente
		}
		return Inconsistente

	default: // aprobada
		if liquidada.Estado == pb.TxStatus_STATUS_COMMITTED {
			return Consistente
		}
		return Inconsistente
	}
}

func ultima(evs []*pb.TransaccionEvaluada) *pb.TransaccionEvaluada {
	var u *pb.TransaccionEvaluada
	for _, e := range evs {
		if u == nil || e.EvaluadoNs > u.EvaluadoNs {
			u = e
		}
	}
	return u
}

// Cierre es una transacción ya clasificada.
type Cierre struct {
	TxID      string
	Resultado Resultado
	Evaluada  *pb.TransaccionEvaluada // la última; nil si no llegó
	Liquidada *pb.TransaccionLiquidada
}

// par junta los eventos de un tx_id mientras se espera a que lleguen todos.
type par struct {
	primero   time.Time // cuándo llegó el primer evento de este tx_id
	evaluadas []*pb.TransaccionEvaluada
	liquidada *pb.TransaccionLiquidada
}

// Cruce espera los eventos de cada transacción durante una ventana y
// después la clasifica.
//
// Por qué una ventana: los dos eventos vienen de topics (y particiones)
// distintos, publicados por servicios distintos. Pueden llegar en cualquier
// orden y separados por un tiempo arbitrario (lag, reintentos). Si en la
// ventana no llegó la pareja, se clasifica con lo que hay.
//
// Seguro para usar desde varias goroutines.
type Cruce struct {
	ventana    time.Duration
	mu         sync.Mutex
	pendientes map[string]*par
}

// NuevoCruce crea un cruce con la ventana dada.
func NuevoCruce(ventana time.Duration) *Cruce {
	return &Cruce{ventana: ventana, pendientes: map[string]*par{}}
}

// Evaluada agrega un evento del motor recibido en el instante ahora.
func (c *Cruce) Evaluada(ev *pb.TransaccionEvaluada, ahora time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.par(ev.TxId, ahora)
	p.evaluadas = append(p.evaluadas, ev)
}

// Liquidada agrega un evento de Settlement. Un duplicado (reintento) pisa
// al anterior: en Settlement gana el primer resultado final, así que traen
// lo mismo (idempotente).
func (c *Cruce) Liquidada(ev *pb.TransaccionLiquidada, ahora time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.par(ev.TxId, ahora).liquidada = ev
}

func (c *Cruce) par(txID string, ahora time.Time) *par {
	p, ok := c.pendientes[txID]
	if !ok {
		p = &par{primero: ahora}
		c.pendientes[txID] = p
	}
	return p
}

// Cerrar clasifica y saca las transacciones cuya ventana venció en ahora.
func (c *Cruce) Cerrar(ahora time.Time) []Cierre {
	c.mu.Lock()
	defer c.mu.Unlock()
	var cierres []Cierre
	for tx, p := range c.pendientes {
		if ahora.Sub(p.primero) < c.ventana {
			continue
		}
		cierres = append(cierres, Cierre{
			TxID:      tx,
			Resultado: Clasificar(p.evaluadas, p.liquidada),
			Evaluada:  ultima(p.evaluadas),
			Liquidada: p.liquidada,
		})
		delete(c.pendientes, tx)
	}
	return cierres
}

// Pendientes es cuántas transacciones esperan a cerrarse.
func (c *Cruce) Pendientes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pendientes)
}
