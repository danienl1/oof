MODULE = github.com/appfolio/oof
BINARY = oof

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

.PHONY: build test lint clean install cover

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/$(BINARY) ./cmd/oof

test:
	go test ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/ dist/ cover.out

install:
	go install ./cmd/oof

cover:
	go test -coverprofile=cover.out ./... && go tool cover -html=cover.out -o coverage.html