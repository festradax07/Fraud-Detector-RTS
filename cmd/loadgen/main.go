// loadgen: generador de carga del D-RTS. Dispara transacciones contra el
// Fraud-Engine a una tasa configurable y clasifica cada resultado.
//
//	go run ./cmd/loadgen -modo nominal               # 50 RPS, 30s
//	go run ./cmd/loadgen -modo sobrecarga -duracion 60s
//
// Diseño (ver GUIA-GO, Etapa 6):
//   - LAZO ABIERTO: las llegadas siguen un proceso de Poisson y cada una sale
//     en su propia goroutine, sin esperar a las anteriores. Un generador de
//     lazo cerrado (N workers que esperan cada respuesta) afloja justo cuando
//     el sistema sufre y esconde la latencia real ("coordinated omission").
//   - La latencia se mide desde el instante PROGRAMADO de la llegada: si el
//     propio loadgen se atrasa, el atraso cuenta y no se esconde.
//   - Datos: categoría, monto y coordenadas del comercio de fraudTest.csv
//     (no usado para calcular las estadísticas), con una tarjeta ÚNICA por
//     transacción: con 924 tarjetas reales, la compresión temporal haría que
//     casi todo diera CARD_TESTING.
//   - Ante SETTLEMENT_TIMEOUT (resultado ambiguo, Etapa 5) reintenta con el
//     MISMO tx_id: el sistema es idempotente y converge al resultado real.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"fd_rts/internal/fallas"
	"fd_rts/internal/metricas"
	pb "fd_rts/proto"
)

// Categorías de resultado.
const (
	catProcesada = "PROCESSED"     // hubo decisión a tiempo
	catEarlyDrop = "EARLY_DROP"    // la admisión la rechazó
	catDeadline  = "DEADLINE_MISS" // no llegó respuesta antes del deadline
	catAmbigua   = "AMBIGUOUS"     // timeout de Settlement: pudo haberse comprometido
	catError     = "ERROR"         // cualquier otro error
)

// modos predefinidos (RPS).
var modos = map[string]float64{
	"nominal":    50,
	"sobrecarga": 350,
}

// fila es lo que se usa de cada transacción del dataset.
type fila struct {
	categoria string
	monto     float64
	lat, lon  float64
}

// resultado de UNA transacción (con todos sus reintentos).
type resultado struct {
	i          int
	programada time.Time     // instante de llegada según el proceso de Poisson
	retraso    time.Duration // cuánto tarde salió respecto de lo programado
	latencia   time.Duration // desde lo programado hasta la respuesta del 1.er intento

	// Del primer intento.
	cat       string
	motivo    string // ErrorInfo.Reason, o el código gRPC si no trae motivo
	fraude    bool
	rechazo   string
	degradada bool

	// Reintentos (solo para ambiguas).
	intentos   int
	resolucion string // categoría del último intento
}

// clasificar decide la categoría de una respuesta a partir del error. Es una
// función pura: lo único que mira es el error (código + ErrorInfo).
func clasificar(err error) (cat, motivo string) {
	if err == nil {
		return catProcesada, ""
	}
	motivo = fallas.Motivo(err)
	switch {
	case motivo == fallas.EarlyDrop:
		return catEarlyDrop, motivo
	case motivo == fallas.SettlementTimeout:
		return catAmbigua, motivo
	case motivo == "" && status.Code(err) == codes.DeadlineExceeded:
		// Sin motivo: lo generó el timer del propio cliente. El servidor no
		// respondió a tiempo.
		return catDeadline, "TIMEOUT_DEL_CLIENTE"
	case motivo == "":
		return catError, status.Code(err).String()
	default:
		return catError, motivo
	}
}

// intervaloPoisson sortea el tiempo hasta la próxima llegada. En un proceso
// de Poisson de tasa λ (llegadas por segundo), los intervalos entre
// llegadas son exponenciales de media 1/λ. ExpFloat64 da una exponencial de
// media 1; dividir por λ la escala.
func intervaloPoisson(r *rand.Rand, rps float64) time.Duration {
	return time.Duration(r.ExpFloat64() / rps * float64(time.Second))
}

// percentil devuelve el percentil q (0..1) de una lista YA ORDENADA. Método
// "nearest-rank": el valor en la posición ceil(q·n)-1.
func percentil(ordenada []time.Duration, q float64) time.Duration {
	if len(ordenada) == 0 {
		return 0
	}
	pos := int(float64(len(ordenada))*q+0.999999999) - 1
	if pos < 0 {
		pos = 0
	}
	if pos >= len(ordenada) {
		pos = len(ordenada) - 1
	}
	return ordenada[pos]
}

type generador struct {
	cliente     pb.FraudEngineClient
	deadline    time.Duration
	maxIntentos int
	corrida     string // prefijo único de tx_id y tarjetas
	met         *metricasLoadgen
}

// metricasLoadgen: la vista del CLIENTE, en vivo (Etapa 7). Es la única que
// ve los DEADLINE_MISS: el servidor cree que respondió bien.
type metricasLoadgen struct {
	ofrecidas   prometheus.Counter
	resultados  *prometheus.CounterVec
	latencia    *prometheus.HistogramVec
	retraso     prometheus.Histogram
	enVuelo     prometheus.Gauge
	reintentos  *prometheus.CounterVec
	rpsObjetivo prometheus.Gauge
}

func nuevasMetricasLoadgen(reg prometheus.Registerer) *metricasLoadgen {
	m := &metricasLoadgen{
		ofrecidas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "drts_loadgen_ofrecidas_total",
			Help: "Transacciones lanzadas (carga ofrecida).",
		}),
		resultados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_loadgen_resultados_total",
			Help: "Resultado del primer intento, por categoría (PROCESSED, EARLY_DROP, DEADLINE_MISS, AMBIGUOUS, ERROR) y motivo.",
		}, []string{"categoria", "motivo"}),
		latencia: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "drts_loadgen_latencia_segundos",
			Help:    "Latencia vista por el cliente, desde el instante PROGRAMADO de llegada.",
			Buckets: metricas.BucketsLatencia,
		}, []string{"categoria"}),
		retraso: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "drts_loadgen_retraso_lanzamiento_segundos",
			Help:    "Cuánto tarde salió cada transacción respecto de lo programado. Si crece, el generador no sigue el ritmo.",
			Buckets: metricas.BucketsFase,
		}),
		enVuelo: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "drts_loadgen_en_vuelo",
			Help: "Transacciones esperando respuesta.",
		}),
		reintentos: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "drts_loadgen_reintentos_total",
			Help: "Reintentos de transacciones ambiguas, por resultado del reintento.",
		}, []string{"resultado"}),
		rpsObjetivo: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "drts_loadgen_rps_objetivo",
			Help: "Tasa de llegadas pedida en esta corrida.",
		}),
	}
	reg.MustRegister(m.ofrecidas, m.resultados, m.latencia, m.retraso, m.enVuelo, m.reintentos, m.rpsObjetivo)
	return m
}

// enviar manda la transacción i y la sigue hasta un resultado final.
func (g *generador) enviar(i int, f fila, programada time.Time) resultado {
	r := resultado{i: i, programada: programada, retraso: time.Since(programada)}
	g.met.retraso.Observe(r.retraso.Seconds())
	g.met.enVuelo.Inc()
	defer g.met.enVuelo.Dec()

	txID := fmt.Sprintf("%s-%d", g.corrida, i)
	req := &pb.TransactionRequest{
		TxId:             txID,
		UserId:           "loadgen",
		CardToken:        txID + "-card", // única por transacción (ver arriba)
		Amount:           f.monto,
		MerchantCategory: f.categoria,
		Latitude:         f.lat,
		Longitude:        f.lon,
	}

	for intento := 1; intento <= g.maxIntentos; intento++ {
		// Cada intento es una emisión nueva con su propio deadline: un
		// reintento con la emisión original llegaría "tarde" por definición.
		req.EmissionTimestampNs = time.Now().UnixNano()
		ctx, cancel := context.WithTimeout(context.Background(), g.deadline)
		resp, err := g.cliente.EvaluateTransaction(ctx, req)
		cancel()

		cat, motivo := clasificar(err)
		r.intentos = intento
		r.resolucion = cat
		if intento == 1 {
			r.latencia = time.Since(programada)
			r.cat, r.motivo = cat, motivo
			g.met.resultados.WithLabelValues(cat, motivo).Inc()
			g.met.latencia.WithLabelValues(cat).Observe(r.latencia.Seconds())
			if resp != nil {
				r.fraude, r.rechazo, r.degradada = resp.IsFraud, resp.RejectionReason, resp.IsDegraded
			}
		}
		if intento > 1 {
			g.met.reintentos.WithLabelValues(cat).Inc()
		}
		if cat != catAmbigua {
			break // resultado conocido: no hace falta reintentar
		}
		// Ambiguo: reintentar con el MISMO tx_id (idempotente).
	}
	return r
}

func main() {
	modo := flag.String("modo", "nominal", "nominal (50 RPS) o sobrecarga (350 RPS)")
	rpsFlag := flag.Float64("rps", 0, "tasa de llegadas; si es 0 se usa la del modo")
	duracion := flag.Duration("duracion", 30*time.Second, "cuánto tiempo generar llegadas")
	deadline := flag.Duration("deadline", 200*time.Millisecond, "deadline de cada transacción")
	rutaCSV := flag.String("csv", "data/fraudTest.csv", "dataset de donde salen las transacciones")
	target := flag.String("target", "localhost:50051", "dirección del Fraud-Engine")
	maxIntentos := flag.Int("intentos", 3, "máximo de intentos ante resultado ambiguo")
	semilla := flag.Uint64("semilla", 1, "semilla del generador de llegadas (reproducible)")
	salida := flag.String("salida", "", "CSV de resultados (default: resultados/loadgen-<modo>-<hora>.csv)")
	metricasAddr := flag.String("metricas", ":2114", "dónde exponer /metrics para Prometheus (vacío: no exponer)")
	gracia := flag.Duration("gracia", 3*time.Second, "cuánto mantener /metrics abierto al terminar, para el último scrape")
	flag.Parse()

	rps := *rpsFlag
	if rps == 0 {
		var ok bool
		if rps, ok = modos[*modo]; !ok {
			log.Fatalf("modo desconocido %q (nominal o sobrecarga)", *modo)
		}
	}

	filas, err := leerFilas(*rutaCSV)
	if err != nil {
		log.Fatalf("leyendo %s: %v", *rutaCSV, err)
	}

	// Una sola conexión: HTTP/2 multiplexa todas las requests concurrentes
	// sobre ella.
	conn, err := grpc.NewClient(*target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("cliente: %v", err)
	}
	defer conn.Close()

	reg := metricas.NuevoRegistro()
	met := nuevasMetricasLoadgen(reg)
	met.rpsObjetivo.Set(rps)
	if *metricasAddr != "" {
		metricas.Servir(*metricasAddr, reg)
		log.Printf("métricas en http://localhost%s/metrics", *metricasAddr)
	}

	inicio := time.Now()
	g := &generador{
		cliente:     pb.NewFraudEngineClient(conn),
		deadline:    *deadline,
		maxIntentos: *maxIntentos,
		corrida:     fmt.Sprintf("lg%d", inicio.Unix()),
		met:         met,
	}
	calentar(g.cliente)

	log.Printf("corrida %s: %.0f RPS durante %v (deadline %v), %d filas del dataset",
		g.corrida, rps, *duracion, *deadline, len(filas))

	// Los resultados llegan por un channel a UNA goroutine que los junta:
	// así nadie más toca el slice y no hace falta mutex ("compartir memoria
	// comunicando").
	resultados := make(chan resultado, 1024)
	var todos []resultado
	listo := make(chan struct{})
	go func() {
		for r := range resultados {
			todos = append(todos, r)
		}
		close(listo)
	}()

	// WaitGroup: cuenta las transacciones en vuelo para esperarlas al final.
	var wg sync.WaitGroup

	// rand.Rand NO es seguro para usar desde varias goroutines: solo lo usa
	// este loop. Con semilla fija, la secuencia de llegadas es reproducible.
	r := rand.New(rand.NewPCG(*semilla, *semilla))
	inicio = time.Now()
	proxima := inicio
	for i := 0; ; i++ {
		// Reloj ABSOLUTO: próxima = anterior + intervalo. Si en cambio se
		// durmiera "intervalo" después de lanzar, el tiempo de lanzar se iría
		// sumando y la tasa real quedaría por debajo de la pedida.
		proxima = proxima.Add(intervaloPoisson(r, rps))
		if proxima.Sub(inicio) >= *duracion {
			break
		}
		time.Sleep(time.Until(proxima)) // si ya pasó, no duerme

		met.ofrecidas.Inc()
		wg.Add(1)
		go func(i int, f fila, programada time.Time) {
			defer wg.Done()
			resultados <- g.enviar(i, f, programada)
		}(i, filas[i%len(filas)], proxima)
	}
	generacion := time.Since(inicio)

	wg.Wait()         // esperar las que siguen en vuelo
	close(resultados) // ya nadie más va a mandar: el colector puede terminar
	<-listo

	sort.Slice(todos, func(a, b int) bool { return todos[a].i < todos[b].i })
	reportar(todos, *modo, rps, generacion, *deadline)

	if *salida == "" {
		*salida = filepath.Join("resultados", fmt.Sprintf("loadgen-%s-%s.csv", *modo, inicio.Format("20060102-150405")))
	}
	if err := escribirCSV(*salida, todos); err != nil {
		log.Fatalf("escribiendo %s: %v", *salida, err)
	}
	log.Printf("resultados por transacción en %s", *salida)

	if *metricasAddr != "" && *gracia > 0 {
		// Prometheus scrapea cada 1s: sin esta espera, lo último que pasó en
		// la corrida podría no llegar a leerse antes de que el proceso muera.
		log.Printf("manteniendo /metrics %v para el último scrape", *gracia)
		time.Sleep(*gracia)
	}
}

// calentar manda una transacción inválida para abrir la conexión gRPC antes
// de medir: la primera llamada paga el costo de conectar (cold start).
func calentar(c pb.FraudEngineClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.EvaluateTransaction(ctx, &pb.TransactionRequest{}) // InvalidArgument: solo abre la conexión
}

func reportar(todos []resultado, modo string, rps float64, generacion, deadline time.Duration) {
	n := len(todos)
	fmt.Printf("\n=== %s: %.0f RPS pedidos, %d transacciones en %v → %.1f RPS logrados ===\n",
		modo, rps, n, generacion.Round(time.Millisecond), float64(n)/generacion.Seconds())

	// ¿Pudo el generador seguir el ritmo? Si no, la corrida no es válida.
	var retrasos []time.Duration
	for _, r := range todos {
		retrasos = append(retrasos, r.retraso)
	}
	sort.Slice(retrasos, func(a, b int) bool { return retrasos[a] < retrasos[b] })
	fmt.Printf("retraso de lanzamiento del generador: P50=%v P99=%v máx=%v\n",
		percentil(retrasos, .5).Round(time.Microsecond), percentil(retrasos, .99).Round(time.Microsecond), retrasos[len(retrasos)-1].Round(time.Microsecond))

	porCat := map[string][]time.Duration{}
	var todas []time.Duration
	aTiempo := 0
	for _, r := range todos {
		porCat[r.cat] = append(porCat[r.cat], r.latencia)
		todas = append(todas, r.latencia)
		if r.latencia <= deadline {
			aTiempo++
		}
	}

	fmt.Printf("\n%-14s %7s %7s %10s %10s %10s\n", "categoría", "n", "%", "P50", "P99", "P100")
	linea := func(nombre string, lat []time.Duration) {
		sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
		fmt.Printf("%-14s %7d %6.2f%% %10v %10v %10v\n", nombre, len(lat), 100*float64(len(lat))/float64(n),
			percentil(lat, .5).Round(time.Microsecond), percentil(lat, .99).Round(time.Microsecond), percentil(lat, 1).Round(time.Microsecond))
	}
	for _, c := range []string{catProcesada, catEarlyDrop, catDeadline, catAmbigua, catError} {
		if len(porCat[c]) > 0 {
			linea(c, porCat[c])
		}
	}
	linea("TODAS", todas)
	fmt.Printf("respondidas dentro del deadline (%v): %.2f%%\n", deadline, 100*float64(aTiempo)/float64(n))

	// Detalle de las procesadas.
	var aprobadas, degradadas int
	rechazos := map[string]int{}
	for _, r := range todos {
		if r.cat != catProcesada {
			continue
		}
		if r.fraude {
			rechazos[r.rechazo]++
		} else {
			aprobadas++
		}
		if r.degradada {
			degradadas++
		}
	}
	totalRechazos := 0
	for _, v := range rechazos {
		totalRechazos += v
	}
	fmt.Printf("\nprocesadas: %d aprobadas, %d rechazadas %v, %d degradadas (sin fase opcional)\n",
		aprobadas, totalRechazos, rechazos, degradadas)

	// Errores por motivo.
	motivos := map[string]int{}
	for _, r := range todos {
		if r.cat == catError || r.cat == catDeadline {
			motivos[r.motivo]++
		}
	}
	if len(motivos) > 0 {
		fmt.Printf("errores y misses por motivo: %v\n", motivos)
	}

	// Ambiguas: cómo se resolvieron al reintentar.
	resoluciones := map[string]int{}
	for _, r := range todos {
		if r.cat == catAmbigua {
			resoluciones[fmt.Sprintf("%s tras %d intentos", r.resolucion, r.intentos)]++
		}
	}
	if len(resoluciones) > 0 {
		fmt.Printf("ambiguas (reintentadas con el mismo tx_id): %v\n", resoluciones)
	}
}

func escribirCSV(ruta string, todos []resultado) error {
	if err := os.MkdirAll(filepath.Dir(ruta), 0o755); err != nil {
		return err
	}
	f, err := os.Create(ruta)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"i", "programada_ns", "retraso_us", "latencia_us", "categoria", "motivo",
		"is_fraud", "rejection_reason", "is_degraded", "intentos", "resolucion"})
	for _, r := range todos {
		w.Write([]string{
			strconv.Itoa(r.i),
			strconv.FormatInt(r.programada.UnixNano(), 10),
			strconv.FormatInt(r.retraso.Microseconds(), 10),
			strconv.FormatInt(r.latencia.Microseconds(), 10),
			r.cat, r.motivo,
			strconv.FormatBool(r.fraude), r.rechazo, strconv.FormatBool(r.degradada),
			strconv.Itoa(r.intentos), r.resolucion,
		})
	}
	w.Flush() // csv.Writer tiene buffer: sin Flush, lo último no se escribe
	return w.Error()
}

// leerFilas carga del dataset solo lo necesario (categoría, monto,
// coordenadas del comercio). Se carga entero en memoria (~555k filas, unos
// pocos MB) para no leer disco mientras se genera carga.
func leerFilas(ruta string) ([]fila, error) {
	f, err := os.Open(ruta)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true

	encabezado, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, nombre := range encabezado {
		col[nombre] = i
	}
	for _, req := range []string{"category", "amt", "merch_lat", "merch_long"} {
		if _, ok := col[req]; !ok {
			return nil, fmt.Errorf("falta la columna %q", req)
		}
	}

	var filas []fila
	for {
		reg, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		monto, err1 := strconv.ParseFloat(reg[col["amt"]], 64)
		lat, err2 := strconv.ParseFloat(reg[col["merch_lat"]], 64)
		lon, err3 := strconv.ParseFloat(reg[col["merch_long"]], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue // fila rota: se saltea
		}
		// strings.Clone: encoding/csv arma los campos de una fila como
		// pedazos de UN string con la línea entera. Guardar el pedazo tal
		// cual mantendría viva toda la línea (~250 bytes) por cada fila:
		// ~140MB para guardar palabras de 10 letras. Clone copia solo eso.
		filas = append(filas, fila{categoria: strings.Clone(reg[col["category"]]), monto: monto, lat: lat, lon: lon})
	}
	if len(filas) == 0 {
		return nil, fmt.Errorf("no hay filas válidas")
	}
	return filas, nil
}
