package mandatoria

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestEnBlacklist(t *testing.T) {
	v := NuevoVerificador([]string{"tok-robada", "tok-clonada"})

	casos := []struct {
		nombre string
		token  string
		want   bool
	}{
		{"tarjeta bloqueada", "tok-robada", true},
		{"otra tarjeta bloqueada", "tok-clonada", true},
		{"tarjeta limpia", "tok-ok", false},
		{"token vacío", "", false},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := v.EnBlacklist(c.token); got != c.want {
				t.Errorf("EnBlacklist(%q) = %v, se esperaba %v", c.token, got, c.want)
			}
		})
	}
}

// intento es un paso de un escenario: qué tarjeta, cuánto después del
// inicio, y cuántos intentos esperamos ver en la ventana tras registrarlo.
type intento struct {
	token  string
	offset time.Duration
	want   int
}

func TestRegistrarIntento(t *testing.T) {
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
	}

	inicio := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	for _, e := range escenarios {
		t.Run(e.nombre, func(t *testing.T) {
			// Verificador nuevo por escenario: sin estado compartido entre casos.
			v := NuevoVerificador(nil)
			for i, p := range e.pasos {
				got := v.RegistrarIntento(p.token, inicio.Add(p.offset))
				if got != p.want {
					t.Errorf("paso %d (%s en +%v): intentos = %d, se esperaba %d",
						i, p.token, p.offset, got, p.want)
				}
			}
		})
	}
}

// TestRegistrarIntentoConcurrente simula lo que hace el server gRPC: muchas
// requests a la vez sobre el mismo Verificador. Su valor real aparece
// corriéndolo con -race, que detecta accesos concurrentes sin proteger.
//
// Nota: goroutines y sync.WaitGroup son tema de la Etapa 6. Acá se usan lo
// mínimo indispensable para poder generar concurrencia en el test.
func TestRegistrarIntentoConcurrente(t *testing.T) {
	v := NuevoVerificador(nil)
	ahora := time.Now()
	const n = 100

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.RegistrarIntento("tok-a", ahora)
		}()
	}
	wg.Wait()

	// Si ningún intento se perdió por una carrera, el próximo es el n+1.
	if got := v.RegistrarIntento("tok-a", ahora); got != n+1 {
		t.Errorf("intentos = %d, se esperaba %d", got, n+1)
	}
}

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

func TestVelocidadDesdeUltima(t *testing.T) {
	inicio := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	cuartoEcuador := radioTierraKm * math.Pi / 2 // ≈ 10007 km

	casos := []struct {
		nombre     string
		conPrevia  bool // si false, la tarjeta nunca tuvo una compra aprobada
		lat, lon   float64
		dt         time.Duration // tiempo desde la compra previa en (0, 0)
		wantPrevia bool
		wantKmh    float64
		wantFraude bool
	}{
		{"primera compra: nada con qué comparar", false, 0, 90, time.Minute, false, 0, false},
		{"1° en 1h: auto en ruta", true, 1, 0, time.Hour, true, radioTierraKm * math.Pi / 180, false},
		{"10.000 km en 13h: vuelo largo legítimo", true, 0, 90, 13 * time.Hour, true, cuartoEcuador / 13, false},
		{"10.000 km en 1h: imposible", true, 0, 90, time.Hour, true, cuartoEcuador, true},
		{"mismo instante, otro lugar: infinito", true, 0, 90, 0, true, math.Inf(1), true},
		{"mismo instante, mismo lugar: quieto", true, 0, 0, 0, true, 0, false},
		// 0.009° de latitud ≈ 1km: en 2s serían 1800 km/h, pero es ruido.
		{"ruido: ~1km en 2s, mismo shopping", true, 0.009, 0, 2 * time.Second, true, 0, false},
		{"ruido: mismo instante a ~1km", true, 0.009, 0, 0, true, 0, false},
		// 0.5° ≈ 55.6km: ya supera el umbral, se evalúa velocidad.
		{"~55km en 1min: ya no es ruido", true, 0.5, 0, time.Minute, true, radioTierraKm * math.Pi / 360 * 60, true},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			v := NuevoVerificador(nil)
			if c.conPrevia {
				v.ActualizarPosicion("tok-a", 0, 0, inicio)
			}

			kmh, hayPrevia := v.VelocidadDesdeUltima("tok-a", c.lat, c.lon, inicio.Add(c.dt))

			if hayPrevia != c.wantPrevia {
				t.Fatalf("hayPrevia = %v, se esperaba %v", hayPrevia, c.wantPrevia)
			}
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

// El ataque de "mover la tarjeta": consultar NO debe modificar la posición.
// Si lo hiciera, el segundo intento desde Moscú daría 0 km/h y pasaría.
func TestConsultarNoMueveLaTarjeta(t *testing.T) {
	inicio := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	v := NuevoVerificador(nil)

	const baLat, baLon = -34.6037, -58.3816
	const moscuLat, moscuLon = 55.7558, 37.6173

	v.ActualizarPosicion("tok-a", baLat, baLon, inicio) // compra aprobada en BA

	// 10:05 intento desde Moscú → rechazado; el handler NO actualiza.
	kmh1, _ := v.VelocidadDesdeUltima("tok-a", moscuLat, moscuLon, inicio.Add(5*time.Minute))
	// 10:07 segundo intento desde Moscú: tiene que seguir comparando con BA.
	kmh2, _ := v.VelocidadDesdeUltima("tok-a", moscuLat, moscuLon, inicio.Add(7*time.Minute))

	if kmh1 <= VelocidadMaxKmh || kmh2 <= VelocidadMaxKmh {
		t.Errorf("ambos intentos deberían ser viaje imposible: %.0f km/h y %.0f km/h", kmh1, kmh2)
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
