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
build:
	@echo "Building aiman..."
	go build -v -o aiman ./cmd/aiman

# Run tests
test:
	@echo "Running tests..."
	go test -v -race -coverprofile=coverage.out ./...
	@echo "Coverage report:"
	go tool cover -func=coverage.out

# Run linter (requires golangci-lint to be installed)
lint:
	@echo "Running golangci-lint..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m; \
	else \
		echo "golangci-lint not found. Install it with:"; \
		echo "  brew install golangci-lint"; \
		echo "  or"; \
		echo "  go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 1; \
	fi

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
	go install ./cmd/aiman

# Build and run
run: build
	./aiman

# Run all CI checks
ci: fmt vet test lint
	@echo "All CI checks passed!"
