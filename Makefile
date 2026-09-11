# dit — build, test and packaging targets.

GO          ?= go
BIN_DIR     ?= bin
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
IMAGE       ?= dit-server
DOCKER      ?= docker

CLI_LDFLAGS := -s -w \
  -X github.com/andriotisnikos1/dit/internal/cli.Version=$(VERSION) \
  -X github.com/andriotisnikos1/dit/internal/cli.Commit=$(COMMIT) \
  -X github.com/andriotisnikos1/dit/internal/cli.Date=$(DATE)
SERVER_LDFLAGS := -s -w -X main.Version=$(VERSION)

# The binaries are static so the server image can be distroless.
export CGO_ENABLED=0

.PHONY: all
all: fmt vet lint test build

.PHONY: build
build: ## Build both binaries into $(BIN_DIR)
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(SERVER_LDFLAGS)" -o $(BIN_DIR)/dit-server ./cmd/dit-server
	$(GO) build -trimpath -ldflags "$(CLI_LDFLAGS)"    -o $(BIN_DIR)/dit        ./cmd/dit
	@echo "built $(BIN_DIR)/dit and $(BIN_DIR)/dit-server ($(VERSION))"

.PHONY: test
test: ## Run the full test suite
	$(GO) test ./... -count=1

.PHONY: test-race
test-race: ## Run the test suite with the race detector (requires cgo)
	CGO_ENABLED=1 $(GO) test ./... -race -cover -count=1

.PHONY: cover
cover: ## Write coverage.out and print the total
	CGO_ENABLED=1 $(GO) test ./... -coverprofile=coverage.out -count=1 >/dev/null
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format the tree
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail when anything is unformatted
	@out=$$(gofmt -l . | grep -v '^$$' || true); \
	if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: fmt-check vet ## Formatting plus vet

.PHONY: tidy
tidy: ## Tidy go.mod/go.sum
	$(GO) mod tidy

.PHONY: run-server
run-server: ## Run dit-server against the local config
	$(GO) run ./cmd/dit-server

.PHONY: docker
docker: ## Build the container image
	$(DOCKER) build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg DATE=$(DATE) \
	  -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN_DIR) coverage.out coverage.html

.PHONY: help
help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
