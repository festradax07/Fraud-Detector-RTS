package registro

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNiveles(t *testing.T) {
	var buf bytes.Buffer
	if err := configurarEn(&buf, "prueba", "info", "texto"); err != nil {
		t.Fatalf("configurar: %v", err)
	}
	slog.Debug("detalle", "tx_id", "t1")
	slog.Warn("importante", "tx_id", "t2")

	salida := buf.String()
	if strings.Contains(salida, "detalle") {
		t.Errorf("con nivel info salió una línea de debug: %q", salida)
	}
	if !strings.Contains(salida, "importante") || !strings.Contains(salida, "servicio=prueba") {
		t.Errorf("falta la línea de warn o el servicio: %q", salida)
	}
	if Depurando(context.Background()) {
		t.Errorf("Depurando = true con nivel info")
	}

	buf.Reset()
	configurarEn(&buf, "prueba", "debug", "texto")
	if !Depurando(context.Background()) {
		t.Errorf("Depurando = false con nivel debug")
	}
}

func TestFormatoJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := configurarEn(&buf, "prueba", "info", "json"); err != nil {
		t.Fatalf("configurar: %v", err)
	}
	slog.Info("hola", "tx_id", "t1")
	var linea map[string]any
	if err := json.Unmarshal(buf.Bytes(), &linea); err != nil {
		t.Fatalf("no es JSON: %v (%q)", err, buf.String())
	}
	if linea["tx_id"] != "t1" || linea["servicio"] != "prueba" {
		t.Errorf("línea = %v", linea)
	}
}

func TestConfiguracionInvalida(t *testing.T) {
	var buf bytes.Buffer
	if err := configurarEn(&buf, "x", "verbose", "texto"); err == nil {
		t.Errorf("nivel inválido: se esperaba error")
	}
	if err := configurarEn(&buf, "x", "info", "xml"); err == nil {
		t.Errorf("formato inválido: se esperaba error")
	}
}
