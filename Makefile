.PHONY: help test test-race test-short test-coverage test-ci vet build clean

# Default target
.DEFAULT_GOAL := test

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

test: ## Run all tests (with and without race detector)
	@echo "=== Running tests without race detector ==="
	go test ./...
	@echo ""
	@echo "=== Running tests with race detector ==="
	go test -race ./...
	@echo ""
	@echo "✓ All tests passed"

test-ci: vet ## Run tests exactly as CI does (race + coverage)
	go test -race -coverprofile=coverage.out -covermode=atomic -coverpkg=./bot,./config,./internal/... ./...

test-race: ## Run tests with race detector only
	go test -race ./...

test-short: ## Run tests in short mode (skip integration tests)
	go test -short ./...

test-coverage: ## Run tests with coverage report
	go test -coverprofile=coverage.out -covermode=atomic -coverpkg=./bot,./config,./internal/... ./...
	go tool cover -func=coverage.out

vet: ## Run go vet
	go vet ./...

build: ## Build the rocketbot binary
	go build -o rocketbot ./cmd/rocketbot

clean: ## Remove build artifacts
	rm -f rocketbot coverage.out
	go clean
