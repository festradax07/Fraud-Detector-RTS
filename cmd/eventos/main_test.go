package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"fd_rts/internal/auditoria"
	"fd_rts/internal/eventos"
	"fd_rts/internal/opcional"
	"fd_rts/internal/pruebas"
	pb "fd_rts/proto"
)

// valor busca el valor de una métrica (con una etiqueta igual a etiqueta) en
// el registro.
func valor(t *testing.T, reg *prometheus.Registry, nombre, etiqueta string) float64 {
	t.Helper()
	familias, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range familias {
		if f.GetName() != nombre {
			continue
		}
		for _, m := range f.Metric {
			for _, l := range m.Label {
				if l.GetValue() == etiqueta {
					if m.Counter != nil {
						return m.Counter.GetValue()
					}
					return m.Gauge.GetValue()
				}
			}
		}
	}
	return 0
}

// terminar cancela un consumidor y espera que devuelva el control. Con
// plazo: un consumidor que no termina al cancelar es un bug (un test que
// queda colgado no dice qué pasó; uno que falla, sí).
func terminar(t *testing.T, cancel context.CancelFunc, fin <-chan error) {
	t.Helper()
	cancel()
	select {
	case <-fin:
	case <-time.After(20 * time.Second):
		t.Fatalf("el consumidor no terminó a los 20s de cancelarlo")
	}
}

// esperar hasta que cond se cumpla (o falla a los 20s).
func esperar(t *testing.T, cond func() bool, que string) {
	t.Helper()
	limite := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(limite) {
			t.Fatalf("timeout esperando: %s", que)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// La auditoría de punta a punta, con Kafka real: tres transacciones con
// distinto destino, publicadas como las publican el motor y Settlement.
func TestAuditoriaDePuntaAPunta(t *testing.T) {
	brokers, nombres := pruebas.Kafka(t, "evaluadas", "liquidadas")
	topics := eventos.Topics{Evaluadas: nombres[0], Liquidadas: nombres[1]}
	prod, err := eventos.NuevoProductor(brokers, topics, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("productor: %v", err)
	}

	ahora := time.Now().UnixNano()
	aprobada := &pb.EvaluationResponse{TxId: "t1"}
	prod.PublicarEvaluada(&pb.TransaccionEvaluada{TxId: "t1", CardToken: "c1", Evaluacion: aprobada, EvaluadoNs: ahora})
	prod.PublicarLiquidada(&pb.TransaccionLiquidada{TxId: "t1", Estado: pb.TxStatus_STATUS_COMMITTED})
	// El cliente recibió SETTLEMENT_TIMEOUT, pero Settlement comprometió.
	prod.PublicarEvaluada(&pb.TransaccionEvaluada{TxId: "t2", CardToken: "c2", ErrorMotivo: "SETTLEMENT_TIMEOUT", EvaluadoNs: ahora})
	prod.PublicarLiquidada(&pb.TransaccionLiquidada{TxId: "t2", Estado: pb.TxStatus_STATUS_COMMITTED})
	// Rechazo respondido, sin registro.
	prod.PublicarEvaluada(&pb.TransaccionEvaluada{TxId: "t3", CardToken: "c3", Evaluacion: &pb.EvaluationResponse{TxId: "t3", IsFraud: true, RejectionReason: "BLACKLIST"}, EvaluadoNs: ahora})
	ctxF, cancelF := context.WithTimeout(context.Background(), 10*time.Second)
	prod.Cerrar(ctxF)
	cancelF()

	dir := t.TempDir()
	reg := prometheus.NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	fin := make(chan error, 1)
	grupo := fmt.Sprintf("test-auditoria-%d", time.Now().UnixNano())
	defer cancel()
	go func() { fin <- auditar(ctx, brokers, topics, grupo, dir, time.Second, reg) }()

	esperar(t, func() bool {
		total := 0.0
		for _, r := range []auditoria.Resultado{auditoria.Consistente, auditoria.AmbiguaComprometida, auditoria.RegistroPerdido} {
			total += valor(t, reg, "drts_auditoria_resultados_total", string(r))
		}
		return total == 3
	}, "las 3 transacciones clasificadas")
	terminar(t, cancel, fin)

	for r, want := range map[auditoria.Resultado]float64{
		auditoria.Consistente: 1, auditoria.AmbiguaComprometida: 1, auditoria.RegistroPerdido: 1,
	} {
		if got := valor(t, reg, "drts_auditoria_resultados_total", string(r)); got != want {
			t.Errorf("%s = %v, se esperaba %v", r, got, want)
		}
	}

	// El archivo del día tiene las tres líneas.
	archivos, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(archivos) != 1 {
		t.Fatalf("archivos de auditoría: %v", archivos)
	}
	f, err := os.Open(archivos[0])
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer f.Close()
	porTx := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct {
			TxID      string `json:"tx_id"`
			Resultado string `json:"resultado"`
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("línea no es JSON: %v", err)
		}
		porTx[l.TxID] = l.Resultado
	}
	if porTx["t2"] != string(auditoria.AmbiguaComprometida) || porTx["t3"] != string(auditoria.RegistroPerdido) || porTx["t1"] != string(auditoria.Consistente) {
		t.Errorf("archivo: %v", porTx)
	}
}

// Las estadísticas en vivo van a claves APARTE (stats_vivo:*) y la línea
// base que usa el motor no se toca: modo sombra.
func TestEstadisticasEnSombra(t *testing.T) {
	brokers, nombres := pruebas.Kafka(t, "evaluadas", "liquidadas")
	topics := eventos.Topics{Evaluadas: nombres[0], Liquidadas: nombres[1]}
	rdb, prefijo := pruebas.Redis(t)
	ctx0 := context.Background()

	// Línea base: mediana $100 (media_log = ln 100), desvío 1.
	base := map[string]any{opcional.CampoMediaLog: math.Log(100), opcional.CampoDesvioLog: 1.0, opcional.CampoPeso: 1.0}
	if err := rdb.HSet(ctx0, prefijo+"stats:cat", base).Err(); err != nil {
		t.Fatalf("línea base: %v", err)
	}

	prod, err := eventos.NuevoProductor(brokers, topics, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("productor: %v", err)
	}
	// 10 aprobadas de $100·e (media_log = ln 100 + 1: deriva de +1 desvío)
	// y un rechazo, que NO cuenta.
	for i := 0; i < 10; i++ {
		prod.PublicarEvaluada(&pb.TransaccionEvaluada{TxId: fmt.Sprint(i), CardToken: "c", MerchantCategory: "cat", Amount: 100 * math.E, Evaluacion: &pb.EvaluationResponse{}})
	}
	prod.PublicarEvaluada(&pb.TransaccionEvaluada{TxId: "x", CardToken: "c", MerchantCategory: "cat", Amount: 1e6, Evaluacion: &pb.EvaluationResponse{IsFraud: true}})
	ctxF, cancelF := context.WithTimeout(ctx0, 10*time.Second)
	prod.Cerrar(ctxF)
	cancelF()

	reg := prometheus.NewRegistry()
	ctx, cancel := context.WithCancel(ctx0)
	fin := make(chan error, 1)
	grupo := fmt.Sprintf("test-estadisticas-%d", time.Now().UnixNano())
	defer cancel()
	go func() {
		fin <- estadisticasEnSombra(ctx, brokers, topics, grupo, rdb, prefijo, 200*time.Millisecond, reg)
	}()
	esperar(t, func() bool { return valor(t, reg, "drts_stats_vivo_muestras", "cat") == 10 }, "10 muestras (el rechazo no cuenta)")
	terminar(t, cancel, fin)

	if d := valor(t, reg, "drts_stats_vivo_deriva_sigmas", "cat"); math.Abs(d-1) > 1e-9 {
		t.Errorf("deriva = %v sigmas, se esperaba 1", d)
	}
	vivo, _ := rdb.HGetAll(ctx0, prefijo+"stats_vivo:cat").Result()
	if vivo["n"] != "10" {
		t.Errorf("stats_vivo = %v", vivo)
	}
	// La línea base que usa el motor quedó igual.
	h, _ := rdb.HGetAll(ctx0, prefijo+"stats:cat").Result()
	if e, ok, err := opcional.ParsearEstadisticas(h); !ok || err != nil || math.Abs(e.MediaLog-math.Log(100)) > 1e-12 {
		t.Errorf("la línea base cambió: %v", h)
	}
}
