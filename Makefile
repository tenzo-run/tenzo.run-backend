CONFIG ?= config.yaml
BIN := bin/tenzo
TEST_DATABASE_URL ?= postgres://tenzo:tenzo@localhost:5472/tenzo_test?sslmode=disable

.PHONY: run build build-linux test test-db lint migrate leads tidy up down

run:
	go run ./cmd/tenzo serve -config $(CONFIG)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/tenzo

build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/tenzo

test:
	go test ./...

test-db:
	TENZO_TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -count=1 ./...

lint:
	go vet ./...

migrate:
	go run ./cmd/tenzo migrate up -config $(CONFIG)

leads:
	go run ./cmd/tenzo leads -config $(CONFIG) -limit $(or $(LIMIT),20)

tidy:
	go mod tidy

up:
	docker compose up -d --build

down:
	docker compose down
