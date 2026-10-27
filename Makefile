BINARY      := cernbox
WS          := ws
GORELEASER  ?= goreleaser

# The build of cernbox is gaia's: packaging/prepare.sh writes the workspace
# ws/, with the versions of packaging/versions.env, and go or goreleaser
# compile it. For a local reva or reva-plugins, set REVA_DIR or
# REVA_PLUGINS_DIR (and BRAND for another brand file); GAIA names the gaia
# binary. They go to prepare.sh only, not exported: cernbox warns about any
# REVA_* variable it sees, so the tests should not see them.
PREPARE     := GAIA="$(GAIA)" BRAND="$(BRAND)" REVA_DIR="$(REVA_DIR)" REVA_PLUGINS_DIR="$(REVA_PLUGINS_DIR)" \
               REVA_VERSION="$(REVA_VERSION)" REVA_PLUGINS_VERSION="$(REVA_PLUGINS_VERSION)" packaging/prepare.sh

COMPOSE     := docker compose -f dev/docker-compose.yaml

.PHONY: all prepare build install snapshot test test-race test-cover test-integration test-all \
        lint vet fmt tidy clean completions dev-up dev-down dev-logs help

all: build

prepare: ## Prepare the gaia workspace of cernbox (ws/)
	$(PREPARE)

$(WS)/bflags:
	$(PREPARE)

build: $(WS)/bflags ## Build cernbox for this machine from the workspace
	cd $(WS) && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $$(cat bflags)" -o ../$(BINARY) .

install: build ## Install cernbox into GOBIN
	install -m 0755 $(BINARY) "$$(go env GOBIN | grep . || echo "$$(go env GOPATH)/bin")/$(BINARY)"

snapshot: $(WS)/bflags ## Build the release artefacts into dist/, without publishing
	BFLAGS="$$(cat $(WS)/bflags)" $(GORELEASER) release --snapshot --clean --skip=publish

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests with the race detector
	go test -race ./...

test-cover: ## Run unit tests and write coverage.out / coverage.html
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1

test-integration: build ## Run integration tests against ./cernbox (requires: make dev-up)
	CERNBOX_BINARY=$(CURDIR)/$(BINARY) go test -tags integration -count=1 -timeout 20m ./integration/...

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

completions: build ## Generate shell completion scripts for packaging
	packaging/completions.sh ./$(BINARY)

clean: ## Remove build and test artifacts
	rm -f $(BINARY) coverage.out coverage.html
	rm -rf packaging/completions $(WS) dist

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
