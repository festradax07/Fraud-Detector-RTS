# ADR-0007: Validación estricta de la request

- **Estado:** Aceptada
- **Fecha:** 2026-09-24 a 2026-10-03
- **Etapas:** 2, 3 y 4

## Contexto

Varios campos son necesarios para decidir, y algunos tienen trampas propias
de protobuf o del cálculo.

## Decisión

Se rechaza con `InvalidArgument` (motivo `REQUEST_INVALIDA` o
`CATEGORIA_DESCONOCIDA`), sin tocar el estado:

- **Sin `tx_id`:** es el miembro del ZSET de velocidad y la clave de
  idempotencia; vacío fundiría intentos distintos en uno.
- **Sin `card_token`:** todas las requests sin tarjeta compartirían una
  ventana de velocidad.
- **Coordenadas (0, 0) o fuera de rango:** en proto3 un `double` no enviado
  llega como 0, indistinguible de un 0 real. (0, 0) exacto es océano en el
  Golfo de Guinea: se trata como "no vino". Sin esto, una compra sin
  coordenadas "viajaría" hasta ahí y daría viaje imposible.
- **Monto no positivo o NaN:** el scoring usa `ln(monto)`. Se valida con
  `!(monto > 0)` y no con `monto <= 0`, porque NaN no es mayor ni menor que
  nada.
- **Categoría vacía o desconocida** (sin estadísticas cargadas): sin
  estadísticas no hay cómo puntuar. Se rechaza aunque la request fuera a
  degradarse.

Además, por debajo de **50km** el desplazamiento entre compras se trata como
ruido de geolocalización (precisión de ciudad) y no se evalúa velocidad: dos
compras en el mismo shopping con 2s y 1km de diferencia darían 1800 km/h. Es
un punto de partida de diseño, no un valor medido.

## Alternativas consideradas

- **`optional double` en el `.proto`** para saber si las coordenadas vinieron:
  más prolijo, pero cambia el contrato.
- **Degradar ante categoría desconocida** en vez de rechazar: aprobaría sin
  poder puntuar.

## Consecuencias

- La categoría desconocida se detecta después del primer viaje a Redis, así
  que ese intento ya quedó contado en la ventana de velocidad: contar de más
  es el lado seguro.
