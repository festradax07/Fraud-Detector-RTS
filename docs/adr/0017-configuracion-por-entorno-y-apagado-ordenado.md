# ADR-0017: Configuración por entorno y apagado ordenado

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** 7.5

## Contexto

El mismo sistema tiene que correr en distintos entornos
([ADR-0018](0018-evaluar-en-distintos-entornos.md)), y un orquestador detiene
un contenedor con SIGTERM y, si no termina en un plazo, con SIGKILL, que corta
las transacciones en vuelo.

## Decisión

- **Configuración por variables de entorno**
  ([`internal/config`](../../internal/config/config.go)): direcciones,
  puertos, `ITERACIONES_SCORING`, `APAGADO_ESPERA`. Los valores por defecto
  son los de antes. Un valor mal escrito impide arrancar.
- **Apagado ordenado** ([`internal/servicio`](../../internal/servicio/servicio.go)),
  ante SIGTERM o Ctrl+C: dejar de aceptar requests, esperar las que están en
  vuelo (como mucho `APAGADO_ESPERA`, 5s) y **recién después** cerrar Redis y
  Settlement, que las requests en vuelo todavía necesitan.

## Alternativas consideradas

- **Valores fijos en el código:** obligan a recompilar por entorno, y el
  artefacto medido deja de ser el mismo.
- **Esperar indefinidamente al apagar:** el orquestador mata el proceso igual.

## Consecuencias

- Probado con señales reales bajo 300 RPS: con SIGTERM, las transacciones en
  vuelo terminaron con su latencia normal; con SIGKILL se cortaron todas en el
  mismo instante.
- En Go no se puede matar una goroutine desde afuera: un handler que no mira
  el `ctx` retrasa incluso a `grpc.Server.Stop()`. Al vencer el plazo, el
  apagado devuelve el control y el proceso termina.
