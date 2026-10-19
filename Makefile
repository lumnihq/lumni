SHELL := /bin/sh

.PHONY: help build console-build console-test console-dev test test-race test-integration lint generate migrate-up migrate-down compose-up compose-up-analytics compose-down demo sdk-test

help:
	@echo "build console-build console-test console-dev test test-race test-integration lint generate migrate-up compose-up compose-up-analytics compose-down demo sdk-test"

build:
	go build ./cmd/...
	$(MAKE) console-build

console-build:
	cd console && bun scripts/build.ts

console-test:
	cd console && bun test && bunx tsc --noEmit

console-dev:
	cd console && bun --hot serve.ts

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	go test -tags=integration ./internal/integration/...

lint:
	gofmt -l . | grep -q . && exit 1 || true
	go vet ./...

generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.29.0 generate

migrate-up:
	go run ./cmd/lumni migrate --direction up

migrate-down:
	go run ./cmd/lumni migrate --direction down

compose-up:
	docker compose up --build -d

compose-up-analytics:
	docker compose --profile analytics up --build -d

compose-down:
	docker compose down

demo:
	./scripts/demo.sh

sdk-test:
	cd sdk/typescript && npm ci && npm test
	cd sdk/python && python3 -m pytest
