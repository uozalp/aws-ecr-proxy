BINARY := aws-ecr-proxy

.PHONY: build test lint fmt vet run clean

build:
	CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o bin/$(BINARY) ./cmd/server

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -l .

vet:
	go vet ./...

run:
	go run ./cmd/server

clean:
	rm -rf bin/
