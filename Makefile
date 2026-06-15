# devstack developer tasks. The binary is named `devstack` (from ./cmd/devstack)
# regardless of the repo folder name.

BINARY      := devstack
PKG         := github.com/open-source-cloud/devdock-go
VERSION_PKG := $(PKG)/internal/version
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).Date=$(DATE)

# The release binary MUST be CGO-free (static), but `go test -race` REQUIRES cgo.
# So CGO is set per-target, never globally.
.PHONY: build run test test-race test-one vet fmt fmt-check lint tidy vuln clean snapshot ci help

build: ## Build the static binary into ./dist
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY) ./cmd/devstack

run: ## Build and run (use ARGS="doctor --json")
	CGO_ENABLED=0 go run -ldflags '$(LDFLAGS)' ./cmd/devstack $(ARGS)

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 go test -race ./...

# Run a single test, e.g.: make test-one RUN=TestSerializesAcquire PKG=./internal/lock
test-one:
	CGO_ENABLED=1 go test -race -run '$(RUN)' -v $(PKG)

vet: ## go vet
	go vet ./...

fmt: ## Format the tree
	gofmt -w .

fmt-check: ## Fail if not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

vuln: ## Vulnerability scan
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

tidy: ## go mod tidy
	go mod tidy

snapshot: ## Local goreleaser snapshot build (no publish)
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean

ci: fmt-check vet build test-race ## What CI runs

clean:
	rm -rf dist

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
