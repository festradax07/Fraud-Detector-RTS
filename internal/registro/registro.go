// Package registro configura los logs de los servicios con log/slog
// (ADR-0021).
//
// Por qué niveles: con un log por evento de cada transacción, a 1000 RPS los
// servicios escriben miles de líneas por segundo (~2GB/h en los logs de un
// contenedor, que por defecto no rotan). El costo de latencia es chico
// (~0.8µs por línea), el problema es el volumen. El detalle de cada
// transacción va en Debug (apagado por defecto); lo que alguien tiene que
// mirar (un registro perdido, un commit que no se confirmó) va en Warn.
//
// Las métricas (internal/metricas) cuentan cada resultado: el log no es la
// forma de contar, es la forma de investigar un caso.
package registro

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"fd_rts/internal/config"
)

// Configurar arma el logger por defecto (slog.Default) desde el entorno:
//
//	LOG_NIVEL   debug | info | warn | error   (default info)
//	LOG_FORMATO texto | json                  (default texto; json para la nube)
//
// servicio se agrega a cada línea (útil cuando los logs de varios servicios
// terminan juntos).
func Configurar(servicio string) error {
	return configurarEn(os.Stderr, servicio, config.Texto("LOG_NIVEL", "info"), config.Texto("LOG_FORMATO", "texto"))
}

func configurarEn(w io.Writer, servicio, nivelTexto, formato string) error {
	var nivel slog.Level
	if err := nivel.UnmarshalText([]byte(nivelTexto)); err != nil {
		return fmt.Errorf("LOG_NIVEL=%q: debe ser debug, info, warn o error", nivelTexto)
	}
	opciones := &slog.HandlerOptions{Level: nivel}

	var h slog.Handler
	switch strings.ToLower(formato) {
	case "texto":
		h = slog.NewTextHandler(w, opciones)
	case "json":
		h = slog.NewJSONHandler(w, opciones)
	default:
		return fmt.Errorf("LOG_FORMATO=%q: debe ser texto o json", formato)
	}
	slog.SetDefault(slog.New(h).With("servicio", servicio))
	return nil
}

// Depurando dice si el nivel Debug está habilitado.
//
// En el hot path, los logs de Debug van detrás de esta guarda:
//
//	if registro.Depurando(ctx) { slog.DebugContext(ctx, "...", "tx_id", id) }
//
// Medido: slog.Debug con el nivel apagado igual arma sus argumentos (...any)
// y cuesta 26ns y 2 reservas de memoria por llamada; con la guarda, 3ns y 0.
func Depurando(ctx context.Context) bool {
	return slog.Default().Enabled(ctx, slog.LevelDebug)
}

// Fatal registra un error y termina el proceso con código 1. Solo para el
// arranque (configuración inválida, una dependencia que no responde): en el
// hot path nunca se termina el proceso.
func Fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
