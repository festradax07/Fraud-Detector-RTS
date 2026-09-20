.PHONY: build run-server proto clean

build:
	go build ./...

run-server:
	go run ./cmd/server

proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/transactions.proto

clean:
	rm -f server settlement loadgen
