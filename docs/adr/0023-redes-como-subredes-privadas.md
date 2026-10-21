# ADR-0023: Redes del Compose como subredes privadas; Kafka como unidad aparte

- **Estado:** Aceptada
- **Fecha:** 2026-10-19
- **Etapa:** 8 (Kafka)

## Contexto

En la nube los componentes van a vivir aislados: servicios en un clúster
(EKS/ECS) o en instancias propias, Kafka en su instancia, Redis en otra,
repartidos en zonas y con balanceadores en el medio. Si en Compose todo
comparte una sola red, algo que dependa de "estar en la misma red" pasa
desapercibido hasta la nube.

## Decisión

- **Tres redes en el Compose, como subredes privadas:** `datos` (Redis,
  Kafka), `aplicacion` (servicios y generador de carga) y `observabilidad`
  (Prometheus, Grafana).
- Docker aísla por defecto: dos contenedores sin una red en común no se ven.
  **Conectar un servicio a dos redes es el equivalente de una regla de
  security group** que permite ese tráfico. Los flujos permitidos están en
  una matriz al principio de [`docker-compose.yml`](../../docker-compose.yml)
  (origen, destino, puerto, para qué), pensada para traducirse 1 a 1 a
  security groups.
- **Kafka es una unidad aparte**, en `datos`, a la que los servicios llegan
  por dirección (`KAFKA_BROKERS`). Un nodo KRaft combinado (broker +
  controller; KRaft es cómo Kafka coordina su clúster sin ZooKeeper). En
  producción serían 3 controllers y 3 o más brokers repartidos en zonas.
- **Dos listeners de clientes:** `kafka:29092` dentro del Compose y
  `localhost:9092` desde el host. El broker le dice a cada cliente a qué
  dirección conectarse después del primer contacto (*advertised listeners*):
  con uno solo, alguno de los dos lados no llega. En la nube pasa lo mismo:
  la dirección anunciada tiene que ser alcanzable desde las otras instancias.
- Topics creados a propósito por un contenedor que corre una vez
  (`kafka-topics`), con la creación automática apagada (un typo en un nombre
  crearía un topic nuevo en silencio).
- El motor **no depende de Kafka para arrancar** (ADR-0019).

## Alternativas consideradas

- **Una sola red:** más simple, pero esconde dependencias de red que en la
  nube no existen.
- **Una red por servicio:** más fiel a un security group por servicio, pero
  la matriz se vuelve difícil de leer para el tamaño del sistema.

## Consecuencias

- Verificado con contenedores de prueba conectados a una sola red: desde
  `aplicacion` no se llega a Redis ni a Kafka, desde `observabilidad` no se
  llega a datos ni a la aplicación, y desde `datos` no se llega a Prometheus;
  los flujos permitidos sí.
- Diferencia con AWS a tener presente: en una VPC las subredes se enrutan
  entre sí por defecto y restringen los security groups; en Docker es al
  revés (aislado y se agrega). Docker se parece más a "negar todo y permitir
  lo necesario".
- Kafka no tiene volumen en Compose: los topics se recrean al levantarlo.
