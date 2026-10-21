# ADR-0012: Settlement como segundo servicio sincrónico, con timeout propio

- **Estado:** Aceptada (la "política ante fallas" la reemplaza [ADR-0027](0027-reintento-responde-lo-registrado.md))
- **Fecha:** 2026-10-04
- **Etapa:** 5

## Contexto

Una decisión no está completa hasta que queda registrada (comprometida o
rechazada). Settlement es un segundo salto de red dentro del mismo deadline,
y puede caerse o ponerse lento.

## Decisión

- **Toda decisión pasa por Settlement**, aprobada o rechazada. Settlement la
  registra en Redis con uno de tres estados: `COMMITTED`, `REJECTED_FRAUD` o
  `ABORTED_TIMEOUT`.
- **`ABORTED_TIMEOUT`:** si la transacción llega a Settlement después del
  deadline global (200ms desde la emisión), no se compromete. En tiempo real
  duro, comprometer algo tardío es dar por buena una respuesta que ya es una
  falla.
- **Timeout propio:** la llamada usa `context.WithTimeout(ctx, 15ms)`, la
  reserva que el slack ya le da a Settlement y la red. `WithTimeout` nunca
  alarga un deadline: si a la request le queda menos, gana ese.
- **Política ante fallas:** aprobada sin commit confirmado → error
  (fail-closed); rechazada → se responde igual el rechazo (ya es la decisión
  segura) y se registra en el log que se perdió el registro.
- **Medición de la red:** Settlement informa su tiempo interno en el trailer
  gRPC `x-settle-us`; el motor calcula `C_net = total − C_settle`. Trailer y
  no header, porque el dato se conoce al terminar y el trailer viaja también
  en los errores. No cambia el contrato `.proto`.

## Alternativas consideradas

- **Sin timeout propio:** un Settlement colgado se llevaría el deadline
  entero del cliente.
- **Error también en los rechazos:** el cliente perdería la información de
  que era fraude.
- **Un campo nuevo en el `.proto` para `C_settle`:** más explícito, pero
  cambia el contrato.

## Consecuencias

- Medido (2000 transacciones, misma máquina): `C_net` P50 0.34ms, P99 0.68ms,
  contra 15ms reservados; aun así 2 de 2000 llamadas superaron los 15ms.
- La latencia de extremo a extremo compara relojes de dos procesos: vale en
  una sola máquina, no entre hosts ([ADR-0018](0018-evaluar-en-distintos-entornos.md)).
- Los registros de Settlement no tienen TTL y crecen sin límite (~1M de
  claves por hora a 300 RPS): se resuelve con
  [ADR-0019](0019-kafka-como-columna-de-eventos.md).
