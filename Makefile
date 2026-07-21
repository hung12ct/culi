.PHONY: help build install fmt vet lint tidy test test-race test-cover bench check clean

# ── colours ──────────────────────────────────────────────────────────────────
GREEN  := \033[0;32m
YELLOW := \033[0;33m
CYAN   := \033[0;36m
RESET  := \033[0m

## help: show this message
help:
	@echo ""
	@echo "$(CYAN)Culi$(RESET) — available targets:"
	@echo ""
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/{printf "  $(GREEN)%-14s$(RESET) %s\n",$$1,$$2}' $(MAKEFILE_LIST)
	@echo ""

# ── build ─────────────────────────────────────────────────────────────────────

# Version stamped into the binary (shown by `culi version`), from git tags:
# a clean tag → v0.1.0; commits past a tag → v0.1.0-3-gabc123; +dirty when the
# working tree has uncommitted changes; "dev" before any tag exists.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/hung12ct/culi/internal/cli.version=$(VERSION)

build: ## compile all packages
	go build -ldflags "$(LDFLAGS)" ./...

install: ## install the version-stamped culi binary to GOBIN
	go install -ldflags "$(LDFLAGS)" ./cmd/culi

# ── code quality ──────────────────────────────────────────────────────────────

fmt: ## gofmt all Go files in place
	gofmt -w .

vet: ## run go vet
	go vet ./...

lint: ## run golangci-lint
	@which golangci-lint > /dev/null || (echo "$(YELLOW)golangci-lint not found. Install:$(RESET) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest" && exit 1)
	golangci-lint run ./...

tidy: ## tidy and verify go.mod / go.sum
	go mod tidy
	go mod verify

# ── testing ───────────────────────────────────────────────────────────────────

test: ## run all unit tests
	go test ./... -count=1

test-race: ## run tests with race detector
	go test ./... -race -count=1

test-cover: ## run tests and open HTML coverage report
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)Coverage report:$(RESET) coverage.html"
	@open coverage.html 2>/dev/null || xdg-open coverage.html 2>/dev/null || true

bench: ## run all benchmarks (retrieval hot path)
	go test ./... -run='^$$' -bench=. -benchmem

# ── gate ──────────────────────────────────────────────────────────────────────

check: fmt vet lint build test ## pre-commit gate: fmt + vet + lint + build + test

# ── cleanup ───────────────────────────────────────────────────────────────────

clean: ## remove generated files
	rm -f coverage.out coverage.html
	rm -rf bin/
