.PHONY: all build test clean deps deps-go deps-check deps-install dev fmt lint coverage build-all build-unix install help \
	operator-build operator-run operator-docker-build operator-docker-push \
	operator-test operator-deploy operator-undeploy operator-install operator-uninstall \
	operator-manifests operator-generate operator-coverage \
	registry-deploy

# Variables
BINARY_NAME ?= mcp-runtime
BUILD_DIR ?= bin
GOCACHE ?= $(CURDIR)/.gocache
VERSION ?= $(shell git describe --tags --match 'v*' --dirty --always 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)
export GOCACHE


##@ General

help: ## Display this help message.
	@echo "Usage: make [target]"
	@echo ""
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*##"; printf ""} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build

all: build ## Default target: build CLI

build: ## Build CLI binary for current platform.
	@echo "Building $(BINARY_NAME) CLI..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/mcp-runtime

build-all: ## Build CLI for all Unix platforms (macOS and Linux, ARM64 and AMD64).
	@echo "Building for all Unix platforms..."
	@mkdir -p $(BUILD_DIR)
	@echo "Building macOS ARM64..."
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/mcp-runtime
	@echo "Building macOS AMD64..."
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/mcp-runtime
	@echo "Building Linux ARM64..."
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/mcp-runtime
	@echo "Building Linux AMD64..."
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/mcp-runtime
	@echo "Build complete. Binaries in $(BUILD_DIR)/"

build-unix: build-all ## Alias for build-all to keep CI targets stable.

##@ Development

dev: build ## Build and run CLI in development mode.
	./$(BUILD_DIR)/$(BINARY_NAME)

deps: deps-check deps-go ## Verify host tools, then download Go modules (Go version from go.mod, docker, kubectl).

deps-go: ## Download Go dependencies for root, service, example and plugin modules.
	@./hack/dev/deps.sh go

deps-check: ## Verify toolchain on PATH. Set STRICT_DEPS_CHECK=1 to fail the recipe if a tool is missing.
	@./hack/dev/deps.sh check

deps-install: ## Best-effort install of host tools where supported (Go, Docker client, kubectl).
	@./hack/dev/deps.sh install

##@ Testing

test: ## Run all tests.
	hack/ci/go-test.sh . -v ./...

coverage: ## Generate code coverage report.
	hack/ci/go-test.sh . -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

##@ Code Quality

fmt: ## Format Go code using go fmt.
	go fmt ./...

lint: ## Lint code using golangci-lint (requires golangci-lint to be installed).
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found. Please install from https://github.com/golangci/golangci-lint#install"; \
		exit 1; \
	fi

vet: ## Run go vet.
	go vet ./...

##@ Cleanup

clean: ## Remove build artifacts and clean Go cache.
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	go clean

##@ Installation

install: build ## Install CLI binary to /usr/local/bin (requires sudo).
	@echo "Installing $(BINARY_NAME) to /usr/local/bin..."
	@sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/
	@echo "Installation complete. Run '$(BINARY_NAME)' to use."

##@ Operator

operator-build: ## Build operator binary.
	@$(MAKE) -f Makefile.operator build

operator-run: ## Run operator against the configured Kubernetes cluster.
	@$(MAKE) -f Makefile.operator run

operator-docker-build: ## Build Docker image with the operator.
	@$(MAKE) -f Makefile.operator docker-build

operator-docker-push: ## Push Docker image to registry.
	@$(MAKE) -f Makefile.operator docker-push

operator-test: ## Run operator tests.
	@$(MAKE) -f Makefile.operator test

operator-coverage: ## Generate operator code coverage report.
	@$(MAKE) -f Makefile.operator coverage

operator-deploy: ## Deploy operator to Kubernetes cluster.
	@$(MAKE) -f Makefile.operator deploy

operator-undeploy: ## Undeploy operator from Kubernetes cluster.
	@$(MAKE) -f Makefile.operator undeploy

operator-install: ## Install operator CRDs into Kubernetes cluster.
	@$(MAKE) -f Makefile.operator install

operator-uninstall: ## Uninstall operator CRDs from Kubernetes cluster.
	@$(MAKE) -f Makefile.operator uninstall

operator-manifests: ## Generate operator manifests (CRDs, RBAC).
	@$(MAKE) -f Makefile.operator manifests

operator-generate: ## Generate operator code (DeepCopy methods).
	@$(MAKE) -f Makefile.operator generate

##@ Registry

registry-deploy: ## Deploy container registry to Kubernetes cluster.
	kubectl apply -k config/registry/
