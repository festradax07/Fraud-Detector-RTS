package main

// Escenarios de la demo: qué transacciones manda cada botón del celular.
// Son funciones puras (arman las requests, no hacen I/O): se testean sin
// red y sin motor.

import (
	"errors"
	"fmt"
	"regexp"

	pb "fd_rts/proto"
)

// TarjetaRobada es la tarjeta que cargar-stats pone en la blacklist para la
// demo (-blacklist en el docker-compose.yml).
const TarjetaRobada = "demo-robada"

type lugar struct{ lat, lon float64 }

var (
	buenosAires = lugar{-34.6037, -58.3816}
	tokio       = lugar{35.6762, 139.6503}
)

// Montos cerca de la mediana de cada categoría en el dataset: así el
// scoring da bajo y la compra se aprueba. (Medianas: súper ~$106, comida
// ~$29, combustible ~$61.)
var comprasNormales = []struct {
	categoria string
	monto     float64
}{
	{"grocery_pos", 98.50},
	{"food_dining", 27.40},
	{"gas_transport", 58.20},
}

// compra es una transacción del escenario, con su descripción para la
// pantalla del celular.
type compra struct {
	descripcion string
	req         *pb.TransactionRequest
}

// escenarios: los botones de la página, en orden.
var escenarios = []string{"normal", "card_testing", "viaje_imposible", "tarjeta_robada", "monto_anormal"}

var sesionValida = regexp.MustCompile(`^[a-zA-Z0-9]{8,32}$`)

var errEscenario = errors.New("escenario desconocido")

// maxTarjetas: cuántas veces un celular puede pedir una tarjeta nueva.
const maxTarjetas = 1000

// TarjetaDe es el token de la tarjeta número i del celular sesion. Cada
// celular tiene la suya; si el sistema la bloquea por fraude (ADR-0029),
// pide otra y sube i.
func TarjetaDe(sesion string, i int) string {
	return fmt.Sprintf("qr-%s-t%d", sesion, i)
}

// armarEscenario devuelve las transacciones de un escenario. sesion
// identifica al celular, tarjeta es el número de su tarjeta actual; sig da
// el próximo tx_id. n es el número de request: varía la categoría de la
// compra normal.
//
// Todos los escenarios usan la tarjeta del celular, salvo "tarjeta robada"
// (la de otro): un card testing o un viaje imposible la hacen bloquear.
func armarEscenario(escenario, sesion string, tarjeta, n int, sig func() string) ([]compra, error) {
	if tarjeta < 0 || tarjeta >= maxTarjetas {
		return nil, errEscenario
	}
	propia := TarjetaDe(sesion, tarjeta)
	tx := func(card string, l lugar, categoria string, monto float64) *pb.TransactionRequest {
		return &pb.TransactionRequest{
			TxId:             sig(),
			UserId:           "demo-" + sesion,
			CardToken:        card,
			Amount:           monto,
			MerchantCategory: categoria,
			Latitude:         l.lat,
			Longitude:        l.lon,
		}
	}

	switch escenario {
	case "normal":
		// Con la tarjeta del celular. Ojo: más de 3 compras en 10s con la
		// misma tarjeta es card testing de verdad (cuentan todos los
		// intentos), aunque sean "normales".
		c := comprasNormales[n%len(comprasNormales)]
		return []compra{{"Compra normal", tx(propia, buenosAires, c.categoria, c.monto)}}, nil

	case "card_testing":
		// Más de 3 intentos en 10s con la misma tarjeta: la cuarta se
		// rechaza. Montos normales a propósito: con $1 el scoring ya la
		// rechazaría por monto y taparía el efecto.
		var cs []compra
		for i := 1; i <= 4; i++ {
			cs = append(cs, compra{fmt.Sprintf("Intento %d de 4 con tu tarjeta", i), tx(propia, buenosAires, "food_dining", 27.40)})
		}
		return cs, nil

	case "viaje_imposible":
		// Buenos Aires y, enseguida, Tokio: ~18.000 km en milisegundos. La
		// primera se aprueba y guarda la posición; la segunda se rechaza.
		return []compra{
			{"Compra en Buenos Aires", tx(propia, buenosAires, "grocery_pos", 98.50)},
			{"Compra en Tokio, un instante después", tx(propia, tokio, "grocery_pos", 98.50)},
		}, nil

	case "tarjeta_robada":
		return []compra{{"Compra con una tarjeta robada (de otra persona)", tx(TarjetaRobada, buenosAires, "grocery_pos", 98.50)}}, nil

	case "monto_anormal":
		// $25.000 en el súper: 12 desvíos por encima de lo normal.
		return []compra{{"$25.000 en el supermercado", tx(propia, buenosAires, "grocery_pos", 25000)}}, nil

	default:
		return nil, errEscenario
	}
}
