// Package servicio corre un servidor gRPC hasta que lo manden a apagar, y lo
// apaga EN ORDEN (Etapa 7.5).
//
// Por qué: un orquestador (Docker Compose, AWS ECS) detiene un contenedor
// mandándole SIGTERM y, si no termina en un plazo (10s en Compose, 30s por
// defecto en ECS), SIGKILL. SIGKILL no se puede atrapar: el proceso muere en
// el acto y corta las transacciones en vuelo. Con SIGTERM hay tiempo de
// terminar bien.
package servicio

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// NuevaSalud registra en g el servicio ESTÁNDAR de salud de gRPC
// (grpc.health.v1.Health/Check). Lo entienden Docker Compose (vía cmd/salud),
// AWS ECS (también vía cmd/salud) y Kubernetes/k3s de forma nativa (probes
// gRPC), así que el mismo mecanismo sirve en los tres entornos.
//
// Arranca en SERVING: llamarla después de verificar las dependencias (el
// PING a Redis), así "sano" significa "listo para atender".
func NuevaSalud(g *grpc.Server) *health.Server {
	s := health.NewServer()
	healthpb.RegisterHealthServer(g, s)
	return s
}

// Plazos del apagado.
type Plazos struct {
	// Aviso: tiempo entre marcar NOT_SERVING y dejar de aceptar requests.
	// Durante el aviso se sigue atendiendo normal: es la ventana para que un
	// balanceador u orquestador vea el estado y saque la instancia de la
	// rotación. Sin balanceador (local) puede ser 0.
	Aviso time.Duration
	// Espera: máximo para drenar las requests en vuelo.
	Espera time.Duration
}

// Correr sirve g en lis hasta que ctx se cancele (la señal de apagado) o
// Serve falle. Al apagarse:
//
//  0. marca el servicio NOT_SERVING (si salud no es nil) y sigue atendiendo
//     durante plazos.Aviso, para que el orquestador o el balanceador dejen
//     de mandarle tráfico;
//  1. deja de aceptar requests nuevas y espera las que están en vuelo
//     (GracefulStop), como mucho plazos.Espera;
//  2. si en ese plazo no terminaron, les cancela el ctx (Stop) y vuelve SIN
//     esperarlas: quien llama termina el proceso;
//  3. apaga el servidor de métricas (si hay).
//
// Trampa del paso 2 (encontrada en el test): Stop también ESPERA a que los
// handlers retornen. Les cancela el ctx y cierra las conexiones, pero no los
// puede matar: en Go no hay forma de matar una goroutine desde afuera, la
// cancelación es cooperativa (vía ctx). Un handler que no mira el ctx (un
// time.Sleep, un loop de CPU) tiene a Stop esperándolo. Por eso Stop va en
// segundo plano: cuando main retorna, el proceso termina, y ESO sí se lleva
// puestas todas las goroutines.
//
// Quien llama cierra DESPUÉS sus clientes (Redis, Settlement): las requests
// en vuelo del paso 1 todavía los necesitan. Por eso esto no los recibe.
//
// Trampa (encontrada en el test): sin aviso, el NOT_SERVING no lo ve nadie.
// GracefulStop les manda GOAWAY a las conexiones existentes, así que un
// chequeo de salud durante el drenaje ve "conexión rechazada", no
// NOT_SERVING; y el tráfico que todavía viene en camino recibe Unavailable.
func Correr(ctx context.Context, g *grpc.Server, lis net.Listener, salud *health.Server, metricas *http.Server, plazos Plazos) error {
	espera := plazos.Espera
	errServe := make(chan error, 1)
	go func() { errServe <- g.Serve(lis) }()

	select {
	case err := <-errServe:
		// Serve terminó solo (sin que nadie llamara a Stop): es un error.
		return err
	case <-ctx.Done():
	}

	if salud != nil {
		salud.Shutdown() // NOT_SERVING para todos los servicios, y ya no cambia
		if plazos.Aviso > 0 {
			log.Printf("apagando: NOT_SERVING, se sigue atendiendo %v para que el balanceador lo vea", plazos.Aviso)
			time.Sleep(plazos.Aviso)
		}
	}
	log.Printf("apagando: no se aceptan requests nuevas, esperando las que están en vuelo (máximo %v)", espera)
	terminado := make(chan struct{})
	go func() {
		g.GracefulStop() // bloquea hasta que terminen las requests en vuelo
		close(terminado)
	}()
	var errApagado error
	select {
	case <-terminado:
		log.Printf("apagando: todas las requests en vuelo terminaron")
	case <-time.After(espera):
		go g.Stop() // cancela el ctx de las que quedan; no esperarlo (ver arriba)
		errApagado = errors.New("apagado forzado: quedaban requests en vuelo")
		log.Printf("apagando: %v", errApagado)
	}

	if metricas != nil {
		ctxM, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := metricas.Shutdown(ctxM); err != nil {
			log.Printf("apagando métricas: %v", err)
		}
	}
	return errApagado
}
