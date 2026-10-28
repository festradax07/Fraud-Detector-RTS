package main

// Límite de requests por IP (balde de fichas): el endpoint es público
// durante la demo, y cualquiera con la URL podría mandar tráfico.

import (
	"sync"
	"time"
)

type limitador struct {
	mu     sync.Mutex
	porSeg float64 // fichas que se recargan por segundo
	rafaga float64 // máximo de fichas acumuladas
	baldes map[string]*balde
	ahora  func() time.Time // inyectable en los tests
}

type balde struct {
	fichas float64
	ultimo time.Time
}

// maxBaldes acota la memoria si llegan muchas IPs distintas: al pasarlo se
// empieza de cero (en una demo de una clase no debería pasar).
const maxBaldes = 10_000

func nuevoLimitador(porSeg, rafaga float64) *limitador {
	return &limitador{porSeg: porSeg, rafaga: rafaga, baldes: map[string]*balde{}, ahora: time.Now}
}

// permitir descuenta una ficha de la clave (la IP) si hay; si no, rechaza.
func (l *limitador) permitir(clave string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	ahora := l.ahora()
	b, ok := l.baldes[clave]
	if !ok {
		if len(l.baldes) >= maxBaldes {
			l.baldes = map[string]*balde{}
		}
		b = &balde{fichas: l.rafaga, ultimo: ahora}
		l.baldes[clave] = b
	}
	b.fichas = min(l.rafaga, b.fichas+ahora.Sub(b.ultimo).Seconds()*l.porSeg)
	b.ultimo = ahora
	if b.fichas < 1 {
		return false
	}
	b.fichas--
	return true
}
