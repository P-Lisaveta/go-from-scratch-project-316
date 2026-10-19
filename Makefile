BINARY := bin/hexlet-go-crawler

.PHONY: setup build test lint run

setup:
	go mod download

build:
	@mkdir -p bin
	go build -o $(BINARY) ./cmd/hexlet-go-crawler

test:
	go test ./...

lint:
	golangci-lint run ./...

run:
	go run ./cmd/hexlet-go-crawler $(URL)
