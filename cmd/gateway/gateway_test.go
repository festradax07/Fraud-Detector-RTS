package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"

	"fd_rts/internal/mandatoria"
	pb "fd_rts/proto"
)

func TestArmarEscenario(t *testing.T) {
	n := 0
	sig := func() string { n++; return fmt.Sprintf("tx%d", n) }

	t.Run("card testing: 4 intentos con la tarjeta del celular", func(t *testing.T) {
		cs, err := armarEscenario("card_testing", "sesion1234", 0, 7, sig)
		if err != nil || len(cs) != 4 {
			t.Fatalf("got %d compras, err=%v; se esperaban 4", len(cs), err)
		}
		for _, c := range cs {
			if c.req.CardToken != TarjetaDe("sesion1234", 0) {
				t.Errorf("tarjeta %q, se esperaba la del celular en los 4 intentos", c.req.CardToken)
			}
		}
		// Más de MaxIntentos: la última tiene que caer en la regla.
		if len(cs) <= mandatoria.MaxIntentos {
			t.Errorf("%d intentos no superan MaxIntentos=%d", len(cs), mandatoria.MaxIntentos)
		}
	})

	t.Run("viaje imposible: misma tarjeta, Buenos Aires y después Tokio", func(t *testing.T) {
		cs, _ := armarEscenario("viaje_imposible", "sesion1234", 0, 8, sig)
		if len(cs) != 2 || cs[0].req.CardToken != cs[1].req.CardToken {
			t.Fatalf("se esperaban 2 compras con la misma tarjeta: %+v", cs)
		}
		if cs[0].req.Latitude != buenosAires.lat || cs[1].req.Latitude != tokio.lat {
			t.Errorf("lugares = %v y %v", cs[0].req.Latitude, cs[1].req.Latitude)
		}
	})

	t.Run("pedir una tarjeta nueva cambia la tarjeta; la del celular es siempre la misma", func(t *testing.T) {
		a, _ := armarEscenario("normal", "sesion1234", 0, 1, sig)
		b, _ := armarEscenario("normal", "sesion1234", 0, 2, sig)
		c, _ := armarEscenario("normal", "sesion1234", 1, 3, sig)
		if a[0].req.CardToken != b[0].req.CardToken {
			t.Errorf("dos compras con la misma tarjeta del celular usaron tarjetas distintas")
		}
		if a[0].req.CardToken == c[0].req.CardToken {
			t.Errorf("la tarjeta nueva es igual a la anterior: %q", c[0].req.CardToken)
		}
		if _, err := armarEscenario("normal", "sesion1234", maxTarjetas, 4, sig); err == nil {
			t.Error("se aceptó un número de tarjeta fuera de rango")
		}
	})

	t.Run("tarjeta robada usa la de la blacklist de la demo", func(t *testing.T) {
		cs, _ := armarEscenario("tarjeta_robada", "sesion1234", 0, 9, sig)
		if cs[0].req.CardToken != TarjetaRobada {
			t.Errorf("tarjeta %q, se esperaba %q", cs[0].req.CardToken, TarjetaRobada)
		}
	})

	t.Run("cada transacción tiene un tx_id distinto", func(t *testing.T) {
		vistos := map[string]bool{}
		for _, e := range escenarios {
			cs, err := armarEscenario(e, "sesion1234", 0, 10, sig)
			if err != nil {
				t.Fatalf("%s: %v", e, err)
			}
			for _, c := range cs {
				if vistos[c.req.TxId] {
					t.Errorf("tx_id repetido: %s", c.req.TxId)
				}
				vistos[c.req.TxId] = true
			}
		}
	})

	if _, err := armarEscenario("otro", "sesion1234", 0, 1, sig); err == nil {
		t.Error("se aceptó un escenario desconocido")
	}
}

func TestLimitador(t *testing.T) {
	ahora := time.Unix(0, 0)
	l := nuevoLimitador(2, 3)
	l.ahora = func() time.Time { return ahora }
	for i := 0; i < 3; i++ {
		if !l.permitir("ip") {
			t.Fatalf("la ráfaga de 3 tendría que pasar (falló la %d)", i+1)
		}
	}
	if l.permitir("ip") {
		t.Error("la cuarta seguida tendría que rechazarse")
	}
	if !l.permitir("otra-ip") {
		t.Error("otra IP tiene su propio balde")
	}
	ahora = ahora.Add(500 * time.Millisecond) // 2 por segundo → 1 ficha
	if !l.permitir("ip") || l.permitir("ip") {
		t.Error("en medio segundo se recarga exactamente una ficha")
	}
}

// motorDePrueba responde según la tarjeta y anota el deadline que recibió.
// El motor real se prueba en cmd/server contra Redis real; acá se prueba la
// capa HTTP del gateway.
type motorDePrueba struct {
	deadlines []time.Duration
}

func (m *motorDePrueba) EvaluateTransaction(ctx context.Context, in *pb.TransactionRequest, _ ...grpc.CallOption) (*pb.EvaluationResponse, error) {
	if d, ok := ctx.Deadline(); ok {
		m.deadlines = append(m.deadlines, time.Until(d))
	}
	if in.CardToken == TarjetaRobada {
		return &pb.EvaluationResponse{TxId: in.TxId, IsFraud: true, RejectionReason: mandatoria.MotivoBlacklist}, nil
	}
	return &pb.EvaluationResponse{TxId: in.TxId}, nil
}

func nuevoGatewayDePrueba() (*gateway, *motorDePrueba) {
	m := &motorDePrueba{}
	return &gateway{motor: m, token: "secreto", deadline: 200 * time.Millisecond,
		limite: nuevoLimitador(2, 6), limiteIP: nuevoLimitador(40, 80), conteos: map[string]int{}}, m
}

func pedir(t *testing.T, h http.Handler, metodo, url, cuerpo string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(metodo, url, strings.NewReader(cuerpo))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCompra(t *testing.T) {
	g, m := nuevoGatewayDePrueba()
	h := g.rutas()

	if rec := pedir(t, h, "POST", "/api/compra?t=otro", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("sin el token correcto: código %d, se esperaba 403", rec.Code)
	}
	if rec := pedir(t, h, "POST", "/api/compra?t=secreto", `{"sesion":"x","escenario":"normal"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("sesión inválida: código %d, se esperaba 400", rec.Code)
	}

	rec := pedir(t, h, "POST", "/api/compra?t=secreto", `{"sesion":"sesion1234","escenario":"tarjeta_robada"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		Resultados []resultado `json:"resultados"`
		DeadlineMs int64       `json:"deadline_ms"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Resultados) != 1 || resp.Resultados[0].Decision != "rechazada" || resp.Resultados[0].Motivo != mandatoria.MotivoBlacklist {
		t.Errorf("resultados = %+v", resp.Resultados)
	}
	if resp.DeadlineMs != 200 {
		t.Errorf("deadline_ms = %d", resp.DeadlineMs)
	}

	// El deadline de 200ms lo pone el gateway: es el borde del sistema.
	if len(m.deadlines) == 0 || m.deadlines[0] > 200*time.Millisecond || m.deadlines[0] < 150*time.Millisecond {
		t.Errorf("deadline que llegó al motor = %v, se esperaba ~200ms", m.deadlines)
	}
}

// El límite es por celular (sesión): un celular que aprieta sin parar se
// frena, pero muchos celulares detrás de la misma IP (el NAT del aula) no se
// frenan entre sí.
func TestLimiteDelEndpoint(t *testing.T) {
	g, _ := nuevoGatewayDePrueba()
	h := g.rutas()
	codigos := map[int]int{}
	for i := 0; i < 10; i++ {
		codigos[pedir(t, h, "POST", "/api/compra?t=secreto", `{"sesion":"sesion1234","escenario":"normal"}`).Code]++
	}
	if codigos[http.StatusOK] != 6 || codigos[http.StatusTooManyRequests] != 4 {
		t.Errorf("un celular: códigos = %v, se esperaban 6 OK (la ráfaga) y 4 rechazadas", codigos)
	}

	// 30 celulares distintos, desde la misma IP (httptest usa siempre la misma).
	for i := 0; i < 30; i++ {
		cuerpo := fmt.Sprintf(`{"sesion":"celular%04d","escenario":"normal"}`, i)
		if rec := pedir(t, h, "POST", "/api/compra?t=secreto", cuerpo); rec.Code != http.StatusOK {
			t.Fatalf("celular %d detrás del mismo NAT: código %d (%s)", i, rec.Code, rec.Body)
		}
	}
}

func TestPaginasYQR(t *testing.T) {
	g, _ := nuevoGatewayDePrueba()
	h := g.rutas()
	for _, url := range []string{"/?t=secreto", "/presentador?t=secreto"} {
		if rec := pedir(t, h, "GET", url, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("%s: código %d", url, rec.Code)
		}
	}
	rec := pedir(t, h, "GET", "/qr.png?t=secreto", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("qr: código %d, tipo %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := pedir(t, h, "GET", "/?t=mal", ""); rec.Code != http.StatusForbidden {
		t.Errorf("página sin token: código %d, se esperaba 403", rec.Code)
	}
}

// Las métricas por tarjeta: una sola serie por tarjeta aunque cambie de
// estado (si no, la tabla la mostraría dos veces), y la hora de la primera
// compra fija (ordena la tabla: las nuevas arriba).
func TestMetricasPorTarjeta(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := nuevasMetricasDemo(reg)
	token := TarjetaDe("sesion1234", 0)
	numero := NumeroDeFantasia(token)

	m.registrar("normal", token, resultado{Decision: "aprobada", SistemaMs: 18})
	alta := testutil.ToFloat64(m.porAlta.WithLabelValues(numero, estadoActiva))
	time.Sleep(5 * time.Millisecond)
	m.registrar("card_testing", token, resultado{Decision: "rechazada", Motivo: mandatoria.MotivoCardTesting, SistemaMs: 1})

	if n := testutil.CollectAndCount(m.porComp); n != 1 {
		t.Fatalf("%d series de compras para una tarjeta, se esperaba 1 (la del estado actual)", n)
	}
	if got := testutil.ToFloat64(m.porComp.WithLabelValues(numero, estadoRevision)); got != 2 {
		t.Errorf("compras = %v, se esperaban 2", got)
	}
	if got := testutil.ToFloat64(m.porAlta.WithLabelValues(numero, estadoRevision)); got != alta {
		t.Errorf("la hora de la primera compra cambió: %v → %v", alta, got)
	}

	// La tarjeta robada es de "otra persona": no entra en la tabla.
	m.registrar("tarjeta_robada", TarjetaRobada, resultado{Decision: "rechazada", Motivo: mandatoria.MotivoBlacklist})
	if n := testutil.CollectAndCount(m.porComp); n != 1 {
		t.Errorf("la tarjeta robada apareció en la tabla (%d series)", n)
	}
}
