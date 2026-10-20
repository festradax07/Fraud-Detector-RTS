# ADR-0001: Registrar las decisiones de arquitectura con ADRs

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** transversal

## Contexto

El D-RTS (*Distributed Real-Time Fraud Detection System*) es un motor de
autorización de pagos que decide aprobar o rechazar una transacción bajo un
**deadline duro de 200ms de extremo a extremo**: una respuesta correcta que
llega tarde cuenta como falla. Se construye por etapas, y en cada una se
tomaron decisiones de diseño cuyo porqué no se lee en el código: qué se
registra ante un rechazo, por qué un fraude no es un error, por qué no se
reintenta contra Redis, etc. Sin registro, esas razones se pierden y una
decisión se puede "corregir" rompiendo justo lo que protegía.

## Decisión

Cada decisión de arquitectura se registra como un ADR (*Architecture
Decision Record*) en `docs/adr/`, numerado y con el formato de
[`0000-plantilla.md`](0000-plantilla.md): contexto, decisión, alternativas
y consecuencias. Un ADR aceptado no se edita para cambiar la decisión: se
escribe uno nuevo que lo reemplaza y se marca el viejo como "Reemplazada
por". Los ADRs son públicos y se versionan con el código.

Los ADRs 0002 a 0017 registran decisiones ya implementadas (escritos
después, a partir del registro de cada etapa); los siguientes se escriben al
decidir.

## Alternativas consideradas

- **Solo comentarios en el código:** explican el *cómo* local, pero no las
  alternativas descartadas ni decisiones que cruzan varios paquetes.
- **Un único documento de diseño:** se desactualiza en bloque y no deja ver
  cuándo y por qué cambió cada decisión.

## Consecuencias

- Cambiar una decisión obliga a escribir por qué: se hace visible.
- Hay un costo de escritura por decisión; se acepta porque el proyecto es
  también material de estudio.
