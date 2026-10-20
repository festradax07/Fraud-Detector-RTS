// Fraud-Engine: decide si una transacción se aprueba o rechaza bajo un
// deadline duro de 200ms.
//
// El recorrido de cada transacción (EvaluateTransaction) es:
//
//	admitir → validar → consultar (Redis, 1 viaje) → reglas mandatorias →
//	fase opcional según el slack → posición confiable → liquidar (Settlement)
//
// Las decisiones de diseño de cada paso están en docs/adr/.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"fd_rts/internal/config"
	"fd_rts/internal/fallas"
	"fd_rts/internal/mandatoria"
	"fd_rts/internal/metricas"
	"fd_rts/internal/opcional"
	"fd_rts/internal/presupuesto"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/registro"
	"fd_rts/internal/servicio"
	"fd_rts/internal/settlement"
	pb "fd_rts/proto"
)

// Configuración por variables de entorno (ADR-0017).
const (
	// iteracionesPorDefecto fija el costo de CPU SIMULADO del scoring (el
	// Z-score real tarda ~40ns; esto simula un modelo más pesado, ADR-0011).
	// Medido en la máquina de desarrollo: 10M ≈ 16ms de media, ≈ 19ms de
	// máximo, cómodo dentro de WCET(O) = 80ms. Es una perilla, no un valor
	// del modelo: se cambia por entorno (ITERACIONES_SCORING).
	iteracionesPorDefecto = 10_000_000

	redisAddrPorDefecto = "localhost:6379"
)

// configuracion del Fraud-Engine, leída del entorno.
type configuracion struct {
	grpcAddr       string        // GRPC_ADDR: dónde escucha el servicio
	metricasAddr   string        // METRICAS_ADDR: /metrics ("" = no exponer)
	redisAddr      string        // REDIS_ADDR
	redisPrefijo   string        // REDIS_PREFIJO: prefijo de las claves
	settlementAddr string        // SETTLEMENT_ADDR
	iteraciones    int           // ITERACIONES_SCORING: costo simulado del scoring
	esperaApagado  time.Duration // APAGADO_ESPERA: máximo para drenar requests en vuelo
	avisoApagado   time.Duration // APAGADO_AVISO: NOT_SERVING antes de dejar de atender
}

func leerConfiguracion() (configuracion, error) {
	c := configuracion{
		grpcAddr:       config.Texto("GRPC_ADDR", ":50051"),
		metricasAddr:   config.Texto("METRICAS_ADDR", ":2112"),
		redisAddr:      config.Texto("REDIS_ADDR", redisAddrPorDefecto),
		redisPrefijo:   config.Texto("REDIS_PREFIJO", redisclient.PrefijoPorDefecto),
		settlementAddr: config.Texto("SETTLEMENT_ADDR", "localhost:50052"),
	}
	var err error
	if c.iteraciones, err = config.Entero("ITERACIONES_SCORING", iteracionesPorDefecto); err != nil {
		return c, err
	}
	if c.iteraciones < 0 {
		return c, fmt.Errorf("ITERACIONES_SCORING=%d: no puede ser negativo", c.iteraciones)
	}
	// 5s: las requests en vuelo terminan en < 200ms (el deadline), así que
	// sobra; y queda por debajo de los plazos de los orquestadores antes del
	// SIGKILL (10s en Compose, 30s por defecto en ECS).
	if c.esperaApagado, err = config.Duracion("APAGADO_ESPERA", 5*time.Second); err != nil {
		return c, err
	}
	// 0 en local (no hay balanceador que tenga que enterarse). Detrás de un
	// balanceador, unos segundos (lo que tarde en ver el NOT_SERVING).
	if c.avisoApagado, err = config.Duracion("APAGADO_AVISO", 0); err != nil {
		return c, err
	}
	return c, nil
}

type fraudEngineServer struct {
	pb.UnimplementedFraudEngineServer
	verificador *mandatoria.Verificador
	// settle es el cliente gRPC de Settlement: el Fraud-Engine es servidor
	// (de los clientes) y cliente (de Settlement).
	settle pb.SettlementClient
	// iteraciones de costo simulado del scoring (ITERACIONES_SCORING).
	iteraciones int
	// metricas del modelo temporal. Puede ser nil: los métodos no hacen nada
	// en ese caso.
	metricas *metricas.Motor
}

func (s *fraudEngineServer) EvaluateTransaction(ctx context.Context, req *pb.TransactionRequest) (*pb.EvaluationResponse, error) {
	inicio := time.Now()

	deadline, err := s.admitir(ctx)
	if err != nil {
		return nil, err
	}
	if err := validar(req); err != nil {
		return nil, err
	}

	// --- Fase mandatoria (M_i) ---
	res, stats, err := s.consultar(ctx, req, inicio)
	if err != nil {
		return nil, err
	}
	if motivo := mandatoria.Evaluar(res); motivo != "" {
		if registro.Depurando(ctx) {
			slog.DebugContext(ctx, "fraude", "tx_id", req.TxId, "motivo", motivo,
				"intentos", res.Intentos, "kmh", res.Kmh)
		}
		return s.liquidar(ctx, req, rechazo(req, motivo, 0), inicio)
	}

	// --- Fase opcional (O_i) ---
	score, degradada := s.faseOpcional(ctx, req, stats, deadline)
	if score > opcional.UmbralRechazo {
		if registro.Depurando(ctx) {
			slog.DebugContext(ctx, "fraude", "tx_id", req.TxId, "motivo", opcional.MotivoRiesgoAlto, "score", score)
		}
		return s.liquidar(ctx, req, rechazo(req, opcional.MotivoRiesgoAlto, score), inicio)
	}

	// Pasó todos los chequeos: recién ahora esta posición pasa a ser
	// confiable (viaje 2 a Redis). Va DESPUÉS de la opcional (un rechazo por
	// riesgo tampoco puede mover la tarjeta) y ANTES de Settlement (si no, un
	// fallo de este SET después de un commit daría "error" sobre algo ya
	// comprometido). Ver ADR-0006.
	if err := s.verificador.ActualizarPosicion(ctx, req.CardToken, req.Latitude, req.Longitude, inicio); err != nil {
		return nil, fallaDeVerificacion(req.TxId, err)
	}
	if registro.Depurando(ctx) {
		slog.DebugContext(ctx, "aprobada por el motor", "tx_id", req.TxId, "score", score, "degradada", degradada)
	}
	return s.liquidar(ctx, req, &pb.EvaluationResponse{
		TxId:       req.TxId,
		RiskScore:  score,
		IsDegraded: degradada,
	}, inicio)
}

// admitir es el control de admisión (ADR-0003): va ANTES de cualquier
// trabajo. Si no queda tiempo para lo mínimo, se rechaza en el acto (load
// shedding). Devuelve el deadline de la request.
//
// Una llamada sin deadline se rechaza: es un bug del cliente, y procesarla
// "sin límite" lo taparía (ADR-0002).
func (s *fraudEngineServer) admitir(ctx context.Context) (time.Time, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}, fallas.Nueva(codes.InvalidArgument, fallas.SinDeadline, "la llamada debe traer un deadline")
	}
	restante := time.Until(deadline) // D_rem
	// Si este valor baja de ~200ms, hay espera ANTES del handler.
	s.metricas.DRemLlegada(restante)
	if !presupuesto.Admitir(restante) {
		return time.Time{}, fallas.Nueva(codes.DeadlineExceeded, fallas.EarlyDrop,
			fmt.Sprintf("deadline insuficiente: quedan %v, se necesitan al menos %v", restante, presupuesto.MinParaAdmitir))
	}
	return deadline, nil
}

// validar rechaza las requests con las que no se puede decidir (ADR-0007).
// Es un error del cliente (InvalidArgument), no un fraude. Función pura.
func validar(req *pb.TransactionRequest) error {
	invalida := func(msg string) error {
		return fallas.Nueva(codes.InvalidArgument, fallas.RequestInvalida, msg)
	}
	switch {
	case req.TxId == "":
		// Es el miembro del ZSET de velocidad y la clave de idempotencia.
		return invalida("falta tx_id")
	case req.CardToken == "":
		return invalida("falta card_token")
	case !mandatoria.CoordenadasValidas(req.Latitude, req.Longitude):
		return invalida(fmt.Sprintf("coordenadas inválidas o ausentes: (%v, %v)", req.Latitude, req.Longitude))
	case !(req.Amount > 0):
		// El scoring usa ln(monto). !(x > 0) y no x <= 0 a propósito: NaN no
		// es mayor ni menor que nada, así que !(NaN > 0) también rechaza.
		return invalida(fmt.Sprintf("amount inválido: %v", req.Amount))
	case req.MerchantCategory == "":
		return invalida("falta merchant_category")
	default:
		return nil
	}
}

// consultar hace el viaje 1 a Redis: registra el intento (antes de
// cualquier rechazo: la velocidad cuenta todos los intentos, ADR-0006) y
// trae blacklist, intentos, posición previa y las estadísticas de la
// categoría. Va con el ctx de la request: el deadline del cliente llega
// hasta Redis.
func (s *fraudEngineServer) consultar(ctx context.Context, req *pb.TransactionRequest, ahora time.Time) (mandatoria.Resultado, opcional.Estadisticas, error) {
	t := time.Now()
	res, err := s.verificador.Consultar(ctx, req.TxId, req.CardToken, req.MerchantCategory, req.Latitude, req.Longitude, ahora)
	s.metricas.Fase(metricas.FaseMandatoria, time.Since(t))
	if err != nil {
		return res, opcional.Estadisticas{}, fallaDeVerificacion(req.TxId, err)
	}

	// Una categoría sin estadísticas se rechaza aunque la request fuera a
	// degradarse: sin estadísticas no hay cómo puntuar. El intento ya quedó
	// contado en la ventana (el ZADD iba en el mismo pipeline): contar de más
	// es el lado seguro.
	stats, conocida, err := opcional.ParsearEstadisticas(res.StatsCategoria)
	if err != nil {
		return res, stats, fallaDeVerificacion(req.TxId, err) // datos corruptos en Redis
	}
	if !conocida {
		return res, stats, fallas.Nueva(codes.InvalidArgument, fallas.CategoriaDesconocida,
			fmt.Sprintf("merchant_category desconocida: %q", req.MerchantCategory))
	}
	return res, stats, nil
}

// faseOpcional decide con el slack si corre el scoring (ADR-0004) y, si
// corre, devuelve el score. D_rem se recalcula ACÁ: importa cuánto queda
// ahora, con la mandatoria y el viaje a Redis ya pagados. Sin slack, la
// respuesta sale degradada (solo mandatoria): se acepta el riesgo de no
// haber puntuado a cambio de cumplir el deadline.
func (s *fraudEngineServer) faseOpcional(ctx context.Context, req *pb.TransactionRequest, stats opcional.Estadisticas, deadline time.Time) (score float64, degradada bool) {
	restante := time.Until(deadline)
	slack := presupuesto.Slack(restante)
	s.metricas.Slack(slack)
	if !presupuesto.CorreOpcional(restante) {
		if registro.Depurando(ctx) {
			slog.DebugContext(ctx, "degradada", "tx_id", req.TxId, "restante", restante, "slack", slack)
		}
		return 0, true
	}
	t := time.Now()
	score = opcional.Puntuar(req.Amount, stats, s.iteraciones)
	s.metricas.Fase(metricas.FaseOpcional, time.Since(t))
	return score, false
}

// rechazo arma la respuesta de un fraude. Un fraude NO es un error gRPC: el
// motor funcionó bien y decidió "no", así que va con código OK y el motivo
// en el cuerpo (ADR-0005).
func rechazo(req *pb.TransactionRequest, motivo string, score float64) *pb.EvaluationResponse {
	return &pb.EvaluationResponse{
		TxId:            req.TxId,
		IsFraud:         true,
		RejectionReason: motivo,
		RiskScore:       score,
	}
}

// liquidar manda la decisión a Settlement y arma la respuesta final
// (ADR-0012).
//
// La llamada tiene su propio timeout: ReservaSettleNet (C_settle + C_net),
// lo que el slack ya le reserva. context.WithTimeout NUNCA alarga el
// deadline: si al ctx de la request le queda menos, gana el que vence
// primero.
//
// Política ante una falla de Settlement:
//   - Aprobada: fail-closed. Sin commit confirmado no se responde "aprobada".
//   - Rechazada: la decisión de rechazar ya es la segura. Se responde igual
//     y queda en el log que se perdió el registro.
func (s *fraudEngineServer) liquidar(ctx context.Context, req *pb.TransactionRequest, eval *pb.EvaluationResponse, inicio time.Time) (*pb.EvaluationResponse, error) {
	ctxSettle, cancel := context.WithTimeout(ctx, presupuesto.ReservaSettleNet)
	defer cancel()

	var trailer metadata.MD
	t := time.Now()
	sr, err := s.settle.CommitTransaction(ctxSettle, &pb.SettlementRequest{
		TxId:                req.TxId,
		UserId:              req.UserId,
		Amount:              req.Amount,
		Evaluation:          eval,
		EmissionTimestampNs: req.EmissionTimestampNs,
	}, grpc.Trailer(&trailer))
	total := time.Since(t)
	s.medirSettlement(total, trailer)

	eval.ProcessingTimeMs = time.Since(inicio).Milliseconds()

	if err != nil {
		if eval.IsFraud {
			slog.WarnContext(ctx, "registro perdido: el rechazo se responde igual", "tx_id", req.TxId, "error", err)
			return eval, nil
		}
		return nil, fallaDeSettlement(ctx, req.TxId, err)
	}
	return respuestaSegunRegistro(ctx, req.TxId, eval, sr)
}

// medirSettlement registra la duración de la llamada y, si vino el trailer,
// la separa en C_settle (adentro de Settlement) y C_net (red + gRPC).
func (s *fraudEngineServer) medirSettlement(total time.Duration, trailer metadata.MD) {
	s.metricas.Fase(metricas.FaseSettlement, total)
	if cSettle, ok := leerCSettle(trailer); ok {
		s.metricas.Fase(metricas.FaseCSettle, cSettle)
		s.metricas.Fase(metricas.FaseCNet, total-cSettle)
	}
}

// respuestaSegunRegistro devuelve lo REGISTRADO por Settlement, no la
// evaluación de este intento: en un reintento (mismo tx_id) Settlement
// devuelve el primer resultado final, que puede diferir si algo cambió entre
// medio (por ejemplo, la tarjeta entró en la blacklist). Si la plata ya se
// comprometió, decirle "fraude" al cliente sería mentirle (ADR-0013).
func respuestaSegunRegistro(ctx context.Context, txID string, eval *pb.EvaluationResponse, sr *pb.SettlementResponse) (*pb.EvaluationResponse, error) {
	switch sr.Status {
	case pb.TxStatus_STATUS_COMMITTED:
		if eval.IsFraud {
			slog.InfoContext(ctx, "reintento: ya estaba comprometida", "tx_id", txID, "evaluado", eval.RejectionReason)
			eval.IsFraud, eval.RejectionReason = false, ""
		}
		return eval, nil
	case pb.TxStatus_STATUS_REJECTED_FRAUD:
		if !eval.IsFraud {
			slog.InfoContext(ctx, "reintento: ya estaba rechazada", "tx_id", txID, "motivo", sr.Message)
			eval.IsFraud, eval.RejectionReason = true, sr.Message
		}
		return eval, nil
	case pb.TxStatus_STATUS_ABORTED_TIMEOUT:
		// Settlement vio que se pasó el deadline global: no comprometió.
		slog.WarnContext(ctx, "abortada por Settlement: superó el deadline global", "tx_id", txID, "latencia_e2e_ms", sr.EndToEndLatencyMs)
		if eval.IsFraud {
			return eval, nil // el rechazo vale igual
		}
		return nil, fallas.Nueva(codes.DeadlineExceeded, fallas.AbortadaPorTimeout, "la transacción superó el deadline global y no se comprometió")
	default:
		return nil, fallas.Nueva(codes.Internal, fallas.EstadoInesperado, fmt.Sprintf("estado inesperado de Settlement: %v", sr.Status))
	}
}

// leerCSettle saca C_settle del trailer de Settlement. ok es false si no
// vino (por ejemplo, si la llamada ni siquiera llegó a Settlement).
func leerCSettle(trailer metadata.MD) (time.Duration, bool) {
	v := trailer.Get(settlement.TrailerSettleUs)
	if len(v) != 1 {
		return 0, false
	}
	us, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(us) * time.Microsecond, true
}

// fallaDeSettlement traduce el error de la llamada a Settlement. Ojo: un
// error de una llamada gRPC es un *status*, no el error de context
// original, así que errors.Is(err, context.DeadlineExceeded) NO funciona:
// hay que mirar status.Code(err).
func fallaDeSettlement(ctx context.Context, txID string, err error) error {
	slog.WarnContext(ctx, "sin commit", "tx_id", txID, "error", err)
	if status.Code(err) == codes.DeadlineExceeded {
		// AMBIGUO: pudo haberse comprometido igual (ADR-0013).
		return fallas.Nueva(codes.DeadlineExceeded, fallas.SettlementTimeout, "se agotó el tiempo esperando a Settlement")
	}
	return fallas.Nueva(codes.Unavailable, fallas.SettlementNoDisponible, "no se pudo comprometer la transacción")
}

// fallaDeVerificacion traduce un error de Redis a un código gRPC. Es
// fail-closed: no se aprueba lo que no se pudo verificar. No es un fraude:
// es que el motor NO PUDO decidir (ADR-0009).
func fallaDeVerificacion(txID string, err error) error {
	slog.Warn("sin verificar", "tx_id", txID, "error", err)
	if errors.Is(err, context.DeadlineExceeded) {
		return fallas.Nueva(codes.DeadlineExceeded, fallas.RedisTimeout, "se agotó el deadline verificando la transacción")
	}
	return fallas.Nueva(codes.Unavailable, fallas.RedisNoDisponible, "no se pudo verificar la transacción")
}

func main() {
	if err := registro.Configurar("fraud_engine"); err != nil {
		registro.Fatal("configuración de logs inválida", "error", err)
	}
	cfg, err := leerConfiguracion()
	if err != nil {
		// Mejor no arrancar que correr con una configuración que no es la
		// que se pidió.
		registro.Fatal("configuración inválida", "error", err)
	}
	slog.Info("configuración", "grpc", cfg.grpcAddr, "metricas", cfg.metricasAddr, "redis", cfg.redisAddr,
		"prefijo", cfg.redisPrefijo, "settlement", cfg.settlementAddr, "iteraciones", cfg.iteraciones,
		"apagado", cfg.esperaApagado, "aviso", cfg.avisoApagado)

	ctx, stop := servicio.ContextoDeApagado()
	defer stop()

	// Los defer de los clientes corren DESPUÉS de servicio.Correr: las
	// requests en vuelo los necesitan hasta el final del apagado.
	rdb, err := redisclient.Conectar(ctx, cfg.redisAddr)
	if err != nil {
		registro.Fatal("no se pudo conectar a Redis", "error", err)
	}
	defer rdb.Close()

	lis, err := net.Listen("tcp", cfg.grpcAddr)
	if err != nil {
		registro.Fatal("no se pudo escuchar", "addr", cfg.grpcAddr, "error", err)
	}

	// Cliente de Settlement. NewClient no conecta todavía; Connect() arranca
	// la conexión en segundo plano para que la primera transacción no pague
	// el costo de conectar. Si Settlement no está, el motor arranca igual:
	// cada transacción que lo necesite falla rápido (fail-closed).
	connSettle, err := grpc.NewClient(cfg.settlementAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		registro.Fatal("cliente de Settlement", "error", err)
	}
	defer connSettle.Close()
	connSettle.Connect()

	// Métricas (ADR-0015): registro propio, expuesto en /metrics. El
	// interceptor mide cada llamada; las decisiones salen de la respuesta.
	reg := metricas.NuevoRegistro()
	rpc := metricas.NuevoRPC(reg, "fraud_engine")
	motor := metricas.NuevoMotor(reg)
	srvMetricas := metricas.Servir(cfg.metricasAddr, reg)

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(rpc.Interceptor(func(resp any) {
		if r, ok := resp.(*pb.EvaluationResponse); ok {
			motor.Decision(r.IsFraud, r.RejectionReason, r.IsDegraded)
		}
	})))
	pb.RegisterFraudEngineServer(grpcServer, &fraudEngineServer{
		verificador: mandatoria.NuevoVerificador(rdb, cfg.redisPrefijo),
		settle:      pb.NewSettlementClient(connSettle),
		iteraciones: cfg.iteraciones,
		metricas:    motor,
	})
	// Salud estándar de gRPC (ADR-0020), con Redis ya verificado: SERVING =
	// listo para atender.
	salud := servicio.NuevaSalud(grpcServer)

	slog.Info("fraud-engine escuchando", "addr", cfg.grpcAddr)
	plazos := servicio.Plazos{Aviso: cfg.avisoApagado, Espera: cfg.esperaApagado}
	if err := servicio.Correr(ctx, grpcServer, lis, salud, srvMetricas, plazos); err != nil {
		slog.Warn("fraud-engine", "error", err)
	}
	slog.Info("fraud-engine apagado")
}
