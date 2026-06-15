# Spec 18 — Resource limits & multi-arch image strategy

**Module:** `internal/config` + `internal/generate` + `internal/doctor` · **Milestone:** v2 · **Effort:** ~1.5w (feature #10)

## Purpose
Stop the two recurring "why is my machine on fire" complaints: unbounded shared/project containers eating a laptop's RAM, and x86 images silently running under slow qemu emulation on Apple Silicon. This spec emits per-service CPU/memory limits into the generated compose, sums declared memory against a host RAM budget so `up`/`doctor` can **warn** before the OOM killer does, and teaches the tool to prefer native-arch images and surface emulated ones. It is a thin layer on machinery that already exists.

> **Builds on v1; deferred because the substrate had to land first.** [spec 12](12-service-profiles-and-selective-up.md) already reserved the `memoryMB` / `groups.<>.memoryHintMB` schema keys and the *summation point* in `up` (warn-only, gated on a configured `memoryBudgetMB`); [spec 02](02-templating-and-generation.md) owns the deterministic programmatic compose model; [spec 13](13-doctor-diagnostics-and-teardown.md) owns the probe matrix. This feature is pure DX polish over a working stack, so it correctly waited until the M0–M6 spine was proven — there is no reason to design RAM warnings before `up` itself is reliable.

## Per-service resource limits (config → compose)
Extend the per-service schema ([spec 01](01-config-schema.md), additive — no `apiVersion` bump) with an optional `resources` block; `memoryMB` from [spec 12](12-service-profiles-and-selective-up.md) remains the budget-summation hint and is treated as shorthand for `resources.memoryMB`:

```yaml
# devstack.yaml
services:
  api:
    template: php.laravel.nginx
    memoryMB: 768                 # spec-12 budget hint == resources.memoryMB
    resources:
      cpus: "1.5"                 # fractional cores
      memoryMB: 768              # hard limit
      memoryReserveMB: 256       # soft reservation (scheduling hint)
      pidsLimit: 512             # optional
```

`internal/generate` translates this into the typed compose model — **never string-templated** ([spec 02](02-templating-and-generation.md)) — emitting **both** spellings for compatibility:

| field | emitted as | why both |
|---|---|---|
| `cpus` | `deploy.resources.limits.cpus` **and** top-level `cpus` | `deploy.*` is the spec-blessed path; top-level is the legacy non-deploy path some toolchains still read |
| `memoryMB` | `deploy.resources.limits.memory` **and** `mem_limit` | same dual-write; values must agree or `compose-go/v2` validation rejects the doc |
| `memoryReserveMB` | `deploy.resources.reservations.memory` | reservation only meaningful under `deploy` |
| `pidsLimit` | `pids_limit` | no `deploy` equivalent |

The dual-write is deterministic (sorted keys, fixed unit rendering — always bytes, `768M` → `805306368`) so the byte-identical-output CI check ([spec 02](02-templating-and-generation.md)) and the SHA-256 rebuild hash stay stable. Limits apply to **both** project stacks and the shared stack ([spec 03](03-workspaces-and-shared-services.md)); shared-service limits are declared in `workspace.yaml shared.<svc>.resources` and emitted into `~/.devstack/shared/docker-compose.yaml`. Changing a shared service's limit is a **stateful-service restart** — gated behind the same explicit confirm as any shared recreate ([spec 03](03-workspaces-and-shared-services.md)), never silent.

## Memory budget check (the `up`/`doctor` warning)
The summation point reserved in [spec 12](12-service-profiles-and-selective-up.md) becomes real:

1. `up` sums `resources.memoryMB` over the **active-profile** services *plus* the shared services the reference-graph walk pulled in (so the budget reflects what is actually starting, not the whole workspace).
2. Compare the declared total against a budget, in priority order: explicit `workspace.yaml resources.memoryBudgetMB` → else the **probed host/VM ceiling** (below) minus a headroom reserve (default 25%).
3. On exceed: **WARN, never hard-fail** — name the offending services, the declared total vs the budget, and suggest `up --profile minimal` (the one-flag escape hatch [spec 12](12-service-profiles-and-selective-up.md) created for exactly this). `--json` carries a structured `budget` object. A `--no-budget-check` flag and `resources.memoryBudgetMB: 0` both disable it.

This is advisory accounting of **declared** limits, not live `docker stats` (that needs a daemon — [Q-DAEMON](../OPEN-QUESTIONS.md)); it catches the over-subscription *before* `up`, which is where the value is.

## Host-RAM + Docker-Desktop-VM probe (doctor matrix addition)
Add two probes to the [spec 13](13-doctor-diagnostics-and-teardown.md) matrix (additive `docker.Check{id,category,fixable}` rows; diagnose-only — there is nothing safe to auto-`--fix`):

| id | cat | what the probe actually does | remediation (one line) |
|---|---|---|---|
| `host.ram` | info | total physical RAM via the platform-correct path (below); on cgroup v2, also the effective `memory.max` of the engine cgroup | informational; baseline for the budget |
| `host.vmsize` | warn | Docker-Desktop/Colima VM memory ceiling (SDK `Info().MemTotal` — the **VM's** total, not the host's) vs the declared workspace total | raise Docker Desktop's memory in Settings → Resources, or `up --profile minimal` |

`Info().MemTotal` is **load-bearing**: on Docker Desktop / WSL2 the engine runs in a fixed-size Linux VM and `MemTotal` reports the **VM** ceiling, which is the real limit per-service limits cannot exceed in aggregate — so `host.vmsize` (not `host.ram`) drives the default budget on those platforms. Reading the true *host* RAM differs per OS and is only used for context/headroom advice:

| platform | host-RAM source |
|---|---|
| Linux (native) | `/proc/meminfo` `MemTotal`; if running in a memory-cgroup, intersect with `memory.max` (v2) / `memory.limit_in_bytes` (v1) |
| macOS | `sysctl hw.memsize` (no `/proc`); the Desktop VM ceiling still wins via `Info().MemTotal` |
| WSL2 | `/proc/meminfo` reports the **WSL VM** allocation (`.wslconfig`), *not* Windows host RAM; the Docker engine VM is separate again → trust `Info().MemTotal` for the budget, note the WSL allocation as context |

All reads are pure-Go (`/proc`/`sysctl` syscall, no cgo) preserving `CGO_ENABLED=0`. The probe degrades to `info: unknown` rather than failing if a source is unreadable.

## Multi-arch image strategy
Templates declare images by manifest-list ref (the normal case — `postgres:18`, `node:22` ship multi-arch manifests); the tool's job is to **detect emulation and warn**, not to mirror or rebuild images:

- **Native preference is automatic:** Docker/containerd select the host-arch entry from a manifest list, so no platform pin is needed when a native variant exists. devstack reads the resolved image's architecture via the **read-only SDK** (`ImageInspect` after pull, or manifest inspect) and compares to the daemon's `Info().Architecture`.
- **Emulation warning:** if the running image's arch ≠ the host arch (an `arm64` host pulling an `amd64`-only image → qemu/Rosetta), surface it — at generate/up time and in `doctor` (`arch.emulation`, warn): *"service `legacy-api` runs amd64 under emulation on arm64 — expect 2–10× slower; pin a native image or `--platform`."* qemu emulation is **correctness-safe but very slow** for DB/compute-heavy services, so this is a performance, not a failure, signal.
- **Explicit override:** honor a per-service `platform: linux/amd64` (emitted to the compose `platform:` field) and a global `up --platform <p>` override for the rare case a dev *must* force an arch (testing the x86 build on Apple Silicon). An explicit override **suppresses** the emulation warning for that service (the dev opted in).
- **doctor probe `arch.emulation` (warn):** enumerate running project+shared containers (label-filtered, `All=true`, exclude `oneoff=true` — [spec 03](03-workspaces-and-shared-services.md)) and flag any whose image arch differs from `Info().Architecture`; lists each emulated service with the native-pin remediation.

No image building, mirroring, or `buildx` orchestration is in scope — that is the template-registry frontier ([spec 19](19-template-registry.md)). devstack only reads what the daemon already resolved.

## Verified constraints / gotchas
- **Compose v2 applies `deploy.resources.limits` to plain (non-swarm) containers in current versions.** Older compose ignored the whole `deploy:` block unless `--compatibility` was passed; modern `docker compose` (≥ v2.20, our floor — [DECISIONS D5](../DECISIONS.md)) honors `limits.cpus`/`limits.memory` directly on `up`. Still emit `mem_limit`/`cpus` alongside for the legacy/non-deploy readers — and never rely on `reservations` being enforced (it is a scheduling hint only).
- **Docker Desktop caps total RAM at the VM size.** Per-service `mem_limit`s carve up a fixed pie; they do **not** raise the global ceiling. Ten services at 2 GB each cannot exceed an 8 GB VM — they OOM. The budget check must compare the declared **sum** against `Info().MemTotal`, not assume the host's physical RAM is available.
- **Reading host RAM is OS-specific and the Desktop VM hides true host RAM.** `/proc/meminfo` on WSL2 is the WSL allocation; macOS has no `/proc` (use `sysctl`); and inside the Desktop VM the engine sees only the VM. Use `Info().MemTotal` for the *budget*, the OS-specific host read only for *context* — conflating them produces false-green budget checks.
- **qemu/Rosetta emulation is correctness-safe but very slow** and can mask subtle arch bugs (timing, SIMD). Warn, never block — a dev may legitimately need to run an x86-only image, and forcing native could be impossible.
- **cgroup v1 vs v2 limit semantics differ:** v2 exposes `memory.max`/`memory.high` (unified hierarchy), v1 exposes `memory.limit_in_bytes`; reservation/OOM behavior and the file paths diverge. The probe must detect the hierarchy (`/sys/fs/cgroup/cgroup.controllers` present ⇒ v2) and read the right file; most 2026 Linux + the Docker Desktop VM are v2, but native distros may still run v1.
- **`mem_limit` and `deploy.resources.limits.memory` must agree** or `compose-go/v2` validation fails the document before write ([spec 02](02-templating-and-generation.md)) — the dual-write derives both from one canonical byte value.
- **`platform:` forces a pull even when a native variant exists** — an over-eager global `--platform` can drag every service onto emulation; scope the override narrowly and warn when it *introduces* emulation rather than removing it.

## Acceptance criteria
- [ ] A service with `resources: { cpus: "1.5", memoryMB: 768 }` emits agreeing `deploy.resources.limits.{cpus,memory}` **and** `cpus`/`mem_limit` in the generated compose, and the doc passes `compose-go/v2` validation.
- [ ] Byte-identical config inputs produce byte-identical resource fields across runs (determinism CI check, [spec 02](02-templating-and-generation.md)); the SHA-256 rebuild hash is unchanged when limits are unchanged.
- [ ] `up` with active-profile declared `memoryMB` summing above `memoryBudgetMB` (or the probed VM ceiling minus headroom) prints a **warning** naming the offending services and suggesting `--profile minimal` — and still proceeds (no hard-fail); `--json` includes the `budget` object.
- [ ] `--no-budget-check` (or `memoryBudgetMB: 0`) suppresses the budget warning entirely.
- [ ] `doctor` reports `host.ram` (info) and `host.vmsize` (warn) with the correct value source per platform; on Docker Desktop/WSL2 the budget derives from `Info().MemTotal`, not `/proc/meminfo`.
- [ ] On an arm64 host, a service pinned to an amd64-only image triggers an `arch.emulation` warning at `up` and a `doctor arch.emulation` warn row; a native multi-arch image triggers neither.
- [ ] `up --platform linux/amd64` (or per-service `platform:`) forces the arch, emits `platform:` to compose, and suppresses the emulation warning for the overridden service(s).
- [ ] The cgroup probe reads `memory.max` on a v2 host and `memory.limit_in_bytes` on a v1 host without crashing where the file is absent.
- [ ] No probe or resource read uses cgo; the binary stays `CGO_ENABLED=0`.

## Dependencies / consumers
Consumes `internal/config` (the `resources`/`platform` schema + the `memoryMB` hint already reserved in [spec 12](12-service-profiles-and-selective-up.md)), `internal/generate` (typed-compose emission + determinism — [spec 02](02-templating-and-generation.md)), `internal/docker` (read-only SDK `Info()`/`ImageInspect` — never run/exec; [spec 03](03-workspaces-and-shared-services.md)), and `internal/workspace` (the active-profile + shared-graph set to sum over — [spec 12](12-service-profiles-and-selective-up.md), [spec 03](03-workspaces-and-shared-services.md)). Adds rows to the `internal/doctor` matrix ([spec 13](13-doctor-diagnostics-and-teardown.md)) and a warning to the `up` saga ([spec 09](09-orchestration-and-onboarding.md)). **Thinner v2 (~3–4 days):** emit `resources` into compose + the declared-sum budget warning + the `host.vmsize` probe (the highest-value slice). **Full ~1.5w** adds the multi-arch emulation detection (`arch.emulation` probe + `--platform`/`platform:` plumbing), the cgroup-v1/v2-aware `host.ram` read, and the cross-platform host-RAM source matrix with golden tests per OS.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (live `docker stats` enforcement / OOM watch would want a daemon; v2 stays declarative-only). [Q-RUNTIME](../OPEN-QUESTIONS.md) (Colima/Lima/Podman VMs report ceilings differently from Docker Desktop — the `host.vmsize` probe assumes Docker only). **New:** [Q-RESOURCES] — should the memory budget **hard-fail** `up` above a *second*, higher threshold (e.g. declared sum > VM ceiling, guaranteeing OOM), or stay warn-only forever? Recommendation: warn-only in v2 (a dev may know their services won't all peak at once); revisit a `--strict-budget` opt-in if OOM-during-up becomes a common support ticket.
