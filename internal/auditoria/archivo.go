package auditoria

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
)

// Archivo escribe cada transacción clasificada como una línea JSON
// (append-only), en un archivo por día: AAAA-MM-DD.jsonl. Es el registro
// durable de auditoría: se inspecciona con grep o jq, y con él los registros
// de Settlement en Redis pueden expirar.
//
// Seguro para usar desde varias goroutines.
type Archivo struct {
	dir string
	mu  sync.Mutex
	dia string
	f   *os.File
	w   *bufio.Writer
}

// NuevoArchivo escribe en dir (lo crea si no existe).
func NuevoArchivo(dir string) (*Archivo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Archivo{dir: dir}, nil
}

// linea es el formato de cada línea. Los eventos van en su forma JSON
// canónica de protobuf (protojson): el mismo esquema, otra codificación.
type linea struct {
	TxID      string          `json:"tx_id"`
	Resultado Resultado       `json:"resultado"`
	Cerrado   string          `json:"cerrado"`
	Evaluada  json.RawMessage `json:"evaluada,omitempty"`
	Liquidada json.RawMessage `json:"liquidada,omitempty"`
}

// Escribir agrega c al archivo del día de ahora.
func (a *Archivo) Escribir(c Cierre, ahora time.Time) error {
	l := linea{TxID: c.TxID, Resultado: c.Resultado, Cerrado: ahora.UTC().Format(time.RFC3339Nano)}
	var err error
	if c.Evaluada != nil {
		if l.Evaluada, err = protojson.Marshal(c.Evaluada); err != nil {
			return err
		}
	}
	if c.Liquidada != nil {
		if l.Liquidada, err = protojson.Marshal(c.Liquidada); err != nil {
			return err
		}
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.abrir(ahora.UTC().Format("2006-01-02")); err != nil {
		return err
	}
	if _, err := a.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// abrir rota al archivo de dia si cambió el día. Con a.mu tomado.
func (a *Archivo) abrir(dia string) error {
	if a.f != nil && a.dia == dia {
		return nil
	}
	if err := a.cerrarLocked(); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(a.dir, dia+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("abriendo el archivo de auditoría: %w", err)
	}
	a.dia, a.f, a.w = dia, f, bufio.NewWriter(f)
	return nil
}

// Sincronizar baja el buffer al archivo y al disco (fsync). Se llama
// periódicamente, no por línea: un fsync por transacción sería muy caro.
func (a *Archivo) Sincronizar() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.f == nil {
		return nil
	}
	if err := a.w.Flush(); err != nil {
		return err
	}
	return a.f.Sync()
}

// Cerrar sincroniza y cierra.
func (a *Archivo) Cerrar() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cerrarLocked()
}

func (a *Archivo) cerrarLocked() error {
	if a.f == nil {
		return nil
	}
	if err := a.w.Flush(); err != nil {
		return err
	}
	if err := a.f.Sync(); err != nil {
		return err
	}
	err := a.f.Close()
	a.f, a.w = nil, nil
	return err
}
