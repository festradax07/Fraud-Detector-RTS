# ADR-0006: Qué se registra en el estado de detección ante un rechazo

- **Estado:** Aceptada
- **Fecha:** 2026-09-24 (revisada al sumar la fase opcional: 2026-10-03)
- **Etapa:** 2, revisada en la 4

## Contexto

La fase mandatoria guarda estado entre transacciones: los intentos recientes
de cada tarjeta (control de velocidad) y su última posición (viaje
imposible). Cuando una transacción se rechaza, hay que decidir si actualiza
ese estado.

## Decisión

- **Velocidad: cuenta todos los intentos**, aprobados y rechazados. El
  intento se registra antes de cualquier chequeo. El *card testing* son
  justamente muchos intentos fallidos: si solo contaran los aprobados, un
  atacante que recibe rechazos nunca sumaría.
- **Posición: solo la de transacciones aprobadas** ("última posición
  *confiable*"). Si se guardara la de un rechazo, el atacante movería la
  tarjeta a su ubicación: compra aprobada en Buenos Aires → intento desde
  Moscú (rechazado, pero se guarda Moscú) → segundo intento desde Moscú:
  0 km/h, **pasa**.
- La posición se guarda **después de la fase opcional** (un rechazo por
  riesgo tampoco la mueve) y **antes de Settlement** (ver
  [ADR-0012](0012-settlement-segundo-servicio-sincronico.md)).
- **Auditoría ≠ estado de detección.** La evidencia de un rechazo va a
  Settlement y, a futuro, a eventos
  ([ADR-0019](0019-kafka-como-columna-de-eventos.md)). El estado de detección
  es volátil a propósito (ventana de 10s, posición que se pisa).

## Alternativas consideradas

- **Registrar todo en todo:** habilita el ataque de "mover la tarjeta".
- **Registrar solo lo aprobado:** el control de velocidad no ve el card
  testing.

## Consecuencias

- Hay tests que fijan ambos ataques (`un rechazo no mueve la tarjeta`, `los
  rechazos suman intentos`).
- Entre consultar la posición y actualizarla hay una ventana (dos requests
  concurrentes de la misma tarjeta leen la misma posición vieja). Se acepta:
  es rara y del lado conservador.
