.PHONY: all format format-check vet staticcheck test test-race build check smoke run-ledger run-payment compose-up compose-down docker-build clean

GO ?= go
BIN_DIR ?= bin

all: check

format:
	$(GO) fmt ./...

format-check:
	@test -z "$$(gofmt -l .)" || { echo "These files need gofmt:"; gofmt -l .; exit 1; }

vet:
	$(GO) vet ./...

staticcheck:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/ledger-service ./cmd/ledger-service
	$(GO) build -trimpath -o $(BIN_DIR)/payment-service ./cmd/payment-service

check: format-check vet test test-race build

smoke:
	./scripts/smoke.sh

run-ledger:
	$(GO) run ./cmd/ledger-service

run-payment:
	$(GO) run ./cmd/payment-service

compose-up:
	docker compose up --build --wait

compose-down:
	docker compose down --remove-orphans

docker-build:
	docker build --target ledger-service -t payment-sim-ledger:local .
	docker build --target payment-service -t payment-sim-payment:local .

clean:
	rm -rf $(BIN_DIR)
