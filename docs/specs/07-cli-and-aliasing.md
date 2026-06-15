# Spec 07 — CLI architecture & aliasing

**Modules:** `cmd/devstack`, `internal/cli`, `internal/xdg` (+ release/self-update) · **Milestone:** M0 · **Effort:** ~7w

## Purpose
The command surface, the multi-alias mechanism (one binary invocable as `rq`, `uranus`, …), config plumbing, completions, packaging/release, and self-update — the scaffolding everything hangs off.

## Decisions
- **Engine:** `github.com/spf13/cobra`, wrapped by **`fang`** (`charm.land/fang/v2` — vanity import, *not* `github.com/charmbracelet/fang`) for styled help/errors, auto `--version`, `man`, `completion`. fang is a thin, removable layer — your commands stay vanilla cobra (drop back in a day if fang stalls).
- **Config:** `github.com/knadh/koanf/v2` (+ `providers/file,env,posflag`; `parsers/yaml`); precedence `flags > env > project > defaults`. **Not viper** (force-lowercases keys → breaks env-var names + `${ref}` keys; bloats the binary). koanf is not goroutine-safe — load once, treat immutable; env keys arrive lowercased so add an env-provider transform callback when merging cased sources.
- **Multi-alias = `argv[0]` dispatch:** read `filepath.Base(os.Args[0])`; if it matches a registered alias, set branding (prompt name, env-var prefix) but run the **identical** command tree. `devstack alias add rq` symlinks the binary in `XDG_BIN_HOME` and records it in the global config (this installer also replaces devdock's direnv/bootstrap activation). `--as <name>` override for tests. (Verified: `rq`/`uranus` symlinks both dispatch from one binary — the git/busybox pattern.)
- **Tech-gated dynamic subcommands:** register stack-specific commands (e.g. `composer`, `artisan`, `npm`) only when the workspace uses that stack — `cmd.AddCommand` conditionally, mirroring devdock's gating.
- **Completions:** cobra's engine via fang's `completion`; register `ValidArgsFunction` on `shell <container>`, `docker exec <container>`, `ws git … <name>` so completions resolve live service/project names from the parsed config.
- **State/XDG:** `github.com/adrg/xdg` for `ConfigHome`/`DataHome`/`StateHome`/`CacheHome` `/<tool>`. Global shared-service registry here; per-project config committed in the repo. Template cache needs a GC/TTL (grows unbounded). WSL2: refuse `/mnt/*` working dirs; keep state on the Linux side.
- **Layout:** `/cmd/<tool>/main.go` (thin: alias dispatch + `fang.Execute` + ldflags version), `/internal/*` (all impl), `/pkg/pluginsdk` (reserved, v2), `/templates` (`go:embed`).
- **Global flags & contract:** `--json` / `--quiet` / `--debug` / `--verbose`, unified error rendering, structured logging via `log/slog`; `--debug` logs every external command run (docker/git/sql/platform-CLI). Define the machine-readable `--json` output contract for `up`/`status`/`doctor`.
- **Release:** **goreleaser** OSS + GitHub Actions on tag → `darwin/{amd64,arm64}` + `linux/{amd64,arm64}` (CGO_ENABLED=0, one Linux runner), archives + checksums + Homebrew tap + `.deb`/`.rpm` (nfpm) + cosign/minisign-signed checksums (signing deferrable post-v1). Version/commit/date via `-ldflags`.
- **Self-update:** `github.com/creativeprojects/go-selfupdate` against goreleaser releases; **detect install method (Homebrew Cellar / dpkg ownership) and refuse to self-replace a package-managed binary**; after update, re-point alias symlinks + run the state-DB migration.
- **Plugins: deferred to v2** — built-in providers compile in-process behind Go interfaces; no `go-plugin`/gRPC in v1. Native Go `plugin` package rejected (OS-limited, version-brittle, panics crash host).

## Command surface (working)
```
devstack up [project...] [--profile P]... [--build] [--rebuild] [--no-hooks] [--skip-clone] [--no-preflight] [--health-timeout D] [--json]
devstack down [project...]               # whole-project teardown; autostop default off
devstack status                 # multi-repo git + service + ref-graph + last-saga table
devstack shell <service>
devstack logs [service...] [-f]
devstack doctor [--fix] [--rebuild-state] [--json]
devstack ws clone|sync|status [all|<name>...]
devstack shared status|gc|doctor
devstack db gc                  # reclaim ORPHANED provisioned roles/dbs/buckets (confirm); v2 adds snapshot/restore/reset
devstack secrets login <provider> | keygen
devstack trust install|uninstall|status
devstack dns setup              # opt-in .test
devstack tunnel login|create|route|up|down
devstack alias add|remove <name>
devstack template init|lint|test          # (registry push/pull = v2)
devstack import <path/to/devdock/project.yaml> [--dry-run] [--out DIR] [--force]   # optional migrator
devstack self update [--force]            # refuses on package-managed installs
devstack workspace destroy [--purge-data] [--yes]   # this workspace's stacks/refs (+ data with --purge-data)
devstack uninstall [--yes]      # reverse ALL machine-global artifacts (see ARCHITECTURE §7.3)
```

> Verb **semantics** live in their feature specs: `up`/`down`/`status` + saga flags ([spec 09](09-orchestration-and-onboarding.md)); `--profile` slices ([spec 12](12-service-profiles-and-selective-up.md)); health gating / `--health-timeout` ([spec 10](10-health-readiness-and-ordering.md)); `--no-hooks` ([spec 11](11-lifecycle-hooks.md)); `doctor`/`workspace destroy`/`uninstall`/`db gc` ([spec 13](13-doctor-diagnostics-and-teardown.md)); `self update`/`import` ([spec 14](14-self-update-and-migration.md)).

## Verified constraints / gotchas
- **fang import path `charm.land/fang/v2`** (vanity), Go 1.25, "experimental" — wrap, pin.
- **Windows (non-WSL2) lacks reliable symlinks** for aliasing → generate `.cmd` shims or steer Windows users to WSL2 (where symlink dispatch works). Native Windows is best-effort ([Q-PLATFORM](../OPEN-QUESTIONS.md)).
- **Self-update + aliases + state schema can brick the install** — run a forward-only state migration (with backup) on first launch after a version change; re-point all alias symlinks; refuse on package-managed installs.
- **`docker/docker/client` is deprecated** — the SDK wrapper uses `moby/moby/client` ([spec 03](03-workspaces-and-shared-services.md), [DECISIONS D4](../DECISIONS.md)).

## Acceptance criteria
- [ ] One built binary, symlinked as `rq` and `uranus`, dispatches the same command tree under each name with per-alias branding.
- [ ] `alias add rq` creates the symlink in `XDG_BIN_HOME` and records it; `alias remove` reverses it.
- [ ] Tech-gated `composer` appears only when the workspace uses a PHP stack.
- [ ] `shell <TAB>` completes live service names from the current workspace config.
- [ ] `goreleaser` produces all 4 OS/arch artifacts from one Linux runner with `CGO_ENABLED=0`.
- [ ] `self update` refuses on a Homebrew-installed binary and directs the user to `brew upgrade`.
- [ ] `--json` on `status`/`doctor` emits the documented machine-readable schema.

## Dependencies / consumers
Foundational — consumed by everything. Owns the global flag/error/log contract that other modules render through ([ARCHITECTURE §7.6](../ARCHITECTURE.md)).

## Open questions
[Q-NAME](../OPEN-QUESTIONS.md) (canonical name + alias set — needed to wire installer/completion), [Q-PLATFORM](../OPEN-QUESTIONS.md) (native Windows scope).
