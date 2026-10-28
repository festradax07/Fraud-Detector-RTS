// gateway: la puerta de la demo interactiva. Recibe compras desde el celular
// (HTTP/JSON: el navegador no habla gRPC) y las manda al Fraud-Engine por
// gRPC, poniendo el deadline de 200ms en el borde, como la pasarela de un
// procesador de pagos (FLUJO-DEL-SISTEMA.md, paso 0). El tiempo de internet
// del celular queda afuera de ese presupuesto, y la página lo muestra aparte.
//
//	GET  /?t=TOKEN             la página del celular
//	GET  /presentador?t=TOKEN  el QR para el proyector y el contador de la clase
//	GET  /qr.png?t=TOKEN       el QR (apunta a la página del celular)
//	POST /api/compra?t=TOKEN   {"sesion": "...", "escenario": "normal"}
//	GET  /api/estado?t=TOKEN   cuántas compras se aprobaron y rechazaron
//	GET  /api/tarjeta?t=TOKEN&sesion=...&tarjeta=N   el número de fantasía
//
// Métricas para el dashboard "D-RTS — Demo en vivo" en METRICAS_ADDR (:2116),
// que no se publica hacia afuera.
//
// Es para la demo, no parte del sistema medido: el que genera la carga que se
// mide es el loadgen.
package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"fd_rts/internal/config"
	"fd_rts/internal/fallas"
	"fd_rts/internal/metricas"
	"fd_rts/internal/presupuesto"
	"fd_rts/internal/registro"
	pb "fd_rts/proto"
)

//go:embed web
var web embed.FS

type gateway struct {
	motor    pb.FraudEngineClient
	token    string
	deadline time.Duration
	limite   *limitador // por celular (sesión)
	limiteIP *limitador // por IP: generoso, porque toda un aula sale por la misma IP (NAT)
	met      *metricasDemo
	n        atomic.Int64 // número de request (tx_id y tarjetas de escenario)

	mu        sync.Mutex
	conteos   map[string]int // decisión → cantidad, para el presentador
	recientes []punto        // las últimas compras, para el gráfico del proyector
}

// punto: una transacción en el gráfico en vivo del presentador.
type punto struct {
	T         int64   `json:"t"` // Unix ms
	SistemaMs float64 `json:"sistema_ms"`
	Decision  string  `json:"decision"`
}

// maxRecientes acota la memoria del gráfico (de sobra para un minuto de clase).
const maxRecientes = 2000

// resultado de una transacción, como lo ve el celular.
type resultado struct {
	Descripcion string  `json:"descripcion"`
	TxID        string  `json:"tx_id"`
	Decision    string  `json:"decision"` // aprobada | rechazada | error
	Motivo      string  `json:"motivo,omitempty"`
	Degradada   bool    `json:"degradada"`
	Score       float64 `json:"score"`
	SistemaMs   float64 `json:"sistema_ms"` // medido en el borde: lo que cuenta contra los 200ms
	// MotorMs: lo que tardó el motor por dentro (processing_time_ms, lo
	// informa el propio motor). SistemaMs - MotorMs es la red interna y la
	// espera antes de que el motor empiece: bajo saturación, crece.
	MotorMs float64 `json:"motor_ms"`
}

func main() {
	if err := registro.Configurar("gateway"); err != nil {
		registro.Fatal("configuración de logs inválida", "error", err)
	}
	addr := config.Texto("GATEWAY_ADDR", ":8080")
	motorAddr := config.Texto("MOTOR_ADDR", "localhost:50051")
	token := config.Texto("DEMO_TOKEN", "")
	if token == "" {
		// Sin token, cualquiera que encuentre la URL podría mandar tráfico.
		registro.Fatal("falta DEMO_TOKEN (va en el QR)")
	}

	conn, err := grpc.NewClient(motorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		registro.Fatal("cliente gRPC", "error", err)
	}
	defer conn.Close()

	g := &gateway{
		motor:    pb.NewFraudEngineClient(conn),
		token:    token,
		deadline: presupuesto.DeadlineGlobal,
		// Por celular: 2 por segundo y ráfagas de 6, de sobra para apretar
		// botones. Por IP: 40 y 80, porque en un aula todos los celulares
		// salen a internet por la misma IP (el NAT del wifi); sigue frenando
		// a un script que mande miles.
		limite:   nuevoLimitador(2, 6),
		limiteIP: nuevoLimitador(40, 80),
		conteos:  map[string]int{},
	}

	reg := metricas.NuevoRegistro()
	g.met = nuevasMetricasDemo(reg)
	if srvMetricas := metricas.Servir(config.Texto("METRICAS_ADDR", ":2116"), reg); srvMetricas != nil {
		defer srvMetricas.Close()
	}

	srv := &http.Server{Addr: addr, Handler: g.rutas(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		apagar, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(apagar)
	}()

	slog.Info("gateway escuchando", "addr", addr, "motor", motorAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		registro.Fatal("gateway", "error", err)
	}
	slog.Info("gateway apagado")
}

func (g *gateway) rutas() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", g.conToken(g.pagina("web/index.html")))
	m.HandleFunc("GET /presentador", g.conToken(g.pagina("web/presentador.html")))
	m.HandleFunc("GET /qr.png", g.conToken(g.qr))
	m.HandleFunc("POST /api/compra", g.conToken(g.compra))
	m.HandleFunc("GET /api/estado", g.conToken(g.estado))
	m.HandleFunc("GET /api/tarjeta", g.conToken(g.numeroTarjeta))
	return m
}

// conToken exige el token del QR. Comparación en tiempo constante: no
// filtra cuántos caracteres coinciden.
func (g *gateway) conToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := r.URL.Query().Get("t")
		if subtle.ConstantTimeCompare([]byte(t), []byte(g.token)) != 1 {
			http.Error(w, "Token inválido o vencido. Escaneá el QR de la presentación.", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (g *gateway) pagina(ruta string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		html, err := web.ReadFile(ruta)
		if err != nil {
			http.Error(w, "página no encontrada", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(html)
	}
}

// qr genera el QR con la URL de la página del celular, armada con el mismo
// host por el que entró el presentador: sirve igual con una IP pública, un
// nombre de AWS o la red local.
func (g *gateway) qr(w http.ResponseWriter, r *http.Request) {
	url := fmt.Sprintf("http://%s/?t=%s", r.Host, g.token)
	png, err := qrcode.Encode(url, qrcode.Medium, 640)
	if err != nil {
		http.Error(w, "no se pudo generar el QR", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}

func (g *gateway) compra(w http.ResponseWriter, r *http.Request) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if !g.limiteIP.permitir(ip) {
		http.Error(w, "Demasiadas compras desde esta red: esperá un segundo.", http.StatusTooManyRequests)
		return
	}

	var pedido struct {
		Sesion    string `json:"sesion"`
		Escenario string `json:"escenario"`
		Tarjeta   int    `json:"tarjeta"` // número de tarjeta del celular (sube al pedir una nueva)
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&pedido); err != nil {
		http.Error(w, "pedido inválido", http.StatusBadRequest)
		return
	}
	if !sesionValida.MatchString(pedido.Sesion) {
		http.Error(w, "sesión inválida", http.StatusBadRequest)
		return
	}
	if !g.limite.permitir(pedido.Sesion) {
		http.Error(w, "Demasiadas compras seguidas: esperá un segundo.", http.StatusTooManyRequests)
		return
	}
	n := int(g.n.Add(1))
	sig := func() string { return fmt.Sprintf("qr-%s-%d-%d", pedido.Sesion, n, time.Now().UnixNano()%1_000_000) }
	compras, err := armarEscenario(pedido.Escenario, pedido.Sesion, pedido.Tarjeta, n, sig)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// En orden: card testing y viaje imposible dependen de que las
	// anteriores ya hayan pasado por el motor.
	inicio := time.Now()
	resultados := make([]resultado, 0, len(compras))
	for _, c := range compras {
		res := g.evaluar(r.Context(), c)
		g.registrar(res)
		g.met.registrar(pedido.Escenario, c.req.CardToken, res)
		resultados = append(resultados, res)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"resultados":  resultados,
		"deadline_ms": g.deadline.Milliseconds(),
		// Todo lo que pasó dentro del servidor: el celular lo resta de su
		// tiempo total y lo que queda es su internet (ida y vuelta).
		"servidor_ms": float64(time.Since(inicio).Microseconds()) / 1000,
		// El número que el celular muestra: el mismo que aparece en el dashboard.
		"tarjeta_numero": NumeroDeFantasia(TarjetaDe(pedido.Sesion, pedido.Tarjeta)),
	})
}

// evaluar manda UNA transacción al motor. El reloj de los 200ms arranca acá,
// en el borde del sistema: el ctx de la request HTTP (si el celular se va, se
// cancela) más el deadline, que gRPC propaga al motor en grpc-timeout.
func (g *gateway) evaluar(ctx context.Context, c compra) resultado {
	ctx, cancel := context.WithTimeout(ctx, g.deadline)
	defer cancel()
	c.req.EmissionTimestampNs = time.Now().UnixNano()
	inicio := time.Now()
	resp, err := g.motor.EvaluateTransaction(ctx, c.req)
	res := resultado{
		Descripcion: c.descripcion,
		TxID:        c.req.TxId,
		SistemaMs:   float64(time.Since(inicio).Microseconds()) / 1000,
	}
	switch {
	case err == nil:
		res.Decision = "aprobada"
		if resp.IsFraud {
			res.Decision, res.Motivo = "rechazada", resp.RejectionReason
		}
		res.Degradada, res.Score = resp.IsDegraded, resp.RiskScore
		res.MotorMs = float64(resp.ProcessingTimeMs)
	default:
		res.Decision, res.Motivo = "error", motivoDeError(err)
	}
	return res
}

// numeroTarjeta devuelve el número de fantasía de la tarjeta de un celular:
// lo calcula el servidor para que el celular y el dashboard muestren el mismo.
func (g *gateway) numeroTarjeta(w http.ResponseWriter, r *http.Request) {
	sesion := r.URL.Query().Get("sesion")
	n, err := strconv.Atoi(r.URL.Query().Get("tarjeta"))
	if !sesionValida.MatchString(sesion) || err != nil || n < 0 || n >= maxTarjetas {
		http.Error(w, "sesión o tarjeta inválida", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"numero": NumeroDeFantasia(TarjetaDe(sesion, n))})
}

// motivoDeError: el motivo del ErrorInfo si lo hay (EARLY_DROP,
// SETTLEMENT_TIMEOUT...); si no, lo que pasó del lado de este cliente.
func motivoDeError(err error) string {
	if m := fallas.Motivo(err); m != "" {
		return m
	}
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return "DEADLINE_MISS" // venció el plazo de 200ms esperando
	case codes.Unavailable:
		return "MOTOR_NO_DISPONIBLE"
	default:
		return status.Code(err).String()
	}
}

// registrar suma la compra a los contadores y al gráfico del presentador.
func (g *gateway) registrar(res resultado) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.conteos[res.Decision]++
	if len(g.recientes) >= maxRecientes {
		g.recientes = g.recientes[maxRecientes/2:] // descarta la mitad más vieja
	}
	g.recientes = append(g.recientes, punto{time.Now().UnixMilli(), res.SistemaMs, res.Decision})
}

func (g *gateway) estado(w http.ResponseWriter, r *http.Request) {
	desde := time.Now().Add(-time.Minute).UnixMilli()
	g.mu.Lock()
	conteos := make(map[string]int, len(g.conteos))
	for k, v := range g.conteos {
		conteos[k] = v
	}
	var ultimoMinuto []punto
	for _, p := range g.recientes {
		if p.T >= desde {
			ultimoMinuto = append(ultimoMinuto, p)
		}
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"conteos": conteos, "recientes": ultimoMinuto, "ahora": time.Now().UnixMilli()})
}
