.PHONY: help build test lint clean install run fmt vet

# Default target
help:
	@echo "Available targets:"
	@echo "  build      - Build the aiman binary"
	@echo "  test       - Run tests with coverage"
	@echo "  lint       - Run golangci-lint"
	@echo "  fmt        - Format code with gofmt"
	@echo "  vet        - Run go vet"
	@echo "  clean      - Remove build artifacts"
	@echo "  install    - Install aiman to GOPATH/bin"
	@echo "  run        - Build and run aiman"
	@echo "  ci         - Run all CI checks (test + lint)"

# Build the binary
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

build:
	@echo "Building aiman $(VERSION)..."
	go build -v -ldflags "$(LDFLAGS)" -o aiman ./cmd/aiman

# Run tests
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...
	@echo "Coverage report:"
	go tool cover -func=coverage.out

# Run linter. Pinned and run via `go run` so the linter is compiled with this
# module's own toolchain and CI runs the identical version. A system-installed
# golangci-lint is deliberately not used: a binary built with an older Go than
# the module targets refuses to run at all.
GOLANGCI_LINT_VERSION ?= v2.7.2
GOLANGCI_LINT = go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint:
	@echo "Running golangci-lint $(GOLANGCI_LINT_VERSION)..."
	$(GOLANGCI_LINT) run --timeout=5m

# Format code
fmt:
	@echo "Formatting code..."
	gofmt -s -w .

# Run go vet
vet:
	@echo "Running go vet..."
	go vet ./...

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f aiman
	rm -f coverage.out

# Install to GOPATH/bin
install:
	@echo "Installing aiman..."
	go install -ldflags "$(LDFLAGS)" ./cmd/aiman

# Build and run
run: build
	./aiman

# Run all CI checks
ci: fmt vet test lint
	@echo "All CI checks passed!"
