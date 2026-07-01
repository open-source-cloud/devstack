# devstack developer tasks. The binary is `devstack` (built from ./cmd/devstack);
# the Go module is github.com/open-source-cloud/devstack. `make install` drops the
# binary into a local bin dir; `make smoke` exercises the built binary end-to-end.

BINARY      := devstack
PKG         := github.com/open-source-cloud/devstack
VERSION_PKG := $(PKG)/internal/version
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).Date=$(DATE)

# Where `make install` puts the binary: $XDG_BIN_HOME if set, else $(PREFIX)/bin
# (~/.local/bin — the same place `devstack alias add` installs symlinks).
# Override: make install PREFIX=/usr/local   or   make install BINDIR=/somewhere/bin
PREFIX      ?= $(HOME)/.local
BINDIR      ?= $(or $(XDG_BIN_HOME),$(PREFIX)/bin)

# The release binary MUST be CGO-free (static), but `go test -race` REQUIRES cgo.
# So CGO is set per-target, never globally.
.PHONY: build run test test-race integration e2e test-one vet fmt fmt-check lint tidy vuln clean snapshot cross ci nightly determinism install uninstall smoke help

build: ## Build the static binary into ./dist
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY) ./cmd/devstack

run: ## Build and run (use ARGS="doctor --json")
	CGO_ENABLED=0 go run -ldflags '$(LDFLAGS)' ./cmd/devstack $(ARGS)

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 go test -race ./...

integration: ## Run the real-daemon integration tests (needs Docker + cgo)
	CGO_ENABLED=1 go test -tags=integration -race ./...

e2e: ## Run the CLI end-to-end tests (builds the binary; mutates Docker)
	DEVSTACK_E2E=1 go test -tags=e2e ./tests/e2e/...

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

cross: ## Cross-compile the 4 CGO-free release targets (build-only, output discarded)
	@set -eu; for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
	  echo "-> $$t"; \
	  GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o /dev/null ./cmd/devstack; \
	done

ci: fmt-check vet build test-race ## What CI runs

nightly: fmt-check vet cross test-race determinism ## Full nightly gate (make ci + 4-target cross-build + determinism)

determinism: build ## Assert generation is byte-identical across runs/paths (M1, spec 02)
	@set -eu; \
	bin="$$PWD/dist/$(BINARY)"; \
	a="$$(mktemp -d)"; b="$$(mktemp -d)"; \
	trap 'rm -rf "$$a" "$$b"' EXIT; \
	cp -r internal/config/testdata/valid/. "$$a/"; \
	cp -r internal/config/testdata/valid/. "$$b/"; \
	DEVSTACK_WORKSPACE="$$a" "$$bin" generate --quiet; \
	DEVSTACK_WORKSPACE="$$b" "$$bin" generate --quiet; \
	if diff -r "$$a" "$$b" >/dev/null; then \
	  printf '\033[32mok\033[0m  generation is byte-deterministic across paths\n'; \
	else \
	  echo "FAIL: generated artifacts differ between runs:"; diff -r "$$a" "$$b"; exit 1; \
	fi; \
	DEVSTACK_WORKSPACE="$$a" "$$bin" generate --check --quiet || { echo "FAIL: --check reports drift after generate"; exit 1; }

install: build ## Install the binary into $(BINDIR) (override with PREFIX= or XDG_BIN_HOME=)
	@install -d "$(BINDIR)"
	@install -m 0755 dist/$(BINARY) "$(BINDIR)/$(BINARY)"
	@echo "installed $(BINDIR)/$(BINARY) ($(VERSION))"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "note: $(BINDIR) is not on your PATH — add it to use \`$(BINARY)\` directly";; esac

uninstall: ## Remove the installed binary from $(BINDIR)
	@rm -f "$(BINDIR)/$(BINARY)" && echo "removed $(BINDIR)/$(BINARY)"

smoke: build ## Exercise the built binary end-to-end in an isolated XDG sandbox
	@set -eu; \
	bin="$$PWD/dist/$(BINARY)"; \
	sandbox="$$(mktemp -d)"; \
	trap 'rm -rf "$$sandbox"' EXIT; \
	export XDG_CONFIG_HOME="$$sandbox/config" XDG_DATA_HOME="$$sandbox/data" \
	       XDG_STATE_HOME="$$sandbox/state" XDG_CACHE_HOME="$$sandbox/cache" \
	       XDG_BIN_HOME="$$sandbox/bin" XDG_RUNTIME_DIR="$$sandbox/run"; \
	printf '\n== smoke: %s ==\n' "$$bin"; \
	echo "-> version is build-stamped"; \
	"$$bin" version | tee "$$sandbox/version.txt"; \
	grep -q 'commit' "$$sandbox/version.txt" || { echo "FAIL: unexpected version output"; exit 1; }; \
	echo "-> --help lists the command surface"; \
	"$$bin" --help 2>&1 | grep -q 'doctor' || { echo "FAIL: help missing 'doctor'"; exit 1; }; \
	echo "-> alias add/list/remove round-trip"; \
	"$$bin" alias add rq >/dev/null; \
	test -L "$$XDG_BIN_HOME/rq" || { echo "FAIL: alias symlink not created"; exit 1; }; \
	"$$bin" alias list | grep -qx 'rq' || { echo "FAIL: alias not listed"; exit 1; }; \
	echo "-> argv[0] dispatch: the symlink runs the identical tree under its own name"; \
	"$$XDG_BIN_HOME/rq" version >/dev/null || { echo "FAIL: symlinked alias does not dispatch"; exit 1; }; \
	"$$bin" alias remove rq >/dev/null; \
	! test -e "$$XDG_BIN_HOME/rq" || { echo "FAIL: alias symlink not removed"; exit 1; }; \
	echo "-> doctor --json emits the checks contract (a down daemon is OK here)"; \
	"$$bin" doctor --json >"$$sandbox/doctor.json" 2>/dev/null || true; \
	grep -q '"checks"' "$$sandbox/doctor.json" || { echo "FAIL: doctor --json missing \"checks\""; exit 1; }; \
	echo "-> generate renders + validates a workspace, and --check is idempotent"; \
	ws="$$sandbox/ws"; mkdir -p "$$ws/web"; \
	printf 'apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  postgres: { template: postgres }\nprojects:\n  - { name: web, path: web }\n' >"$$ws/workspace.yaml"; \
	printf 'apiVersion: devstack/v1\nkind: Project\nname: web\nservices:\n  web:\n    template: node.vite\n    uses: [workspace.shared.postgres]\n    env:\n      import:\n        - { from: workspace.shared.postgres, vars: [host, port] }\n' >"$$ws/web/devstack.yaml"; \
	DEVSTACK_WORKSPACE="$$ws" "$$bin" generate --quiet || { echo "FAIL: generate"; exit 1; }; \
	test -f "$$ws/.devstack/shared/docker-compose.yaml" || { echo "FAIL: shared compose not generated"; exit 1; }; \
	grep -q 'shared-postgres' "$$ws/web/.devstack/docker-compose.yaml" || { echo "FAIL: web compose missing shared-postgres ref"; exit 1; }; \
	DEVSTACK_WORKSPACE="$$ws" "$$bin" generate --check --quiet || { echo "FAIL: generate --check stale after generate"; exit 1; }; \
	echo "-> template list shows the built-ins"; \
	"$$bin" template list | grep -q 'php.laravel.nginx' || { echo "FAIL: template list missing built-in"; exit 1; }; \
	printf '\n\033[32mok\033[0m  smoke passed\n'

clean:
	rm -rf dist

help:
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
