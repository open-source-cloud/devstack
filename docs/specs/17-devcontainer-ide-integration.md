# Spec 17 — Devcontainer & IDE integration

**Module:** `internal/ide` (+ `internal/generate`) · **Milestone:** v2 · **Effort:** ~2w (feature #9)

## Purpose
Meet developers in their editor: emit, from the same resolved config, the editor artifacts that point at devstack's already-generated compose stacks — per-repo `.devcontainer/devcontainer.json`, a multi-repo VS Code `.code-workspace`, per-language debugger attach configs, and the `# yaml-language-server:` schema modeline that turns `workspace.yaml`/`devstack.yaml` into autocompleted, validated files. This is **almost entirely generation**: it rides the existing deterministic pipeline ([spec 02](02-templating-and-generation.md)) and the workspace service graph ([spec 03](03-workspaces-and-shared-services.md)), adding one new sink, not new orchestration.

> **Post-1.0 because** it builds on three v1 substrates that must be solid first: the deterministic generation engine + `writeIfChanged` ([spec 02](02-templating-and-generation.md)), the tool-owned compose project name / labels / external-network stanza ([spec 03](03-workspaces-and-shared-services.md)), and the published JSON Schema + `$schema` modeline policy ([spec 01](01-config-schema.md), [DECISIONS D16](../DECISIONS.md)). Without those, an IDE artifact pointing at the wrong project name or a drifting network is worse than none. It is also pure quality-of-life — it never gates `up` — so it correctly waits behind the four pillars.

## What `ide gen` emits
A single `devstack ide gen` (and a `--json` manifest of written paths) produces, per the workspace it discovers:

| Artifact | Path | Drives |
|---|---|---|
| Dev Container | `<repo>/.devcontainer/devcontainer.json` | VS Code Dev Containers, JetBrains Gateway, GitHub Codespaces |
| Multi-root workspace | `<workspace-root>/<name>.code-workspace` | VS Code multi-root (all repos + shared logs) |
| Debug configs | `<repo>/.vscode/launch.json` (+ `.idea/` run configs, opt-in) | per-language debugger attach |
| Schema modeline | injected into `workspace.yaml`/`devstack.yaml` on `init`/`ide gen` | editor validation/autocomplete |
| Neovim/LSP hint | `<workspace-root>/.devstack/ide/lsp.json` (+ README snippet) | `yaml-language-server`, `dap` attach map |

All paths and references are derived from the **service/project name index** the config layer already builds (the same index that powers `${ref}` resolution and shell/exec completions, [spec 07](07-cli-and-aliasing.md)) — never from string guesses — so a service rename re-flows into every artifact.

## Devcontainer model (the load-bearing call)
The generated `devcontainer.json` uses the **`dockerComposeFile` + `service` + `workspaceFolder`** form, *not* a standalone `image`/`build`, so the IDE attaches to the **exact same container devstack runs** — same shared infra, same provisioned DB role, same secret env. To make that correct, the file MUST:

- list the **generated** compose file(s) for that repo under `dockerComposeFile` (relative to `.devcontainer/`), and name the project service in `service`;
- set `"runServices"` to *only* that repo's services (the devcontainer must not try to own the shared stack — devstack owns `devstack-shared` lifecycle, [spec 03](03-workspaces-and-shared-services.md));
- inherit, never redefine, the external network: the referenced compose already carries `networks: { devstack_shared: { external: true } }` and the tool-owned project name (`-p devstack-<name>`) via a sibling `name:`/`COMPOSE_PROJECT_NAME` — the devcontainer must reuse it so the IDE's `compose up` lands in the same project and doesn't fork a parallel network or container set;
- use `"overrideCommand": false` and `"shutdownAction": "none"` so the IDE neither hijacks the entrypoint nor tears down a stack devstack and other repos share.

Because the IDE may run its own `docker compose up` against these files, the generated compose must be **self-consistent without devstack present for that one repo** (network is `external: true` and assumed pre-created by `devstack up`); the devcontainer is an *attach* surface, not an *orchestration* surface. devstack still owns network-ensure and shared services; a `postCreateCommand` of `devstack up <repo> --skip-clone --no-hooks` (opt-in) reconciles the ledger/refs if the user opened the folder cold.

## Multi-root `.code-workspace` & debugger attach
- The `.code-workspace` lists each repo folder plus a virtual entry for `.devstack/` (generated artifacts, logs), sets workspace-level `yaml.schemas` mappings, and recommends the Dev Containers extension. Folder order follows the workspace's declared repo order for stable diffs.
- **Debugger attach needs published host ports.** Per [spec 03](03-workspaces-and-shared-services.md), bridge IPs are not host-routable on Docker Desktop (macOS/WSL2), so a `launch.json` "attach" config can only reach a debug port (delve `:2345`, `debugpy`, `--inspect`, `xdebug 9003`) if it is **published to the host**. `ide gen` therefore requests host-port allocation for any service with `debug: true`, going **through `internal/workspace` port allocation inside the flock** ([spec 08](08-state-locking-and-lifecycle.md)) — it never invents a port itself — and writes the resolved `localhost:<port>` into the attach config. Without an allocated port, it emits a commented-out stub plus a remediation line, not a wrong config.
- Per-language presets keyed off the resolved stack (the same tech-gating that registers `composer`/`artisan`/`npm`, [spec 07](07-cli-and-aliasing.md)): Go→delve, Node→`pwa-node` attach + `localRoot`/`remoteRoot` path map, Python→debugpy, PHP→Xdebug `pathMappings`. `remoteRoot`/`workspaceFolder` come from the container's known mount, not a guess.

## Schema modeline & editor validation
On `init` and `ide gen`, devstack injects the first-line modeline
`# yaml-language-server: $schema=https://raw.githubusercontent.com/open-source-cloud/devstack/v<ver>/schemas/devstack.schema.json`
(pinned to the running binary's schema version) into `workspace.yaml`/`devstack.yaml`, and adds equivalent `yaml.schemas` globs to the `.code-workspace` for editors that prefer settings over modelines. The published schema is the **same hand-authored JSON Schema** the structural validator loads ([spec 01](01-config-schema.md), [DECISIONS D16](../DECISIONS.md)). Crucially, the editor's `yaml-language-server` (AJV-based) is **not** spec-conformant with the Go `validator/v10` semantic layer on edge keywords — so the modeline is an *authoring aid only*; the **Go validator remains the source of truth** and `up`/`doctor` still reject configs the editor accepted (and may accept ones the editor red-squiggled). `ide gen --offline` rewrites `$schema` to a `file://` path under `.devstack/` for air-gapped/pinned setups.

## Determinism & the managed-block contract
- Artifacts are produced through the existing pipeline: typed struct → marshal (sorted keys, JSONC for `devcontainer.json`/`launch.json`) → `writeIfChanged` (atomic tmp + rename) → golden tests. Byte-identical inputs yield byte-identical files (ARCHITECTURE §3); CI asserts it.
- **Regeneration must never clobber user edits.** devcontainer/launch files are commonly hand-tweaked, so devstack writes only inside a fenced **managed block** and preserves everything outside it:
  - JSONC files: a `"// devstack:managed:begin … end"` comment-fenced object region; on rewrite, parse, replace only the managed keys, re-emit, and **bail with a clear error if a user edited inside the fence** (offer `--force`).
  - First-time creation writes the whole file; thereafter it is merge-by-managed-block, never overwrite.
- **Commit-vs-gitignore follows [Q-GEN](../OPEN-QUESTIONS.md):** default **gitignore** (regenerated freely, `.devcontainer/`/`.vscode/` added to the managed `.gitignore` stanza), with `--commit-artifacts` for teams that want reviewable editor configs checked in. The schema modeline is *always* committed (it lives in the user's source file, not a generated one).

## Editor-target coverage
- **VS Code** — primary: Dev Containers (`dockerComposeFile` attach) + multi-root `.code-workspace` + `launch.json`. Fully generated.
- **JetBrains Gateway** — consumes the same `devcontainer.json` (Gateway's Dev Containers support reads the spec); JetBrains run/debug configs (`.idea/runConfigurations/*.xml`) are opt-in (`--jetbrains`) because the XML format is finicky and less load-bearing than the portable spec.
- **neovim / generic LSP** — no proprietary project file; devstack emits `.devstack/ide/lsp.json` (schema map + per-service DAP attach host:port) and a README snippet wiring `yaml-language-server` + `nvim-dap`. This keeps the non-VS-Code path first-class without owning an editor's config format.

Selection of which targets to emit is config-driven (`ide.targets: [vscode, jetbrains, lsp]` in `workspace.yaml`, default `[vscode]`) so a team standardizes on one set and CI golden tests stay scoped; `ide gen --target <name>` overrides per-invocation. Unknown/disabled targets are skipped silently, never error, so adding an editor later is additive.

## Verified constraints / gotchas
- **A `dockerComposeFile` devcontainer must reference the SAME external network and tool-owned project name/labels** as `devstack up`, or the IDE forks a second `devstack_shared`-less project — broken DNS to `shared-postgres`, duplicate containers ([spec 03](03-workspaces-and-shared-services.md)). Reuse `name:`/`COMPOSE_PROJECT_NAME`, don't let the IDE recompute it.
- **Debugger attach requires published host ports** — bridge IPs are not host-routable on Docker Desktop (macOS/WSL2); only an allocated, published port reaches a debug socket. Allocate inside the flock; never bind-guess ([spec 03](03-workspaces-and-shared-services.md), [spec 08](08-state-locking-and-lifecycle.md)).
- The editor `yaml-language-server` is **AJV-based and not fully spec-conformant** with the Go validator — the **Go validator is the source of truth** ([DECISIONS D16](../DECISIONS.md)); ship schema that sticks to broadly-supported keywords so editor and authority diverge minimally.
- `devcontainer.json`/`launch.json` are **JSONC** (comments + trailing commas); use a JSONC-aware marshal/parse, not `encoding/json`, or the managed-block fence and user comments are destroyed on round-trip.
- The **Dev Containers CLI is a separate Node tool**; devstack generates the *spec file*, it does not embed or shell the `devcontainer` CLI (would violate the single-static-binary constraint). Opening the folder is the editor's job.
- **WSL2 path duality:** a `.code-workspace` authored from the Linux side must use Linux paths and the `vscode-remote://wsl+<distro>/…` convention; never emit a `/mnt/c` path (devstack refuses `/mnt/*` working dirs anyway, [ARCHITECTURE §8](../ARCHITECTURE.md)).
- `mounts`/`workspaceFolder` must match the project compose's actual bind mount; deriving them from the typed model (not defaults) avoids the "edits don't appear in the container" class of bug.
- Secrets reach the devcontainer **only** because they are already per-service `environment:` keys in the generated compose ([spec 04](04-secrets.md)); `ide gen` must **never** copy a resolved secret value into `containerEnv`/`remoteEnv` (a committed-file leak) — a CI test asserts no secret value lands in any IDE artifact ([ARCHITECTURE §7.5](../ARCHITECTURE.md)).

## Acceptance criteria
- [ ] `ide gen` in a two-repo workspace writes one `.devcontainer/devcontainer.json` per repo (correct `service`, `dockerComposeFile`, `runServices`) and one `<name>.code-workspace` listing both repos + `.devstack/`.
- [ ] Opening a generated devcontainer attaches to the *same* container/network as `devstack up` (same `devstack-<name>` project, DNS to `shared-postgres` resolves) — no duplicate network or container set.
- [ ] A service marked `debug: true` gets a host port allocated through the flock-guarded allocator and a working `launch.json` attach config at `localhost:<port>`; a service without it gets a commented stub + remediation, never a wrong port.
- [ ] Byte-identical config → byte-identical IDE artifacts on re-run (`writeIfChanged`, no spurious diff); CI determinism check passes.
- [ ] A user edit *outside* the managed block survives regeneration; an edit *inside* the fence is detected and `ide gen` refuses without `--force`.
- [ ] `init`/`ide gen` injects the `$schema` modeline pinned to the binary's schema version; removing it and re-running restores it idempotently.
- [ ] No resolved secret value appears in any `devcontainer.json`/`launch.json`/`.code-workspace` (CI grep + golden assertion).
- [ ] A service rename in `devstack.yaml` re-flows into every artifact's references (name index, not string match).

## Dependencies / consumers
Consumes `internal/generate` (typed marshal + `writeIfChanged` + golden harness, [spec 02](02-templating-and-generation.md)), `internal/config` (the service/project name index + published schema, [spec 01](01-config-schema.md)), `internal/workspace` + `internal/lock` (host-port allocation for debug attach inside the flock, [spec 03](03-workspaces-and-shared-services.md)/[spec 08](08-state-locking-and-lifecycle.md)), and the `--json`/error contract of `internal/cli` ([spec 07](07-cli-and-aliasing.md)). It is purely a *generation sink* — it never runs or execs containers (Compose/IDE own that; the Engine SDK stays read-only, [ARCHITECTURE §4](../ARCHITECTURE.md)). **Thinner v2 (~1w):** VS Code only — devcontainer + `.code-workspace` + Go/Node `launch.json` + the schema modeline. **Full (~2w)** adds JetBrains run configs, the neovim/LSP path, the managed-block merge/force machinery, and debug-port allocation wired through the workspace lock.

## Open questions
[Q-GEN](../OPEN-QUESTIONS.md) (commit vs gitignore generated artifacts — IDE files inherit the same policy). New: **Q-IDE-DEBUGPORTS** — should `debug: true` allocate a *persistent* published host port (stable across `up`, but consumes the registry and a host port even when no debugger attaches) or an *ephemeral* port requested only by `ide gen`/an explicit `--debug` `up` (frees ports but the `launch.json` value can drift between runs)? Recommendation: persistent per-`(project,service)` debug port in the ledger, gated behind `debug: true`, so committed `launch.json` stays valid — confirm against the port-budget concern on low-port hosts.
