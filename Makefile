.PHONY: all build test test-coverage lint clean install help

# Build settings
BINARY_NAME=deploy-senpai
BUILD_DIR=build
GO=go
GOFLAGS=-trimpath
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-s -w -X main.version=$(VERSION)

# Coverage settings
COVERAGE_FILE=coverage.out
COVERAGE_HTML=coverage.html
COVERAGE_THRESHOLD=60

all: lint test build

## build: Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/deployer

## test: Run tests
test:
	@echo "Running tests..."
	$(GO) test -v -race ./...

## test-coverage: Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	$(GO) test -v -race -coverprofile=$(COVERAGE_FILE) -covermode=atomic ./...
	@echo ""
	@echo "Coverage summary:"
	@$(GO) tool cover -func=$(COVERAGE_FILE) | tail -1
	@echo ""
	@echo "Generating HTML coverage report..."
	$(GO) tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML)
	@echo "Coverage report: $(COVERAGE_HTML)"

## test-coverage-check: Run tests and fail if coverage is below threshold
test-coverage-check: test-coverage
	@echo "Checking coverage threshold ($(COVERAGE_THRESHOLD)%)..."
	@coverage=$$($(GO) tool cover -func=$(COVERAGE_FILE) | tail -1 | awk '{print $$3}' | tr -d '%'); \
	if [ $$(echo "$$coverage < $(COVERAGE_THRESHOLD)" | bc -l) -eq 1 ]; then \
		echo "Coverage $$coverage% is below threshold $(COVERAGE_THRESHOLD)%"; \
		exit 1; \
	else \
		echo "Coverage $$coverage% meets threshold $(COVERAGE_THRESHOLD)%"; \
	fi

## lint: Run linter
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, running go vet instead"; \
		$(GO) vet ./...; \
	fi

## fmt: Format code
fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...
	@if command -v goimports >/dev/null 2>&1; then \
		goimports -w .; \
	fi

## clean: Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -f $(COVERAGE_FILE) $(COVERAGE_HTML)

## install: Install the binary
install: build
	@echo "Installing $(BINARY_NAME)..."
	install -m 755 $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)

## install-deps: Install development dependencies
install-deps:
	@echo "Installing development dependencies..."
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GO) install golang.org/x/tools/cmd/goimports@latest

## docker-build: Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(BINARY_NAME):latest .

## run: Run the application locally
run: build
	@echo "Running $(BINARY_NAME)..."
	./$(BUILD_DIR)/$(BINARY_NAME) serve --config config.yaml

## help: Show this help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':'
