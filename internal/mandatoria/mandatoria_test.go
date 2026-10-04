package mandatoria

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fd_rts/internal/redisclient"
)

// ---------------------------------------------------------------------------
// Helpers: Redis real
// ---------------------------------------------------------------------------

const redisAddrTest = "localhost:6379"

// verificadorDePrueba conecta al Redis REAL y usa un prefijo de claves único,
// para que el test no pise datos del server ni de otros tests. Al terminar,
// t.Cleanup borra todas las claves del prefijo.
//
// Si Redis no está levantado, el test FALLA (no se saltea): un test salteado
// se ve como "ok" y esconde que falta la infraestructura.
func verificadorDePrueba(t *testing.T) *Verificador {
	t.Helper()
	rdb := redisclient.Nuevo(redisAddrTest)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		t.Fatalf("Redis no disponible en %s (levantalo con: docker start redis-drts): %v", redisAddrTest, err)
	}

	prefijo := fmt.Sprintf("test:%d:%d:", os.Getpid(), time.Now().UnixNano())

	// t.Cleanup corre al terminar el test, pase o falle (parecido a defer,
	// pero registrado desde un helper).
	t.Cleanup(func() {
		ctx := context.Background()
		iter := rdb.Scan(ctx, 0, prefijo+"*", 100).Iterator()
		for iter.Next(ctx) {
			rdb.Del(ctx, iter.Val())
		}
		rdb.Close()
	})

	return NuevoVerificador(rdb, prefijo)
}

// txIDs únicos para cada intento: el tx_id es el miembro del ZSET.
var contadorTx atomic.Int64

func nuevoTxID() string {
	return fmt.Sprintf("tx-%d", contadorTx.Add(1))
}

// consultar es Consultar con un ctx de 200ms y fallo inmediato si hay error.
func consultar(t *testing.T, v *Verificador, card string, lat, lon float64, ahora time.Time) Resultado {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res, err := v.Consultar(ctx, nuevoTxID(), card, "test_cat", lat, lon, ahora)
	if err != nil {
		t.Fatalf("Consultar: error inesperado: %v", err)
	}
	return res
}

var inicio = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

const (
	baLat, baLon       = -34.6037, -58.3816
	moscuLat, moscuLon = 55.7558, 37.6173
)

// ---------------------------------------------------------------------------
// Contra Redis real
// ---------------------------------------------------------------------------

func TestBlacklist(t *testing.T) {
	v := verificadorDePrueba(t)
	ctx := context.Background()
	for _, card := range []string{"tok-robada", "tok-clonada"} {
		if err := v.Bloquear(ctx, card); err != nil {
			t.Fatalf("Bloquear(%q): %v", card, err)
		}
	}

	casos := []struct {
		nombre string
		token  string
		want   bool
	}{
		{"tarjeta bloqueada", "tok-robada", true},
		{"otra tarjeta bloqueada", "tok-clonada", true},
		{"tarjeta limpia", "tok-ok", false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			res := consultar(t, v, c.token, baLat, baLon, inicio)
			if res.EnBlacklist != c.want {
				t.Errorf("EnBlacklist = %v, se esperaba %v", res.EnBlacklist, c.want)
			}
		})
	}
}

// intento es un paso de un escenario: qué tarjeta, cuánto después del
// inicio, y cuántos intentos esperamos ver en la ventana tras registrarlo.
type intento struct {
	token  string
	offset time.Duration
	want   int64
}

func TestVentanaDeVelocidad(t *testing.T) {
	escenarios := []struct {
		nombre string
		pasos  []intento
	}{
		{"card testing: el 4.º intento en 10s supera el máximo", []intento{
			{"tok-a", 0, 1},
			{"tok-a", 1 * time.Second, 2},
			{"tok-a", 2 * time.Second, 3},
			{"tok-a", 3 * time.Second, 4}, // > MaxIntentos → fraude
		}},
		{"la ventana se desliza y olvida lo viejo", []intento{
			{"tok-a", 0, 1},
			{"tok-a", 1 * time.Second, 2},
			{"tok-a", 2 * time.Second, 3},
			{"tok-a", 11500 * time.Millisecond, 2}, // quedan 2s y 11.5s
		}},
		{"borde: un intento de exactamente 10s ya no cuenta", []intento{
			{"tok-a", 0, 1},
			{"tok-a", 10 * time.Second, 1},
		}},
		{"borde: 1ms antes de los 10s todavía cuenta", []intento{
			{"tok-a", 0, 1},
			{"tok-a", 10*time.Second - time.Millisecond, 2},
		}},
		{"cada tarjeta tiene su propia ventana", []intento{
			{"tok-a", 0, 1},
			{"tok-a", 1 * time.Second, 2},
			{"tok-b", 2 * time.Second, 1},
			{"tok-a", 3 * time.Second, 3},
		}},
		// Antes (Etapa 2) se guardaban timestamps en un slice: dos intentos
		// en el mismo instante contaban 2. Con el ZSET también, porque el
		// miembro es el tx_id y no el timestamp.
		{"dos intentos en el mismo ms cuentan 2", []intento{
			{"tok-a", 0, 1},
			{"tok-a", 0, 2},
		}},
	}

	for _, e := range escenarios {
		t.Run(e.nombre, func(t *testing.T) {
			v := verificadorDePrueba(t) // prefijo nuevo: sin estado compartido
			for i, p := range e.pasos {
				res := consultar(t, v, p.token, baLat, baLon, inicio.Add(p.offset))
				if res.Intentos != p.want {
					t.Errorf("paso %d (%s en +%v): intentos = %d, se esperaba %d",
						i, p.token, p.offset, res.Intentos, p.want)
				}
			}
		})
	}
}

// Si el cliente reintenta la MISMA transacción (mismo tx_id), no cuenta como
// un intento nuevo: ZADD de un miembro existente solo actualiza su score.
func TestTxIDRepetidoNoSumaIntentos(t *testing.T) {
	v := verificadorDePrueba(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		res, err := v.Consultar(ctx, "tx-reintentada", "tok-a", "test_cat", baLat, baLon, inicio.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("Consultar: %v", err)
		}
		if res.Intentos != 1 {
			t.Errorf("reintento %d: intentos = %d, se esperaba 1", i, res.Intentos)
		}
	}
}

// En la Etapa 2 este test probaba el mutex. Ahora prueba que Redis ordena
// la concurrencia: 100 requests simultáneas, ningún intento perdido.
//
// Nota: goroutines y sync.WaitGroup son tema de la Etapa 6.
func TestVelocidadConcurrente(t *testing.T) {
	v := verificadorDePrueba(t)
	const n = 100

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// t.Errorf es seguro desde varias goroutines; t.Fatalf no.
			if _, err := v.Consultar(context.Background(), nuevoTxID(), "tok-a", "test_cat", baLat, baLon, inicio); err != nil {
				t.Errorf("Consultar: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := consultar(t, v, "tok-a", baLat, baLon, inicio).Intentos; got != n+1 {
		t.Errorf("intentos = %d, se esperaba %d", got, n+1)
	}
}

func TestPosicion(t *testing.T) {
	ctx := context.Background()

	t.Run("primera compra: nada con qué comparar", func(t *testing.T) {
		v := verificadorDePrueba(t)
		if res := consultar(t, v, "tok-a", baLat, baLon, inicio); res.HayPrevia {
			t.Errorf("HayPrevia = true, se esperaba false")
		}
	})

	t.Run("con previa: calcula la velocidad", func(t *testing.T) {
		v := verificadorDePrueba(t)
		if err := v.ActualizarPosicion(ctx, "tok-a", baLat, baLon, inicio); err != nil {
			t.Fatalf("ActualizarPosicion: %v", err)
		}
		res := consultar(t, v, "tok-a", moscuLat, moscuLon, inicio.Add(5*time.Minute))
		if !res.HayPrevia || res.Kmh <= VelocidadMaxKmh {
			t.Errorf("HayPrevia=%v Kmh=%.0f, se esperaba viaje imposible", res.HayPrevia, res.Kmh)
		}
	})

	// El ataque de "mover la tarjeta": consultar NO modifica la posición.
	t.Run("consultar no mueve la tarjeta", func(t *testing.T) {
		v := verificadorDePrueba(t)
		if err := v.ActualizarPosicion(ctx, "tok-a", baLat, baLon, inicio); err != nil {
			t.Fatalf("ActualizarPosicion: %v", err)
		}
		r1 := consultar(t, v, "tok-a", moscuLat, moscuLon, inicio.Add(5*time.Minute))
		r2 := consultar(t, v, "tok-a", moscuLat, moscuLon, inicio.Add(7*time.Minute))
		if r1.Kmh <= VelocidadMaxKmh || r2.Kmh <= VelocidadMaxKmh {
			t.Errorf("ambos intentos deberían ser viaje imposible: %.0f km/h y %.0f km/h", r1.Kmh, r2.Kmh)
		}
	})

	t.Run("la posición expira sola (TTL)", func(t *testing.T) {
		v := verificadorDePrueba(t)
		if err := v.ActualizarPosicion(ctx, "tok-a", baLat, baLon, inicio); err != nil {
			t.Fatalf("ActualizarPosicion: %v", err)
		}
		ttl, err := v.rdb.TTL(ctx, v.clavePosicion("tok-a")).Result()
		if err != nil {
			t.Fatalf("TTL: %v", err)
		}
		if ttl <= 25*time.Hour || ttl > 26*time.Hour {
			t.Errorf("TTL = %v, se esperaba (25h, 26h]", ttl)
		}
	})

	t.Run("posición corrupta en Redis: error, no se ignora", func(t *testing.T) {
		v := verificadorDePrueba(t)
		v.rdb.Set(ctx, v.clavePosicion("tok-a"), "basura", 0)
		_, err := v.Consultar(ctx, nuevoTxID(), "tok-a", "test_cat", baLat, baLon, inicio)
		if err == nil {
			t.Errorf("se esperaba error con una posición corrupta")
		}
	})
}

// Las estadísticas de la categoría viajan en el mismo pipeline.
func TestEstadisticasEnElViaje1(t *testing.T) {
	v := verificadorDePrueba(t)
	ctx := context.Background()

	if res := consultar(t, v, "tok-a", baLat, baLon, inicio); len(res.StatsCategoria) != 0 {
		t.Errorf("categoría sin cargar: StatsCategoria = %v, se esperaba vacío", res.StatsCategoria)
	}

	if err := v.GuardarEstadisticas(ctx, "test_cat", map[string]any{"media_log": 4.6, "desvio_log": 0.45, "peso": 2.44}); err != nil {
		t.Fatalf("GuardarEstadisticas: %v", err)
	}
	res := consultar(t, v, "tok-a", baLat, baLon, inicio)
	if res.StatsCategoria["media_log"] != "4.6" || res.StatsCategoria["peso"] != "2.44" {
		t.Errorf("StatsCategoria = %v", res.StatsCategoria)
	}
}

// ---------------------------------------------------------------------------
// Fallas: fail-closed
// ---------------------------------------------------------------------------

func TestRedisCaido(t *testing.T) {
	// Puerto donde no hay nadie escuchando: simula Redis caído.
	rdb := redisclient.Nuevo("localhost:1")
	defer rdb.Close()
	v := NuevoVerificador(rdb, "test:")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	_, err := v.Consultar(ctx, nuevoTxID(), "tok-a", "test_cat", baLat, baLon, inicio)
	tardo := time.Since(t0)
	if err == nil {
		t.Errorf("se esperaba error con Redis caído: sin verificar no se aprueba")
	}
	// Sin reintentos tiene que fallar rápido, no al vencer el deadline.
	if errors.Is(err, context.DeadlineExceeded) || tardo > 20*time.Millisecond {
		t.Errorf("tardó %v (err=%v): se esperaba fallo rápido sin quemar el deadline", tardo, err)
	}
}

// El deadline de la request tiene que llegar hasta Redis
// (ContextTimeoutEnabled): con el ctx ya vencido, Consultar falla con
// context.DeadlineExceeded en vez de ir a Redis igual.
func TestDeadlineLlegaARedis(t *testing.T) {
	v := verificadorDePrueba(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // asegurar que ya venció

	_, err := v.Consultar(ctx, nuevoTxID(), "tok-a", "test_cat", baLat, baLon, inicio)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, se esperaba context.DeadlineExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// Funciones puras: sin Redis
// ---------------------------------------------------------------------------

// Los valores esperados salen de la geometría de la esfera, no de un mapa:
// un arco de θ grados sobre un círculo máximo mide R·θ·π/180.
func TestDistanciaKm(t *testing.T) {
	const R = radioTierraKm
	casos := []struct {
		nombre                 string
		lat1, lon1, lat2, lon2 float64
		want                   float64
	}{
		{"mismo punto", -34.6, -58.4, -34.6, -58.4, 0},
		{"1° sobre un meridiano", 0, 0, 1, 0, R * math.Pi / 180},
		{"un cuarto del ecuador", 0, 0, 0, 90, R * math.Pi / 2},
		{"polo a ecuador", 90, 0, 0, 0, R * math.Pi / 2},
		{"antípodas", 0, 0, 0, 180, R * math.Pi},
		// Cruza el antimeridiano: son 2°, no 358°.
		{"cruzando el antimeridiano", 0, 179, 0, -179, R * 2 * math.Pi / 180},
	}

	// Comparar float64 con == es frágil por redondeo: se usa una tolerancia.
	const tolerancia = 1e-6 // km, o sea 1mm

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			ida := DistanciaKm(c.lat1, c.lon1, c.lat2, c.lon2)
			vuelta := DistanciaKm(c.lat2, c.lon2, c.lat1, c.lon1)
			if math.Abs(ida-c.want) > tolerancia {
				t.Errorf("distancia = %.6f km, se esperaba %.6f km", ida, c.want)
			}
			if math.Abs(ida-vuelta) > tolerancia {
				t.Errorf("no es simétrica: ida %.6f km, vuelta %.6f km", ida, vuelta)
			}
		})
	}
}

func TestVelocidadKmh(t *testing.T) {
	cuartoEcuador := radioTierraKm * math.Pi / 2 // ≈ 10007 km
	previa := Posicion{Lat: 0, Lon: 0, Momento: inicio}

	casos := []struct {
		nombre     string
		lat, lon   float64
		dt         time.Duration // tiempo desde la compra previa en (0, 0)
		wantKmh    float64
		wantFraude bool
	}{
		{"1° en 1h: auto en ruta", 1, 0, time.Hour, radioTierraKm * math.Pi / 180, false},
		{"10.000 km en 13h: vuelo largo legítimo", 0, 90, 13 * time.Hour, cuartoEcuador / 13, false},
		{"10.000 km en 1h: imposible", 0, 90, time.Hour, cuartoEcuador, true},
		{"mismo instante, otro lugar: infinito", 0, 90, 0, math.Inf(1), true},
		{"mismo instante, mismo lugar: quieto", 0, 0, 0, 0, false},
		// 0.009° de latitud ≈ 1km: en 2s serían 1800 km/h, pero es ruido.
		{"ruido: ~1km en 2s, mismo shopping", 0.009, 0, 2 * time.Second, 0, false},
		{"ruido: mismo instante a ~1km", 0.009, 0, 0, 0, false},
		// 0.5° ≈ 55.6km: ya supera el umbral, se evalúa velocidad.
		{"~55km en 1min: ya no es ruido", 0.5, 0, time.Minute, radioTierraKm * math.Pi / 360 * 60, true},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			kmh := VelocidadKmh(previa, c.lat, c.lon, inicio.Add(c.dt))

			// Inf - Inf da NaN, así que el infinito se compara aparte.
			if math.IsInf(c.wantKmh, 1) {
				if !math.IsInf(kmh, 1) {
					t.Errorf("kmh = %v, se esperaba +Inf", kmh)
				}
			} else if math.Abs(kmh-c.wantKmh) > 1e-6 {
				t.Errorf("kmh = %.3f, se esperaba %.3f", kmh, c.wantKmh)
			}
			if fraude := kmh > VelocidadMaxKmh; fraude != c.wantFraude {
				t.Errorf("fraude = %v (%.1f km/h), se esperaba %v", fraude, kmh, c.wantFraude)
			}
		})
	}
}

// Ida y vuelta por el formato de Redis: tiene que dar EXACTAMENTE lo mismo
// (acá sí se compara float con ==: FormatFloat con precisión -1 garantiza
// que el parseo devuelve el mismo float64, bit a bit).
func TestCodificarPosicion(t *testing.T) {
	original := Posicion{Lat: baLat, Lon: baLon, Momento: inicio.Add(123 * time.Millisecond)}
	vuelta, err := decodificarPosicion(codificarPosicion(original))
	if err != nil {
		t.Fatalf("decodificar: %v", err)
	}
	if vuelta.Lat != original.Lat || vuelta.Lon != original.Lon || !vuelta.Momento.Equal(original.Momento) {
		t.Errorf("ida y vuelta: %+v, se esperaba %+v", vuelta, original)
	}

	for _, malo := range []string{"", "1,2", "a,2,3", "1,b,3", "1,2,c", "1,2,3,4"} {
		if _, err := decodificarPosicion(malo); err == nil {
			t.Errorf("decodificarPosicion(%q): se esperaba error", malo)
		}
	}
}

func TestCoordenadasValidas(t *testing.T) {
	casos := []struct {
		nombre   string
		lat, lon float64
		want     bool
	}{
		{"Buenos Aires", -34.6037, -58.3816, true},
		{"(0, 0): el cliente no mandó coordenadas", 0, 0, false},
		{"sobre el ecuador, lon distinta de 0", 0, 10, true},
		{"sobre Greenwich, lat distinta de 0", 51.48, 0, true},
		{"bordes válidos", -90, 180, true},
		{"latitud fuera de rango", 91, 0, false},
		{"longitud fuera de rango", 10, -181, false},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := CoordenadasValidas(c.lat, c.lon); got != c.want {
				t.Errorf("CoordenadasValidas(%v, %v) = %v, se esperaba %v", c.lat, c.lon, got, c.want)
			}
		})
	}
}
