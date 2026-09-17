BINARY      := cernbox
CMD         := ./cmd/cernbox
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

COMPOSE     := docker compose -f dev/docker-compose.yaml

.PHONY: all build install test test-race test-cover test-integration test-all \
        lint vet fmt tidy clean dev-up dev-down dev-logs help

all: build

build: ## Build the cernbox binary
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

install: ## Install the cernbox binary into GOBIN
	go install -ldflags "$(LDFLAGS)" $(CMD)

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests with the race detector
	go test -race ./...

test-cover: ## Run unit tests and write coverage.out / coverage.html
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1

test-integration: build ## Run integration tests (requires: make dev-up)
	go test -tags integration -count=1 -timeout 15m ./integration/...

test-all: dev-up test test-integration ## Run every test suite

lint: vet ## Run go vet and golangci-lint
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || \
		echo "golangci-lint not installed, skipping"

vet: ## Run go vet
	go vet ./...

fmt: ## Format the code
	go fmt ./...

tidy: ## Tidy go.mod
	go mod tidy

clean: ## Remove build and test artifacts
	rm -f $(BINARY) coverage.out coverage.html

dev-up: ## Start the dev environment (revad + EOS)
	$(COMPOSE) up -d --build
	@./dev/wait-for-revad.sh

dev-down: ## Stop the dev environment
	$(COMPOSE) down -v

dev-logs: ## Tail revad logs
	$(COMPOSE) logs -f revad

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
