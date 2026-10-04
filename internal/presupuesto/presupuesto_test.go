package presupuesto

import (
	"testing"
	"time"
)

const ms = time.Millisecond

// Los bordes se prueban a 1µs de distancia: es donde se esconden los
// errores de "<" contra "<=".

func TestAdmitir(t *testing.T) {
	casos := []struct {
		nombre string
		dRem   time.Duration
		want   bool
	}{
		{"deadline normal", 200 * ms, true},
		{"justo el mínimo: se admite", 35 * ms, true},
		{"1µs menos que el mínimo: se rechaza", 35*ms - time.Microsecond, false},
		{"muy por debajo", 5 * ms, false},
		{"deadline ya vencido", -1 * ms, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Admitir(c.dRem); got != c.want {
				t.Errorf("Admitir(%v) = %v, se esperaba %v", c.dRem, got, c.want)
			}
		})
	}
}

func TestSlack(t *testing.T) {
	casos := []struct {
		nombre       string
		dRem         time.Duration
		wantSlack    time.Duration
		wantOpcional bool
	}{
		// 199 - 15 - 80 = 104: el caso típico con deadline de 200ms.
		{"deadline normal tras la mandatoria", 199 * ms, 104 * ms, true},
		// 15 + 80 = 95: el punto exacto donde la opcional entra justo.
		{"justo 95ms: slack 0, corre", 95 * ms, 0, true},
		{"1µs menos: slack negativo, degradada", 95*ms - time.Microsecond, -time.Microsecond, false},
		// Admitida (>= 35ms) pero sin lugar para la opcional.
		{"60ms: admitida pero degradada", 60 * ms, -35 * ms, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Slack(c.dRem); got != c.wantSlack {
				t.Errorf("Slack(%v) = %v, se esperaba %v", c.dRem, got, c.wantSlack)
			}
			if got := CorreOpcional(c.dRem); got != c.wantOpcional {
				t.Errorf("CorreOpcional(%v) = %v, se esperaba %v", c.dRem, got, c.wantOpcional)
			}
		})
	}
}

// Los presupuestos tienen que ser coherentes entre sí: si alguien cambia
// uno, este test avisa si dejó de cerrar la cuenta del diseño.
func TestCoherenciaDelDiseno(t *testing.T) {
	if MinParaAdmitir != 35*ms {
		t.Errorf("MinParaAdmitir = %v, el diseño parte de 35ms", MinParaAdmitir)
	}
	// Lo peor que puede tardar una transacción completa tiene que entrar en
	// el deadline global de 200ms.
	peorCaso := WCETMandatoria + WCETOpcional + ReservaSettleNet
	if peorCaso > 200*ms {
		t.Errorf("M + O + settle + net = %v, no entra en 200ms", peorCaso)
	}
}
