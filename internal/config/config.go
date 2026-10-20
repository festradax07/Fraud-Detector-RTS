// Package config lee la configuración de los servicios desde variables de
// entorno (Etapa 7.5).
//
// Por qué variables de entorno: el MISMO binario (la misma imagen) tiene que
// correr en la notebook, en Docker Compose y en AWS ECS. Lo que cambia entre
// entornos (direcciones, puertos, perillas) se inyecta desde afuera; el
// artefacto no se recompila. Es el factor III de "The Twelve-Factor App".
//
// Cada variable tiene un valor por defecto igual al que se usaba fijo en el
// código, así que sin configurar nada todo sigue andando como antes.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Texto devuelve la variable clave, o defecto si no está definida.
//
// os.LookupEnv distingue "no definida" de "definida vacía" (os.Getenv no:
// devuelve "" en los dos casos). Una variable definida vacía se respeta: es
// la forma de apagar algo a propósito (por ejemplo, METRICAS_ADDR="").
func Texto(clave, defecto string) string {
	if v, ok := os.LookupEnv(clave); ok {
		return v
	}
	return defecto
}

// Entero devuelve la variable clave como entero, o defecto si no está.
// Un valor que no es un entero es un error: mejor no arrancar que correr con
// una configuración que no es la que se pidió.
func Entero(clave string, defecto int) (int, error) {
	v, ok := os.LookupEnv(clave)
	if !ok {
		return defecto, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q no es un entero: %w", clave, v, err)
	}
	return n, nil
}

// Duracion devuelve la variable clave como time.Duration ("5s", "200ms"), o
// defecto si no está. Un valor mal escrito es un error.
func Duracion(clave string, defecto time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(clave)
	if !ok {
		return defecto, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q no es una duración (ejemplos: 5s, 200ms): %w", clave, v, err)
	}
	return d, nil
}
