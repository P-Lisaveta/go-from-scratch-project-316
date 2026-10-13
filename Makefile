BINARY := bin/hexlet-go-crawler

.PHONY: build test run

build:
	@mkdir -p bin
	go build -o $(BINARY) ./cmd/hexlet-go-crawler

test:
	go test ./...

run:
	go run ./cmd/hexlet-go-crawler $(URL)
