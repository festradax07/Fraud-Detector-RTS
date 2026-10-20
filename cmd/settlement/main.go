// Settlement: binario del segundo servicio del hot path. La lógica está en
// internal/settlement; acá solo se conecta a Redis y a la red.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"fd_rts/internal/config"
	"fd_rts/internal/metricas"
	"fd_rts/internal/redisclient"
	"fd_rts/internal/servicio"
	"fd_rts/internal/settlement"
	pb "fd_rts/proto"
)

func main() {
	// Configuración por variables de entorno (Etapa 7.5). Los valores por
	// defecto son los que se usaban fijos en el código hasta la Etapa 7.
	addr := config.Texto("GRPC_ADDR", ":50052")
	metricasAddr := config.Texto("METRICAS_ADDR", ":2113") // "" = no exponer
	redisAddr := config.Texto("REDIS_ADDR", "localhost:6379")
	espera, err := config.Duracion("APAGADO_ESPERA", 5*time.Second)
	if err != nil {
		log.Fatalf("configuración inválida: %v", err)
	}
	aviso, err := config.Duracion("APAGADO_AVISO", 0) // ver cmd/server
	if err != nil {
		log.Fatalf("configuración inválida: %v", err)
	}
	log.Printf("configuración: grpc=%s metricas=%q redis=%s apagado=%v aviso=%v", addr, metricasAddr, redisAddr, espera, aviso)

	// Señal de apagado (SIGTERM del orquestador o Ctrl+C); un segundo
	// Ctrl+C mata en el acto. Ver cmd/server.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	rdb := redisclient.Nuevo(redisAddr)
	defer rdb.Close() // después de servicio.Correr: las requests en vuelo lo necesitan

	// Fail-closed desde el arranque, igual que el Fraud-Engine.
	ctxPing, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err = rdb.Ping(ctxPing).Err()
	cancel()
	if err != nil {
		log.Fatalf("no pude conectar a Redis en %s: %v", redisAddr, err)
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("no pude escuchar en %s: %v", addr, err)
	}
	// Métricas (Etapa 7). Los estados y la latencia de extremo a extremo
	// salen de la respuesta: el handler no se toca.
	reg := metricas.NuevoRegistro()
	rpc := metricas.NuevoRPC(reg, "settlement")
	dominio := metricas.NuevoSettlement(reg)
	var srvMetricas *http.Server
	if metricasAddr != "" {
		srvMetricas = metricas.Servir(metricasAddr, reg)
		log.Printf("métricas en %s/metrics", metricasAddr)
	}

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(rpc.Interceptor(func(resp any) {
		if r, ok := resp.(*pb.SettlementResponse); ok {
			dominio.Registrado(r.Status.String(), time.Duration(r.EndToEndLatencyMs)*time.Millisecond)
		}
	})))
	pb.RegisterSettlementServer(grpcServer, settlement.NuevoServidor(rdb, "drts:"))

	salud := servicio.NuevaSalud(grpcServer) // con Redis ya verificado

	log.Printf("settlement escuchando en %s", addr)
	if err := servicio.Correr(ctx, grpcServer, lis, salud, srvMetricas, servicio.Plazos{Aviso: aviso, Espera: espera}); err != nil {
		log.Printf("settlement: %v", err)
	}
	log.Printf("settlement apagado")
}
