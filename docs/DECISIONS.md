# Technical Decisions

ADR-style record of the chosen stack. Each entry: the decision, why, and **verified corrections** — facts that were fact-checked against current (2026) reality during research and that *change* the naive choice. Treat the corrections as load-bearing; they are the difference between "compiles in a tutorial" and "compiles today."

Project-wide constraints that gate everything:
- **Single static binary, `CGO_ENABLED=0`**, cross-compiled to `darwin/{amd64,arm64}` + `linux/{amd64,arm64}` from one Linux runner. Every dependency must be pure-Go, or it's behind a build tag / external binary.
- **Go 1.25 toolchain floor** — the max required by `fang`, `validator/v10` (1.25), `compose-go` (1.24), `infisical-sdk` (1.24). Declared project-wide and enforced in CI; it's a hard contributor gate.
- Every fast-moving/risky dependency sits **behind an internal interface** so it can be swapped or vendored.

---

## D1. CLI engine — cobra + fang
**Decision:** `github.com/spf13/cobra` as the command engine, wrapped by `fang` (UX layer: styled help/errors, auto `--version`, `man` generation, `completion`). Not `urfave/cli`; do not treat fang as a framework.
**Why:** cobra is the ubiquitous standard (kubectl/gh/docker/helm) — contributors know it, completion/man are free, and it supports the tech-gated dynamic subcommand registration we need. fang is a thin, removable styling layer (drop back to vanilla cobra in a day if it stalls).
**⚠️ Verified corrections:**
- **Import path is `charm.land/fang/v2`** (vanity domain) as of v2 — *not* `github.com/charmbracelet/fang` (the README's top example is stale). go.mod reads `module charm.land/fang/v2`.
- fang declares `go 1.25.0` and self-describes as "experimental" — API stability not guaranteed. The removable-wrapper posture is the mitigation.

## D2. Templating & compose generation — **OPEN DECISION** (programmatic compose + text-template engine)
**Decision (recommended):** Build the Docker Compose document as a **typed model validated through `compose-go/v2`** — *not* by string-templating YAML. Reserve a **text-template engine** strictly for unstructured artifacts (Dockerfiles, proxy/entrypoint configs) and user-authored templates. Custom delimiters (`%{ }%` `%= =%` `%# #%`, or `[[ ]]`) so template syntax never collides with shell `${VAR}` / Dockerfile `$TAG`.

**The open part — which text-template engine:** this is a genuine fork the builder should confirm ([OPEN-QUESTIONS Q-T](OPEN-QUESTIONS.md)):
- **Option A — stdlib `text/template` + `Masterminds/sprig`** (recommended for a clean-slate). Zero engine-maintenance risk, lean, fast, `Delims()` avoids `${}` clashes. Weaker: no true template inheritance, clunky pipelines. Fine *because* we generate compose programmatically and only template text files.
- **Option B — `github.com/nikolalohinski/gonja/v2`** (pure-Go Jinja2 with first-class custom delimiters, inheritance, macros, filters). Richer for file-based user-authored templates and mirrors the devdock heritage. Cost: single-maintainer dependency; wrap behind `pkg/template` so it's forkable; reimplement the few custom filters yourself with golden tests.

> Research (which optimized for devdock-template parity) leaned **gonja**. Our design conversation (clean-slate, minimize templating) leaned **stdlib text/template + programmatic compose-go**. Since you chose clean-slate (no need to port devdock's template corpus), the stdlib path is lighter; pick gonja only if you want Jinja-style authoring for third-party templates. Either way, **compose generation stays programmatic.** See [spec 02](specs/02-templating-and-generation.md).

**⚠️ Verified corrections (apply to whichever engine):**
- **Config layering list-merge:** the Python predecessor's `deepmerge.always_merger` **appends** lists; `knadh/koanf/maps.Merge` **replaces** them. You must supply a custom merge func (append) or document replace-by-default with an opt-in `$merge: append`. `dario.cat/mergo` is *frozen but usable* (not "abandoned"), and its skip-non-empty default is wrong for layering.
- **YAML lib:** `gopkg.in/yaml.v3` is archived (Apr 2025). The lowest-risk drop-in successor is `go.yaml.in/yaml/v3`; **`goccy/go-yaml`** is a from-scratch rewrite chosen *specifically* for `file:line:col` `FormatError` + AST/path access, **not** for compatibility (it is **not** byte-compatible — e.g. emits `1.0` where yaml.v3 emits `1`; regenerate golden files; map order is randomized so use ordered structs / `MapSlice`).

## D3. Config — koanf (not viper)
**Decision:** `github.com/knadh/koanf/v2` (+ `providers/file,env,posflag`; `parsers/yaml`) for layered config; precedence `flags > env > project > defaults`.
**Why:** **viper force-lowercases all keys** — fatal for Docker env-var names and `${ref}` keys. koanf preserves case, is modular (smaller binary, ~2.9 MB vs ~12 MB transitive), and natively merges sources.
**⚠️ Verified corrections:**
- koanf case-preservation is **per-source**: a YAML key `auth.myKey` stays cased, but env `MYVAR_AUTH_MYKEY` arrives lowercased — merging cased + env sources needs an explicit env-provider transform callback.
- koanf is **not goroutine-safe** under concurrent `Get`/`Load` — load config once, treat it immutable.

## D4. Docker Engine SDK — **`moby/moby/client`**, not `docker/docker/client`
**Decision:** `github.com/moby/moby/client` for read-only plumbing (network ensure, label-filtered `ContainerList`, health/state, events).
**⚠️ Verified correction — this is the big one:** `github.com/docker/docker` and its `client` subpackage are **deprecated as of Docker v29** (govulncheck GO-2026-4883/4887). At `latest`, `go get docker/docker/client` resolves to a split module that declares its path as `moby/moby/client` but was required as `docker/docker/client` — a **hard module-path error**. Target `moby/moby/client` from day one (equally pure-Go, cross-compiles identically). Compilation ≠ runtime: daemon connectivity differs per OS (Windows named pipe; macOS/WSL2 vary `DOCKER_HOST`) — each target still needs runtime testing.

## D5. Compose lifecycle — shell out to the CLI; validate with `compose-go/v2`
**Decision:** Execute verbs by exec'ing the **`docker compose` CLI plugin** (require **v2.20+**) with explicit `-p <normalized-name>` and a **tool-owned label namespace**. Build/validate/normalize the model with `github.com/compose-spec/compose-go/v2`.
**Why:** `docker/compose/v2` is effectively unvendorable (pulls all of BuildKit, churny internals); shelling out keeps the binary small and reuses the user's trusted Compose.
**⚠️ Verified corrections:**
- `compose-go/v2` is **Apache-2.0, not MIT** — carry `LICENSE`/`NOTICE`, list Apache-2.0 in SPDX metadata.
- Filter container enumeration on **your own label**, not only `com.docker.compose.project`, because the project-name normalization (`-p` > `COMPOSE_PROJECT_NAME` > `name:` > dir basename) must be computed *exactly* or filters silently match nothing. Set `ListOptions.All=true` (else stopped/exited containers are invisible) and **exclude `com.docker.compose.oneoff=true`** (else `compose run`/`exec` one-offs inflate ref counts). Labels are user-overridable conventions — treat as advisory.

## D6. Global state store — `modernc.org/sqlite`, keyed by Docker context
**Decision:** Pure-Go SQLite (`modernc.org/sqlite`) for the ledger; WAL + `busy_timeout=5s` + `foreign_keys=ON`; versioned migrations table + backup-before-migrate.
**Why:** CGo-free preserves the static binary; keying by Docker context avoids the WSL2 two-daemon ref-count confusion.
**⚠️ Note:** pure-Go ≠ immune to `database is locked` under concurrent writers, and SQLite locking is unreliable on WSL2/9p — hence D7. See [spec 08](specs/08-state-locking-and-lifecycle.md).

## D7. Cross-process locking — `gofrs/flock` (the concurrency spine)
**Decision:** A coarse `gofrs/flock` advisory lock around **all** ledger/shared-stack mutations; lockfile under `XDG_RUNTIME_DIR` (fallback `XDG_STATE_HOME`). Single writer; reads are lock-free snapshots.
**Why:** Without it, concurrent invocations race on network-ensure, port allocation, ref-counts, and `CREATE ROLE`. This is the single biggest unowned gap; **build it first (M0)**, not last.

## D8. Per-project DB provisioning — `jackc/pgx/v5`, idempotent
**Decision:** Connect as superuser via `pgx/v5`; existence-guarded `CREATE ROLE`/`CREATE DATABASE`/`GRANT`. Not `initdb.d`.
**⚠️ Verified corrections:** `initdb.d` runs only on an empty PGDATA (and the marker is a non-empty `PG_VERSION`, so a volume with only `lost+found` still counts as fresh). `CREATE DATABASE` is **not** idempotent — guard it. **PG18+ moved PGDATA to `/var/lib/postgresql`** (was `/var/lib/postgresql/data`) — mounting the old path silently fails to persist; mount the version-correct path or set `PGDATA`.

## D9. Git — shell out to system git (`gitx`), not go-git
**Decision:** System `git` (require **>= 2.30**) behind an internal `gitx` package; `go-git` only as a build-tagged, read-only, offline fallback (never on the auth/network path).
**Why:** System git inherits the user's SSH agent, `~/.ssh/config` (IdentityFile, Host aliases, ProxyJump), `known_hosts`, and OS credential helpers **for free** — the entire value proposition. Inject HTTPS tokens via a generated `GIT_ASKPASS` shim (never in the URL/`.git/config`).
**⚠️ Verified corrections:**
- go-git *does* parse `~/.ssh/config` but **only honors `Hostname`/`Port`** — it ignores `IdentityFile`/`User`/`ProxyJump`/`Include`, so deploy-key and bastion setups silently break; and it fails when no `known_hosts` exists.
- go-git carries **2026 CVEs** (e.g. CVE-2026-33762, low-severity DoS panic) — pin **≥ v5.19.x** (not just v5.17.1); v6 is still alpha. The shallow-clone-bloat myth is *config*: set `Tags: NoTags` + `SingleBranch`.
- `status --porcelain=v2`: stability is documented for **v1 only** (treat v2 as de-facto stable, tolerate added headers); **no explicit upstream-gone field** — detect via `branch.upstream` present + `branch.ab` absent; run with `LC_ALL=C`.

## D10. Secrets — pluggable provider interface; batch `Resolve`
**Decision:** A tiny `Provider` interface with **batch `Resolve([]Ref)`** (exploits Infisical `List`, SSM `GetParameters`, SOPS whole-file decrypt). `secret://provider/path#key?opt=val` reference syntax, opaque to templates, resolved in a post-render pass. v1 providers: confirm the set in [OPEN-QUESTIONS Q-S](OPEN-QUESTIONS.md) — research recommends **SOPS+age (offline default) + AWS**; you originally named **Infisical + AWS**. Defer Vault/1Password/Doppler behind the same Factory.
**⚠️ Verified corrections:**
- **The "host env auto-propagates into compose" claim is FALSE.** A host var reaches a container only if the compose file references it; each secret name must appear **per service** as a valueless `environment: [NAME]` key (Compose resolves it from the process env) — `up` has no `-e` flag. So secrets ↔ generation are coupled; add a CI test that no secret value appears in any generated file. Values are still visible via `docker inspect` / `/proc/<pid>/environ` — document the threat model.
- AWS: ride entirely on `aws-sdk-go-v2/config.LoadDefaultConfig` (SSO/profile/env/IMDS) — no custom credential code.
- Infisical SDK is **pre-1.0** (`v0.x`, breaking changes; requires Go 1.24; free tier caps total identities at 5) — pin and wrap.
- SOPS pulls **all cloud-KMS SDKs transitively** even if you only use age (binary bloat) — consider build tags or shelling to the `sops` binary; only `getsops/sops/v3/decrypt` has a (convention-level) stable API.
- `1Password` desktop-auth needs `CGO_ENABLED=1` (breaks static build); only service-account auth is pure-Go. `nikoksr/doppler-go` is **archived** — use the `doppler` CLI.

## D11. Reverse proxy + local HTTPS — Caddy + mkcert (Caddy via `caddy-docker-proxy`)
**Decision:** **Caddy 2** as the shared reverse proxy via `lucaslorentz/caddy-docker-proxy` (label-driven, zero central config when projects come and go). Install the local root CA into host trust by **shelling out to the maintained `mkcert` binary** — **not** importing `smallstep/truststore`. Local HTTPS is **opt-in** so a broken trust path never blocks `up`.
**Why Caddy over Traefik/nginx:** shortest path to working local HTTPS — `caddy.tls=internal` is one label. Traefik needs ACME (wrong for offline) or a templated file-provider cert block; nginx has no dynamic discovery or built-in CA (the brittle path devdock used). Both stay available behind a `Proxy` interface.
**⚠️ Verified corrections:**
- `smallstep/truststore` is **pre-v1 (v0.13.0), last released Oct 2023, effectively unmaintained** — mkcert (the maintained binary it was extracted from) is the robust choice for macOS/Linux/Firefox trust. Accept a runtime dependency on mkcert (auto-install or bundle).
- **`*.localhost` is NOT uniformly zero-config:** Linux needs systemd-resolved active (not guaranteed on WSL2/minimal); **macOS resolves `*.localhost` only on macOS 26+** (earlier: non-browser clients + Safari fail); Chrome/Firefox bypass the OS resolver but Firefox ignores `/etc/hosts`. **`/etc/hosts` (idempotent, marker-fenced, sudo) is the only consistently reliable mechanism** for OS-resolver clients — own it in `internal/dns`. Default domain: `<service>.<project>.localhost`.
- WSL2 trust: the CA must reach the **Windows** store via `certutil.exe` interop (browsers run on Windows); corporate-managed Windows may block user-root CAs.

## D12. Public tunnel — cloudflared, optional, default-down
**Decision:** `cloudflared` as an **optional** managed container; locally-managed named tunnel; ingress points at the **Caddy** container (so public routing reuses local routing). **Default DOWN**, loud confirm, and **refuse to tunnel a service whose env carries non-local `secret://` values** without an override.
**Why:** exposing an auth-less, debug-enabled dev app carrying real cloud secrets is a genuine incident.
**⚠️ Verified corrections:** the **wildcard CNAME (`*.project`) must be created manually** in DNS — `cloudflared tunnel route dns` and the dashboard reject `*`. Only a single leading label is wildcardable. Free tier is generous (≈1000 tunnels). WSL2: runs as a normal Linux binary but QUIC/clock-skew can bite — offer the http2 protocol fallback.

## D13. Release & self-update — goreleaser + go-selfupdate
**Decision:** goreleaser OSS + GitHub Actions on tag; cross-compile the 4 targets; cosign/minisign-signed checksums; Homebrew tap + `.deb`/`.rpm` (nfpm). Self-update via `creativeprojects/go-selfupdate`, **refusing to replace a brew/dpkg-managed binary** (detect install method); re-point alias symlinks and run state-DB migration after update. Defer release **signing** to post-v1.

## D14. Multi-alias — `argv[0]` dispatch + symlink installer
**Decision:** Read `filepath.Base(os.Args[0])`; if it matches a registered alias, set branding (prompt, env prefix) but run the identical command tree. `devstack alias add rq` symlinks the binary in `XDG_BIN_HOME` and records it in the global config. `--as <name>` override for testing. (Verified working live: `rq`/`uranus` symlinks both dispatch from one binary — the git/busybox pattern.) Windows (non-WSL2) lacks reliable symlinks → generate `.cmd` shims or steer to WSL2.

## D15. State/XDG paths — `adrg/xdg`
**Decision:** `github.com/adrg/xdg` for `ConfigHome`/`DataHome`/`StateHome`/`CacheHome` (`/<tool>`). Global shared-service registry lives here; per-project config stays committed in the repo. Template cache needs a GC/TTL (it grows unbounded).

## D16. Validation — JSON Schema (editor) + validator/v10 (authority)
**Decision:** Hand-authored **JSON Schema draft 2020-12** (`santhosh-tekuri/jsonschema/v6`) as the structural layer **and** the editor artifact (publish `$schema` + `# yaml-language-server:` modeline → VS Code/JetBrains/neovim autocomplete). `go-playground/validator/v10` + a custom resolver as the semantic authority (cross-refs, cycle detection, capability matching). CUE explicitly rejected as a runtime dep.
**⚠️ Verified corrections:**
- `jsonschema/v6` **format assertions are OFF by default** — enable explicitly or `format` (uri/email/…) silently passes.
- The editor's `yaml-language-server` is AJV-based and **not** spec-conformant with the Go validator on edge keywords — the **Go validator is the source of truth**; keep the schema to broadly-supported keywords.
- `validator/v10` **has no reference-resolution** (implement as a struct-level validator reading the name index from `context`), **panics on bad tags** (cover every tag with tests; `recover()` around `Validate`), requires **Go 1.25**, and carries a "call for maintainers" notice — pin it.
- Guard against JSON-Schema↔struct **drift** with a CI round-trip test (validate fixtures against the schema *and* unmarshal them).

## D17. Plugin system — **deferred to v2**
**Decision:** v1 ships built-in providers compiled in-process behind Go interfaces; **no `go-plugin`/gRPC**. Reserve `/pkg/pluginsdk`. Add `hashicorp/go-plugin` (MPL-2.0; local-subprocess; TCP-loopback transport on Windows) only when third parties want out-of-tree secrets/tunnel providers. Go's native `plugin` package is rejected (Linux/macOS-only, version-brittle, panics crash the host).

---

## Dependency-risk register (pin everything; wrap risky ones)
`fang` (experimental, vanity v2, Go 1.25) · `gonja` (single-maintainer, *if chosen*) · `mkcert` (external binary) · `infisical-sdk` (v0.x) · `go-git` (alpha v6 + 2026 CVEs, fallback-only) · `validator/v10` ("call for maintainers", Go 1.25) · `moby/moby/client` (post-deprecation migration) · `1Password`/`Doppler` SDKs (v0/archived). Mitigation: pin all; wrap each behind an internal interface; run `govulncheck` + Renovate/Dependabot in CI; enforce the Go 1.25 floor.
