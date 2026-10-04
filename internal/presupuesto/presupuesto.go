// Package presupuesto concentra el modelo temporal del Fraud-Engine: los
// presupuestos de tiempo, el control de admisión y el slack time.
//
// Todas son funciones puras: reciben duraciones y no leen el reloj ni el
// context. Quien llama calcula D_rem = deadline - ahora y se lo pasa.
//
// Los valores son PUNTOS DE PARTIDA de diseño (CLAUDE.md), no mediciones:
// se recalibran midiendo en la Etapa 10.
package presupuesto

import "time"

const (
	// DeadlineGlobal es el deadline duro de extremo a extremo de cada
	// transacción: D_i = a_i + 200ms. Una respuesta correcta que llega
	// después cuenta como falla.
	DeadlineGlobal = 200 * time.Millisecond

	// WCETMandatoria es el peor tiempo de ejecución presupuestado para la
	// fase mandatoria (M_i).
	WCETMandatoria = 20 * time.Millisecond

	// WCETOpcional es el peor tiempo de ejecución presupuestado para la fase
	// opcional (O_i): el scoring de riesgo.
	WCETOpcional = 80 * time.Millisecond

	// ReservaSettleNet es C_settle + C_net: lo que hay que reservar para el
	// commit en Settlement y los viajes de red. Sale de despejar el umbral de
	// admisión del diseño: 35ms - WCET(M) = 15ms. Van juntos hasta la
	// Etapa 5, donde Settlement existe y C_net se mide por primera vez.
	ReservaSettleNet = 15 * time.Millisecond

	// MinParaAdmitir es el mínimo que tiene que quedar para que valga la
	// pena empezar: la mandatoria más lo que viene después de ella.
	// = WCET(M) + C_settle + C_net = 35ms.
	MinParaAdmitir = WCETMandatoria + ReservaSettleNet
)

// Admitir dice si una transacción con dRem de tiempo restante se procesa
// (control de admisión, Etapa 1). Si no alcanza para la mandatoria más
// Settlement y red, se rechaza sin hacer nada: load shedding.
func Admitir(dRem time.Duration) bool {
	return dRem >= MinParaAdmitir
}

// Slack calcula S_i = D_rem - C_settle - C_net - WCET(O_i): cuánto tiempo
// sobraría si corriéramos la fase opcional en su peor caso y después
// Settlement. Puede ser negativo.
//
// dRem tiene que medirse DESPUÉS de la fase mandatoria: lo que importa es
// cuánto queda ahora, con la mandatoria ya pagada.
func Slack(dRem time.Duration) time.Duration {
	return dRem - ReservaSettleNet - WCETOpcional
}

// CorreOpcional dice si hay tiempo para la fase opcional: S_i >= 0. Si no,
// la respuesta sale solo con la mandatoria y marcada como degradada.
func CorreOpcional(dRem time.Duration) bool {
	return Slack(dRem) >= 0
}
