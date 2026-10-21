// cargar-stats calcula las estadísticas por categoría de comercio que usa la
// fase opcional (Z-score) y las guarda en Redis. Se corre una vez, aparte
// del server: no es parte del hot path.
//
//	go run ./cmd/cargar-stats -csv data/fraudTrain.csv
//
// Por categoría calcula, sobre ln(monto):
//   - media y desvío (población), con el algoritmo de Welford;
//   - peso = tasa de fraude de la categoría / tasa de fraude global.
//
// Se usa SOLO el archivo de entrenamiento: el de test queda sin tocar para
// poder evaluar el modelo con datos que no usó para calcularse.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"time"

	"fd_rts/internal/estadisticas"
	"fd_rts/internal/mandatoria"
	"fd_rts/internal/opcional"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/registro"
)

// categoria acumula ln(monto) (Welford) y cuántos fraudes hubo.
type categoria struct {
	estadisticas.Acumulador
	fraudes int
}

func main() {
	rutaCSV := flag.String("csv", "data/fraudTrain.csv", "CSV de entrenamiento (dataset Sparkov)")
	redisAddr := flag.String("redis", "localhost:6379", "dirección de Redis")
	prefijo := flag.String("prefijo", redisclient.PrefijoPorDefecto, "prefijo de claves (el mismo que usan los servicios)")
	flag.Parse()

	if err := registro.Configurar("cargar_stats"); err != nil {
		registro.Fatal("configuración de logs inválida", "error", err)
	}

	porCategoria, err := leerCSV(*rutaCSV)
	if err != nil {
		registro.Fatal("no se pudo leer el dataset", "csv", *rutaCSV, "error", err)
	}

	// Totales para la tasa de fraude global.
	var total, fraudes int
	for _, a := range porCategoria {
		total += a.N
		fraudes += a.fraudes
	}
	if fraudes == 0 {
		registro.Fatal("el CSV no tiene fraudes: no se puede calcular el peso", "csv", *rutaCSV)
	}
	tasaGlobal := float64(fraudes) / float64(total)

	// Orden fijo (por nombre) para que la salida sea reproducible: iterar un
	// map en Go da un orden distinto cada vez, a propósito.
	categorias := make([]string, 0, len(porCategoria))
	for c := range porCategoria {
		categorias = append(categorias, c)
	}
	sort.Strings(categorias)

	rdb := redisclient.Nuevo(*redisAddr)
	defer rdb.Close()
	v := mandatoria.NuevoVerificador(rdb, *prefijo)

	// No es hot path: context.Background con un timeout propio está bien.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Printf("%d transacciones, %d fraudes (tasa global %.4f%%)\n", total, fraudes, 100*tasaGlobal)
	fmt.Printf("%-16s %8s %9s %10s %7s %8s\n", "categoria", "n", "media_log", "desvio_log", "peso", "%fraude")
	for _, c := range categorias {
		a := porCategoria[c]
		tasa := float64(a.fraudes) / float64(a.N)
		peso := tasa / tasaGlobal

		err := v.GuardarEstadisticas(ctx, c, map[string]any{
			opcional.CampoMediaLog:  a.Media,
			opcional.CampoDesvioLog: a.Desvio(),
			opcional.CampoPeso:      peso,
		})
		if err != nil {
			registro.Fatal("no se pudo guardar en Redis", "categoria", c, "error", err)
		}
		fmt.Printf("%-16s %8d %9.4f %10.4f %7.3f %7.3f%%\n", c, a.N, a.Media, a.Desvio(), peso, 100*tasa)
	}
	fmt.Printf("cargadas %d categorías en Redis (%s, prefijo %q)\n", len(categorias), *redisAddr, *prefijo)
}

// leerCSV recorre el CSV fila por fila (sin cargarlo entero en memoria) y
// acumula las estadísticas por categoría.
func leerCSV(ruta string) (map[string]*categoria, error) {
	f, err := os.Open(ruta)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.ReuseRecord = true // reusa el slice de cada fila: menos basura para el GC

	// Las columnas se buscan por nombre en el encabezado, no por posición:
	// si el CSV cambia de orden, esto sigue andando (o falla con un mensaje
	// claro).
	encabezado, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("leyendo encabezado: %w", err)
	}
	col := map[string]int{}
	for i, nombre := range encabezado {
		col[nombre] = i
	}
	for _, requerida := range []string{"category", "amt", "is_fraud"} {
		if _, ok := col[requerida]; !ok {
			return nil, fmt.Errorf("falta la columna %q", requerida)
		}
	}

	porCategoria := map[string]*categoria{}
	for fila := 2; ; fila++ { // fila 1 es el encabezado
		reg, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("fila %d: %w", fila, err)
		}

		monto, err := strconv.ParseFloat(reg[col["amt"]], 64)
		if err != nil || !(monto > 0) {
			return nil, fmt.Errorf("fila %d: monto inválido %q", fila, reg[col["amt"]])
		}

		c := reg[col["category"]]
		a, ok := porCategoria[c]
		if !ok {
			a = &categoria{}
			porCategoria[c] = a
		}
		a.Agregar(math.Log(monto))
		if reg[col["is_fraud"]] == "1" {
			a.fraudes++
		}
	}
	return porCategoria, nil
}
