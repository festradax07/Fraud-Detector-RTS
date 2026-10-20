# ADR-0018: Evaluar el sistema real en distintos entornos, no contra un simulador

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** transversal (afecta a 7.5, la etapa de nube y la calibración)

## Contexto

El plan original contrastaba el sistema contra una simulación conceptual en
SimPy. Con el sistema real construido, el simulador como paso previo sobra.
La pregunta más útil es otra: ¿qué tan estables son la latencia y el
cumplimiento del deadline según dónde corre el sistema? Ya hay evidencia de
que importa: en la Etapa 7 la misma build dio P50 de 22ms un día y 37ms otro,
en la misma máquina.

## Decisión

- Se evalúa **el mismo sistema** en distintos entornos: binarios nativos y
  Docker Compose en una máquina, y después en AWS.
- **Mismo artefacto en todos** (la misma imagen), configurado por entorno
  ([ADR-0017](0017-configuracion-por-entorno-y-apagado-ordenado.md)).
- **Mismo protocolo de medición:** misma semilla, dataset y perfil de carga;
  varias corridas por entorno, intercaladas cuando se comparan; se reporta la
  distribución completa, no un número.
- **La latencia de referencia es la del cliente** (load generator): no
  depende de que los relojes de distintos hosts estén sincronizados. La de
  extremo a extremo (`emission_timestamp_ns`) solo vale en una máquina, o
  declarando el error de la sincronización de la plataforma.

## Alternativas consideradas

- **Mantener el simulador:** compararía el sistema contra un modelo, no
  contra la realidad de cada entorno.

## Consecuencias

- La forma del entorno de nube está abierta (ECS, o EC2 con k3s). Restricciones
  para que la medición valga: evitar instancias con créditos de CPU
  (t2/t3/t4g), que limitan el CPU a mitad de una corrida; y tener en cuenta
  que con una sola instancia el entorno vuelve a ser de una máquina.
- Presupuestos y umbrales se recalibran **por entorno**, con datos.
