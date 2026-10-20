package config

import (
	"testing"
	"time"
)

func TestTexto(t *testing.T) {
	// t.Setenv define la variable solo durante el test y la restaura al
	// terminar (y prohíbe t.Parallel: el entorno es global del proceso).
	t.Setenv("DRTS_PRUEBA", "redis:6379")
	if got := Texto("DRTS_PRUEBA", "localhost:6379"); got != "redis:6379" {
		t.Errorf("definida: %q", got)
	}
	if got := Texto("DRTS_NO_EXISTE", "localhost:6379"); got != "localhost:6379" {
		t.Errorf("no definida: %q, se esperaba el defecto", got)
	}
	// Definida vacía se respeta (sirve para apagar algo a propósito).
	t.Setenv("DRTS_VACIA", "")
	if got := Texto("DRTS_VACIA", "x"); got != "" {
		t.Errorf("definida vacía: %q, se esperaba \"\"", got)
	}
}

func TestEntero(t *testing.T) {
	if n, err := Entero("DRTS_NO_EXISTE", 7); n != 7 || err != nil {
		t.Errorf("no definida: %d, %v", n, err)
	}
	t.Setenv("DRTS_N", "10000000")
	if n, err := Entero("DRTS_N", 7); n != 10000000 || err != nil {
		t.Errorf("definida: %d, %v", n, err)
	}
	t.Setenv("DRTS_N", "10M")
	if _, err := Entero("DRTS_N", 7); err == nil {
		t.Errorf("\"10M\": se esperaba error")
	}
}

func TestDuracion(t *testing.T) {
	if d, err := Duracion("DRTS_NO_EXISTE", time.Second); d != time.Second || err != nil {
		t.Errorf("no definida: %v, %v", d, err)
	}
	t.Setenv("DRTS_D", "250ms")
	if d, err := Duracion("DRTS_D", time.Second); d != 250*time.Millisecond || err != nil {
		t.Errorf("definida: %v, %v", d, err)
	}
	t.Setenv("DRTS_D", "5") // sin unidad: ambiguo, se rechaza
	if _, err := Duracion("DRTS_D", time.Second); err == nil {
		t.Errorf("\"5\" sin unidad: se esperaba error")
	}
}
