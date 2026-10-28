// eventos: los consumidores del cold path (ADR-0019, ADR-0025). Un binario,
// varios roles; cada rol corre en su propio contenedor y con su propio
// consumer group, así cada uno recibe TODOS los eventos.
//
//	eventos -rol auditoria      cruza evaluadas con liquidadas, detecta discrepancias
//	eventos -rol alertas        fraudes por motivo
//	eventos -rol estadisticas   estadísticas en vivo, en modo sombra
//	eventos -rol volcar         muestra los eventos como JSON (para inspeccionar)
//
// Nada de esto afecta al camino de decisión: si un consumidor se atrasa o se
// cae, las transacciones siguen igual (los eventos esperan en Kafka).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"fd_rts/internal/auditoria"
	"fd_rts/internal/config"
	"fd_rts/internal/consumo"
	"fd_rts/internal/estadisticas"
	"fd_rts/internal/eventos"
	"fd_rts/internal/mandatoria"
	"fd_rts/internal/metricas"
	"fd_rts/internal/opcional"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/registro"
	"fd_rts/internal/servicio"
	pb "fd_rts/proto"
)

func main() {
	rol := flag.String("rol", "", "auditoria | alertas | estadisticas | volcar")
	metricasAddr := flag.String("metricas", ":2115", "dónde exponer /metrics (vacío: no exponer)")
	dir := flag.String("dir", "auditoria", "auditoria: carpeta de los archivos JSONL")
	ventana := flag.Duration("ventana", 30*time.Second, "auditoria: cuánto esperar la pareja de cada evento")
	cada := flag.Duration("cada", 10*time.Second, "estadisticas: cada cuánto escribir y comparar con la línea base")
	desdeInicio := flag.Bool("desde-inicio", false, "volcar: leer desde el principio (si no, solo lo nuevo)")
	flag.Parse()

	if err := registro.Configurar("eventos_" + *rol); err != nil {
		registro.Fatal("configuración de logs inválida", "error", err)
	}
	brokers := eventos.Brokers(config.Texto("KAFKA_BROKERS", "localhost:9092"))
	if len(brokers) == 0 {
		registro.Fatal("KAFKA_BROKERS vacío: los consumidores necesitan Kafka")
	}

	ctx, stop := servicio.ContextoDeApagado()
	defer stop()

	reg := metricas.NuevoRegistro()
	srvMetricas := metricas.Servir(*metricasAddr, reg)
	defer func() {
		if srvMetricas != nil {
			srvMetricas.Close()
		}
	}()

	topics := eventos.TopicsPorDefecto
	var err error
	switch *rol {
	case "auditoria":
		err = auditar(ctx, brokers, topics, "drts-auditoria", *dir, *ventana, reg)
	case "alertas":
		// Bloqueo automático (ADR-0029): con BLOQUEO_AUTOMATICO, los fraudes
		// con esos motivos bloquean la tarjeta (la agregan a la blacklist).
		var bloqueo *bloqueoAutomatico
		if motivos := motivosDeBloqueo(config.Texto("BLOQUEO_AUTOMATICO", "")); len(motivos) > 0 {
			rdb, errR := redisclient.Conectar(ctx, config.Texto("REDIS_ADDR", "localhost:6379"))
			if errR != nil {
				registro.Fatal("no se pudo conectar a Redis", "error", errR)
			}
			defer rdb.Close()
			prefijo := config.Texto("REDIS_PREFIJO", redisclient.PrefijoPorDefecto)
			bloqueo = &bloqueoAutomatico{verificador: mandatoria.NuevoVerificador(rdb, prefijo), motivos: motivos}
			slog.Info("bloqueo automático activado", "motivos", config.Texto("BLOQUEO_AUTOMATICO", ""))
		}
		err = alertar(ctx, brokers, topics, "drts-alertas", bloqueo, reg)
	case "estadisticas":
		var rdb *redis.Client
		if rdb, err = redisclient.Conectar(ctx, config.Texto("REDIS_ADDR", "localhost:6379")); err != nil {
			registro.Fatal("no se pudo conectar a Redis", "error", err)
		}
		defer rdb.Close()
		prefijo := config.Texto("REDIS_PREFIJO", redisclient.PrefijoPorDefecto)
		err = estadisticasEnSombra(ctx, brokers, topics, "drts-estadisticas", rdb, prefijo, *cada, reg)
	case "volcar":
		err = volcar(ctx, brokers, topics, *desdeInicio)
	default:
		registro.Fatal("rol desconocido", "rol", *rol, "validos", "auditoria, alertas, estadisticas, volcar")
	}
	if err != nil {
		slog.Warn("consumidor terminó con error", "rol", *rol, "error", err)
	}
	slog.Info("consumidor apagado", "rol", *rol)
}

// ---------------------------------------------------------------- auditoría

// auditar cruza los dos topics por tx_id, escribe cada transacción
// clasificada en el archivo del día y cuenta los resultados.
//
// Limitación conocida: los offsets se confirman al procesar cada lote, no
// al cerrar la ventana. Si el consumidor se cae con transacciones esperando
// su pareja, esas no se clasifican (al volver no se releen). Para hacerlo
// bien habría que confirmar solo hasta el evento más viejo todavía abierto.
func auditar(ctx context.Context, brokers []string, topics eventos.Topics, grupo, dir string, ventana time.Duration, reg prometheus.Registerer) error {
	archivo, err := auditoria.NuevoArchivo(dir)
	if err != nil {
		return err
	}
	defer archivo.Cerrar()

	cruce := auditoria.NuevoCruce(ventana)
	resultados := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "drts_auditoria_resultados_total",
		Help: "Transacciones cruzadas, por resultado (CONSISTENTE, NO_COMPROMETIDA, AMBIGUA_COMPROMETIDA, REGISTRO_PERDIDO, EVENTO_PERDIDO, INCONSISTENTE).",
	}, []string{"resultado"})
	reg.MustRegister(resultados, prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "drts_auditoria_pendientes",
		Help: "Transacciones esperando a que llegue su pareja (dentro de la ventana).",
	}, func() float64 { return float64(cruce.Pendientes()) }))
	for _, r := range auditoria.Resultados {
		resultados.WithLabelValues(string(r)) // en 0 desde el arranque
	}

	// Cada segundo: cerrar lo que venció y bajarlo al disco.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ahora := <-t.C:
				for _, c := range cruce.Cerrar(ahora) {
					resultados.WithLabelValues(string(c.Resultado)).Inc()
					if c.Resultado.EsDiscrepancia() {
						slog.Warn("auditoría: discrepancia", "tx_id", c.TxID, "resultado", c.Resultado)
					}
					if err := archivo.Escribir(c, ahora); err != nil {
						slog.Error("auditoría: no se pudo escribir", "tx_id", c.TxID, "error", err)
					}
				}
				if err := archivo.Sincronizar(); err != nil {
					slog.Error("auditoría: no se pudo sincronizar el archivo", "error", err)
				}
			}
		}
	}()

	m := consumo.NuevasMetricas(reg, grupo)
	cfg := consumo.Config{Brokers: brokers, Grupo: grupo, Topics: []string{topics.Evaluadas, topics.Liquidadas}}
	err = consumo.Correr(ctx, cfg, m, func(ctx context.Context, r *kgo.Record) error {
		switch r.Topic {
		case topics.Evaluadas:
			var ev pb.TransaccionEvaluada
			if err := proto.Unmarshal(r.Value, &ev); err != nil {
				return err
			}
			cruce.Evaluada(&ev, time.Now())
		case topics.Liquidadas:
			var ev pb.TransaccionLiquidada
			if err := proto.Unmarshal(r.Value, &ev); err != nil {
				return err
			}
			cruce.Liquidada(&ev, time.Now())
		}
		return nil
	})
	wg.Wait()
	return err
}

// ------------------------------------------------------------------ alertas

// alertar cuenta y registra los fraudes por motivo. En un sistema real, acá
// se abriría un caso en el sistema de gestión de fraude.
// bloqueoAutomatico: qué motivos de fraude bloquean la tarjeta, y dónde.
type bloqueoAutomatico struct {
	verificador *mandatoria.Verificador
	motivos     map[string]bool
}

// motivosDeBloqueo convierte "CARD_TESTING,VIAJE_IMPOSIBLE" en un conjunto.
// Vacío = sin bloqueo automático.
func motivosDeBloqueo(lista string) map[string]bool {
	m := map[string]bool{}
	for _, x := range strings.Split(lista, ",") {
		if x = strings.TrimSpace(x); x != "" {
			m[x] = true
		}
	}
	return m
}

// alertar registra cada fraude detectado. Con bloqueo != nil, además bloquea
// la tarjeta cuando el motivo es una señal fuerte (ADR-0029): lo hace el
// camino frío, no el motor, así que el bloqueo llega unos milisegundos
// después (eventualmente consistente), como en un sistema real.
func alertar(ctx context.Context, brokers []string, topics eventos.Topics, grupo string, bloqueo *bloqueoAutomatico, reg prometheus.Registerer) error {
	alertas := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "drts_alertas_total", Help: "Fraudes detectados, por motivo.",
	}, []string{"motivo"})
	bloqueos := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "drts_bloqueos_total", Help: "Tarjetas bloqueadas automáticamente por el camino frío, por motivo del fraude (ADR-0029).",
	}, []string{"motivo"})
	reg.MustRegister(alertas, bloqueos)
	for _, m := range []string{mandatoria.MotivoBlacklist, mandatoria.MotivoCardTesting, mandatoria.MotivoViajeImposible, opcional.MotivoRiesgoAlto} {
		alertas.WithLabelValues(m) // en 0 desde el arranque
	}
	if bloqueo != nil {
		for m := range bloqueo.motivos {
			bloqueos.WithLabelValues(m)
		}
	}

	m := consumo.NuevasMetricas(reg, grupo)
	cfg := consumo.Config{Brokers: brokers, Grupo: grupo, Topics: []string{topics.Evaluadas}}
	return consumo.Correr(ctx, cfg, m, func(ctx context.Context, r *kgo.Record) error {
		var ev pb.TransaccionEvaluada
		if err := proto.Unmarshal(r.Value, &ev); err != nil {
			return err
		}
		if ev.Evaluacion.GetIsFraud() {
			motivo := ev.Evaluacion.GetRejectionReason()
			alertas.WithLabelValues(motivo).Inc()
			slog.Info("alerta de fraude", "tx_id", ev.TxId, "motivo", motivo, "categoria", ev.MerchantCategory)
			if bloqueo != nil && bloqueo.motivos[motivo] {
				// SADD es idempotente: si el evento llega dos veces (al menos
				// una vez), bloquear de nuevo no cambia nada. Si falla, se
				// devuelve el error y el evento se vuelve a procesar.
				if err := bloqueo.verificador.Bloquear(ctx, ev.CardToken); err != nil {
					return fmt.Errorf("bloqueando %s: %w", ev.CardToken, err)
				}
				bloqueos.WithLabelValues(motivo).Inc()
				slog.Info("tarjeta bloqueada", "tarjeta", ev.CardToken, "motivo", motivo, "tx_id", ev.TxId)
			}
		}
		return nil
	})
}

// ------------------------------------------------------ estadísticas (sombra)

// sombra acumula ln(monto) por categoría de las transacciones aprobadas.
type sombra struct {
	mu  sync.Mutex
	por map[string]*estadisticas.Acumulador
}

func (s *sombra) agregar(categoria string, monto float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.por[categoria]
	if !ok {
		a = &estadisticas.Acumulador{}
		s.por[categoria] = a
	}
	a.Agregar(math.Log(monto))
}

// copia devuelve una instantánea, para escribirla sin tener tomado el lock.
func (s *sombra) copia() map[string]estadisticas.Acumulador {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make(map[string]estadisticas.Acumulador, len(s.por))
	for k, v := range s.por {
		c[k] = *v
	}
	return c
}

// estadisticasEnSombra calcula las estadísticas en vivo por categoría y las
// guarda en claves APARTE (stats_vivo:*), que el motor no usa: modo sombra.
//
// Por qué sombra: el sistema no tiene una señal de "transacción confirmada
// como legítima" (en la realidad es el contracargo, días después). Aprender
// directo de las aprobadas dejaría que un atacante paciente corra la media
// con compras que pasan. Primero se observa: cuánto se alejan de la línea
// base, en desvíos (drts_stats_vivo_deriva_sigmas).
func estadisticasEnSombra(ctx context.Context, brokers []string, topics eventos.Topics, grupo string, rdb *redis.Client, prefijo string, cada time.Duration, reg prometheus.Registerer) error {
	s := &sombra{por: map[string]*estadisticas.Acumulador{}}
	deriva := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "drts_stats_vivo_deriva_sigmas",
		Help: "Cuánto se alejó la media en vivo de ln(monto) de la línea base, en desvíos de la línea base (modo sombra: el motor no la usa).",
	}, []string{"categoria"})
	muestras := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "drts_stats_vivo_muestras",
		Help: "Transacciones aprobadas acumuladas por categoría en las estadísticas en vivo.",
	}, []string{"categoria"})
	reg.MustRegister(deriva, muestras)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(cada)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for cat, a := range s.copia() {
					if err := guardarSombra(ctx, rdb, prefijo, cat, a); err != nil {
						slog.Warn("estadísticas: no se pudo guardar", "categoria", cat, "error", err)
						continue
					}
					muestras.WithLabelValues(cat).Set(float64(a.N))
					if base, ok := lineaBase(ctx, rdb, prefijo, cat); ok {
						deriva.WithLabelValues(cat).Set((a.Media - base.MediaLog) / base.DesvioLog)
					}
				}
			}
		}
	}()

	m := consumo.NuevasMetricas(reg, grupo)
	cfg := consumo.Config{Brokers: brokers, Grupo: grupo, Topics: []string{topics.Evaluadas}}
	err := consumo.Correr(ctx, cfg, m, func(ctx context.Context, r *kgo.Record) error {
		var ev pb.TransaccionEvaluada
		if err := proto.Unmarshal(r.Value, &ev); err != nil {
			return err
		}
		// Solo las aprobadas: el motor responde "aprobada" con el commit ya
		// confirmado. Reintentos del mismo tx_id: con miles de muestras por
		// categoría, un duplicado ocasional no mueve la media.
		if ev.Evaluacion != nil && !ev.Evaluacion.IsFraud && ev.Amount > 0 {
			s.agregar(ev.MerchantCategory, ev.Amount)
		}
		return nil
	})
	wg.Wait()
	return err
}

func guardarSombra(ctx context.Context, rdb *redis.Client, prefijo, cat string, a estadisticas.Acumulador) error {
	return rdb.HSet(ctx, prefijo+"stats_vivo:"+cat, map[string]any{
		opcional.CampoMediaLog:  a.Media,
		opcional.CampoDesvioLog: a.Desvio(),
		"n":                     a.N,
	}).Err()
}

func lineaBase(ctx context.Context, rdb *redis.Client, prefijo, cat string) (opcional.Estadisticas, bool) {
	h, err := rdb.HGetAll(ctx, prefijo+"stats:"+cat).Result()
	if err != nil {
		return opcional.Estadisticas{}, false
	}
	e, ok, err := opcional.ParsearEstadisticas(h)
	return e, ok && err == nil
}

// ------------------------------------------------------------------- volcar

// volcar muestra cada evento como una línea JSON (protojson): para
// inspeccionar a mano lo que pasa por Kafka. Sin consumer group: no confirma
// nada ni le quita eventos a nadie.
func volcar(ctx context.Context, brokers []string, topics eventos.Topics, desdeInicio bool) error {
	inicio := kgo.NewOffset().AtEnd()
	if desdeInicio {
		inicio = kgo.NewOffset().AtStart()
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(topics.Evaluadas, topics.Liquidadas),
		kgo.ConsumeResetOffset(inicio))
	if err != nil {
		return err
	}
	defer cl.Close()
	for {
		f := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		f.EachRecord(func(r *kgo.Record) {
			var msg proto.Message = &pb.TransaccionLiquidada{}
			if r.Topic == topics.Evaluadas {
				msg = &pb.TransaccionEvaluada{}
			}
			if err := proto.Unmarshal(r.Value, msg); err != nil {
				fmt.Fprintf(os.Stderr, "%s: no se pudo decodificar: %v\n", r.Topic, err)
				return
			}
			b, _ := protojson.Marshal(msg)
			fmt.Printf("%s %s\n", r.Topic, b)
		})
	}
}
