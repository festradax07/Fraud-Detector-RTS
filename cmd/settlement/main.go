// Settlement: binario del segundo servicio del camino de decisión. La
// lógica está en internal/settlement; acá solo se conecta a Redis y a la red.
package main

import (
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"

	"fd_rts/internal/config"
	"fd_rts/internal/metricas"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/registro"
	"fd_rts/internal/servicio"
	"fd_rts/internal/settlement"
	pb "fd_rts/proto"
)

func main() {
	if err := registro.Configurar("settlement"); err != nil {
		registro.Fatal("configuración de logs inválida", "error", err)
	}

	// Configuración por variables de entorno (ADR-0017).
	addr := config.Texto("GRPC_ADDR", ":50052")
	metricasAddr := config.Texto("METRICAS_ADDR", ":2113") // "" = no exponer
	redisAddr := config.Texto("REDIS_ADDR", "localhost:6379")
	prefijo := config.Texto("REDIS_PREFIJO", redisclient.PrefijoPorDefecto)
	espera, err := config.Duracion("APAGADO_ESPERA", 5*time.Second)
	if err != nil {
		registro.Fatal("configuración inválida", "error", err)
	}
	aviso, err := config.Duracion("APAGADO_AVISO", 0) // ver cmd/server
	if err != nil {
		registro.Fatal("configuración inválida", "error", err)
	}
	slog.Info("configuración", "grpc", addr, "metricas", metricasAddr, "redis", redisAddr,
		"prefijo", prefijo, "apagado", espera, "aviso", aviso)

	ctx, stop := servicio.ContextoDeApagado()
	defer stop()

	rdb, err := redisclient.Conectar(ctx, redisAddr) // fail-closed desde el arranque
	if err != nil {
		registro.Fatal("no se pudo conectar a Redis", "error", err)
	}
	defer rdb.Close() // después de servicio.Correr: las requests en vuelo lo necesitan

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		registro.Fatal("no se pudo escuchar", "addr", addr, "error", err)
	}

	// Métricas (ADR-0015). Los estados y la latencia de extremo a extremo
	// salen de la respuesta: el handler no se toca.
	reg := metricas.NuevoRegistro()
	rpc := metricas.NuevoRPC(reg, "settlement")
	dominio := metricas.NuevoSettlement(reg)
	srvMetricas := metricas.Servir(metricasAddr, reg)

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(rpc.Interceptor(func(resp any) {
		if r, ok := resp.(*pb.SettlementResponse); ok {
			dominio.Registrado(r.Status.String(), time.Duration(r.EndToEndLatencyMs)*time.Millisecond)
		}
	})))
	pb.RegisterSettlementServer(grpcServer, settlement.NuevoServidor(rdb, prefijo))
	salud := servicio.NuevaSalud(grpcServer) // con Redis ya verificado

	slog.Info("settlement escuchando", "addr", addr)
	if err := servicio.Correr(ctx, grpcServer, lis, salud, srvMetricas, servicio.Plazos{Aviso: aviso, Espera: espera}); err != nil {
		slog.Warn("settlement", "error", err)
	}
	slog.Info("settlement apagado")
}
