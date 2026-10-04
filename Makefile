.PHONY: build run-server cargar-stats proto clean

build:
	go build ./...

run-server:
	go run ./cmd/server

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
