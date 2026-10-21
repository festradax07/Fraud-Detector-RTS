package auditoria

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "fd_rts/proto"
)

func evaluada(ns int64, fraude bool) *pb.TransaccionEvaluada {
	return &pb.TransaccionEvaluada{TxId: "t1", EvaluadoNs: ns, Evaluacion: &pb.EvaluationResponse{IsFraud: fraude}}
}

func sinDecision(ns int64) *pb.TransaccionEvaluada {
	return &pb.TransaccionEvaluada{TxId: "t1", EvaluadoNs: ns, ErrorMotivo: "SETTLEMENT_TIMEOUT"}
}

func liquidada(estado pb.TxStatus) *pb.TransaccionLiquidada {
	return &pb.TransaccionLiquidada{TxId: "t1", Estado: estado}
}

const (
	commit  = pb.TxStatus_STATUS_COMMITTED
	rechazo = pb.TxStatus_STATUS_REJECTED_FRAUD
	abort   = pb.TxStatus_STATUS_ABORTED_TIMEOUT
)

func TestClasificar(t *testing.T) {
	casos := []struct {
		nombre    string
		evaluadas []*pb.TransaccionEvaluada
		liquidada *pb.TransaccionLiquidada
		want      Resultado
	}{
		{"aprobada y comprometida", []*pb.TransaccionEvaluada{evaluada(1, false)}, liquidada(commit), Consistente},
		{"rechazada y registrada", []*pb.TransaccionEvaluada{evaluada(1, true)}, liquidada(rechazo), Consistente},
		{"rechazada y abortada por tarde: el rechazo vale", []*pb.TransaccionEvaluada{evaluada(1, true)}, liquidada(abort), Consistente},
		{"error y nada comprometido", []*pb.TransaccionEvaluada{sinDecision(1)}, nil, NoComprometida},
		{"error y abortada", []*pb.TransaccionEvaluada{sinDecision(1)}, liquidada(abort), NoComprometida},
		// El caso de ADR-0013.
		{"error pero comprometida", []*pb.TransaccionEvaluada{sinDecision(1)}, liquidada(commit), AmbiguaComprometida},
		// El cliente reintentó y la segunda vez recibió la decisión: resuelta.
		{"ambigua resuelta por un reintento", []*pb.TransaccionEvaluada{sinDecision(1), evaluada(2, false)}, liquidada(commit), Consistente},
		// El orden de llegada no importa: cuenta la última por instante.
		{"reintento que llegó primero", []*pb.TransaccionEvaluada{evaluada(2, false), sinDecision(1)}, liquidada(commit), Consistente},
		{"rechazo sin registro", []*pb.TransaccionEvaluada{evaluada(1, true)}, nil, RegistroPerdido},
		{"aprobada sin registro: se perdió el evento", []*pb.TransaccionEvaluada{evaluada(1, false)}, nil, EventoPerdido},
		{"registro sin evaluación: se perdió el evento", nil, liquidada(commit), EventoPerdido},
		{"aprobada pero registrada como rechazo", []*pb.TransaccionEvaluada{evaluada(1, false)}, liquidada(rechazo), Inconsistente},
		{"rechazada pero comprometida", []*pb.TransaccionEvaluada{evaluada(1, true)}, liquidada(commit), Inconsistente},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := Clasificar(c.evaluadas, c.liquidada); got != c.want {
				t.Errorf("Clasificar = %v, se esperaba %v", got, c.want)
			}
		})
	}
}

// La ventana: una transacción se clasifica recién cuando venció, aunque los
// eventos lleguen en cualquier orden.
func TestCruceVentana(t *testing.T) {
	c := NuevoCruce(30 * time.Second)
	t0 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

	// Llega primero la liquidación y 5s después la evaluación.
	c.Liquidada(liquidada(commit), t0)
	c.Evaluada(evaluada(1, false), t0.Add(5*time.Second))

	if cierres := c.Cerrar(t0.Add(29 * time.Second)); len(cierres) != 0 {
		t.Fatalf("antes de la ventana: %d cierres, se esperaba 0", len(cierres))
	}
	if c.Pendientes() != 1 {
		t.Fatalf("pendientes = %d, se esperaba 1", c.Pendientes())
	}
	cierres := c.Cerrar(t0.Add(30 * time.Second))
	if len(cierres) != 1 || cierres[0].Resultado != Consistente {
		t.Fatalf("al vencer la ventana: %+v", cierres)
	}
	if c.Pendientes() != 0 {
		t.Errorf("pendientes = %d tras cerrar, se esperaba 0", c.Pendientes())
	}
}

// Si la pareja no llega en la ventana, se clasifica con lo que hay.
func TestCruceSinPareja(t *testing.T) {
	c := NuevoCruce(time.Second)
	t0 := time.Now()
	c.Evaluada(evaluada(1, true), t0)
	cierres := c.Cerrar(t0.Add(time.Second))
	if len(cierres) != 1 || cierres[0].Resultado != RegistroPerdido {
		t.Fatalf("cierres = %+v, se esperaba REGISTRO_PERDIDO", cierres)
	}
}

func TestEsDiscrepancia(t *testing.T) {
	for _, r := range []Resultado{Consistente, NoComprometida} {
		if r.EsDiscrepancia() {
			t.Errorf("%v no debería ser discrepancia", r)
		}
	}
	for _, r := range []Resultado{AmbiguaComprometida, RegistroPerdido, EventoPerdido, Inconsistente} {
		if !r.EsDiscrepancia() {
			t.Errorf("%v debería ser discrepancia", r)
		}
	}
}

func TestArchivo(t *testing.T) {
	dir := t.TempDir() // se borra solo al terminar el test
	a, err := NuevoArchivo(dir)
	if err != nil {
		t.Fatalf("NuevoArchivo: %v", err)
	}
	dia1 := time.Date(2026, 10, 19, 23, 0, 0, 0, time.UTC)
	dia2 := dia1.Add(2 * time.Hour) // otro día: rota el archivo

	cierre := Cierre{TxID: "t1", Resultado: AmbiguaComprometida, Evaluada: sinDecision(1), Liquidada: liquidada(commit)}
	if err := a.Escribir(cierre, dia1); err != nil {
		t.Fatalf("Escribir: %v", err)
	}
	if err := a.Escribir(Cierre{TxID: "t2", Resultado: Consistente}, dia2); err != nil {
		t.Fatalf("Escribir: %v", err)
	}
	if err := a.Cerrar(); err != nil {
		t.Fatalf("Cerrar: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "2026-10-19.jsonl"))
	if err != nil {
		t.Fatalf("archivo del día 1: %v", err)
	}
	var l map[string]any
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatalf("no es JSON: %v (%s)", err, b)
	}
	if l["resultado"] != string(AmbiguaComprometida) || l["liquidada"].(map[string]any)["estado"] != "STATUS_COMMITTED" {
		t.Errorf("línea = %v", l)
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-10-20.jsonl")); err != nil {
		t.Errorf("no rotó al día 2: %v", err)
	}
}
