.PHONY: build run-server run-settlement cargar-stats carga-nominal carga-sobrecarga obs-up obs-down kafka-up kafka-down volcar sistema-up sistema-down imagenes carga-compose limpiar-carga proto clean

build:
	go build ./...

run-server:
	go run ./cmd/server

run-settlement:
	go run ./cmd/settlement

# Load-generator (ADR-0014). Requieren Redis, Settlement y el Fraud-Engine
# corriendo, y las estadísticas cargadas.
carga-nominal:
	go run ./cmd/loadgen -modo nominal

carga-sobrecarga:
	go run ./cmd/loadgen -modo sobrecarga

# Observabilidad (ADR-0015): Prometheus + Grafana provisionados.
# Grafana en http://localhost:3000 (sin login), Prometheus en :9090.
obs-up:
	docker compose up -d

obs-down:
	docker compose down

# Kafka solo (con sus topics), para el entorno nativo y los tests: queda en
# localhost:9092. Los servicios nativos lo usan con KAFKA_BROKERS=localhost:9092.
kafka-up:
	docker compose --profile kafka up -d --wait kafka
	docker compose --profile kafka up kafka-topics

kafka-down:
	docker compose --profile kafka down

# Muestra en vivo los eventos que pasan por Kafka, como JSON (Ctrl+C corta).
# Agregar ARGS=-desde-inicio para ver desde el principio.
volcar:
	go run ./cmd/eventos -rol volcar -metricas "" $(ARGS)

# Sistema completo en Docker Compose (ADR-0020): Redis, carga de
# estadísticas, Settlement y Fraud-Engine, más la observabilidad.
sistema-up:
	docker compose --profile sistema up -d --build

# Baja todo (sistema, carga y observabilidad).
sistema-down:
	docker compose --profile sistema --profile carga --profile kafka down

# Construye las imágenes sin levantar nada.
imagenes:
	docker compose --profile sistema --profile carga build

# Carga dentro del Compose. Los dos perfiles: el loadgen depende del engine.
# --use-aliases: sin esto, el contenedor de "run" no tiene el nombre
# "loadgen" en la red y Prometheus no lo encuentra.
# Ejemplo: make carga-compose ARGS="-rps 700 -duracion 30s"
ARGS ?= -modo nominal
carga-compose:
	docker compose --profile sistema --profile carga run --rm --use-aliases loadgen $(ARGS)

# Borra de Redis lo que dejan las corridas de carga (registros de Settlement
# y posiciones), conservando estadísticas y blacklist. Con cientos de miles de
# claves, todo lo que hace SCAN (por ejemplo, la limpieza de los tests) se
# vuelve lento.
limpiar-carga:
	docker exec redis-drts sh -c "redis-cli --scan --pattern 'drts:settle:*' --count 10000 | xargs -r -n 1000 redis-cli DEL > /dev/null"
	docker exec redis-drts sh -c "redis-cli --scan --pattern 'drts:pos:*' --count 10000 | xargs -r -n 1000 redis-cli DEL > /dev/null"
	docker exec redis-drts redis-cli DBSIZE

# Estadísticas por categoría para el Z-score (fase opcional). Requiere el
# dataset en data/ y Redis levantado.
cargar-stats:
	go run ./cmd/cargar-stats -csv data/fraudTrain.csv

proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/transactions.proto proto/eventos.proto

clean:
	rm -f server settlement loadgen eventos cargar-stats salud
