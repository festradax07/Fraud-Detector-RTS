.PHONY: build run-server run-settlement cargar-stats carga-nominal carga-sobrecarga obs-up obs-down limpiar-carga proto clean

build:
	go build ./...

run-server:
	go run ./cmd/server

run-settlement:
	go run ./cmd/settlement

# Load-generator (Etapa 6). Requieren Redis, Settlement y el Fraud-Engine
# corriendo, y las estadísticas cargadas.
carga-nominal:
	go run ./cmd/loadgen -modo nominal

carga-sobrecarga:
	go run ./cmd/loadgen -modo sobrecarga

# Observabilidad (Etapa 7): Prometheus + Grafana provisionados.
# Grafana en http://localhost:3000 (sin login), Prometheus en :9090.
obs-up:
	docker compose up -d

obs-down:
	docker compose down

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
		proto/transactions.proto

clean:
	rm -f server settlement loadgen
