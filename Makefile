.PHONY: build run-server run-settlement cargar-stats carga-nominal carga-sobrecarga proto clean

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
