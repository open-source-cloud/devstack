# Spec 26 — CLI completeness & README reconciliation

**Module:** `internal/cli` (+ `internal/docker`, `internal/state`, `README.md`) · **Milestone:** M6 (GA-blocking polish) · **Effort:** ~1–1.5w

## Purpose
Finish what the surface already promises, and stop the docs from lying. The command tree in `internal/cli/root.go:79-101` is far ahead of `README.md`: `up`/`down`/`status`/`secrets`/`trust`/`dns`/`tunnel`/`init`/`import`/`uninstall`/`template new`/`secrets ingest`/`shared`/`ws`/`store`/`alias`/`self` are all wired and tested, yet the README Commands table still marks half of them "🚧 planned". The only real stubs are `shell` and `logs` (`internal/cli/stubs.go`, currently tagged `M2`).

This spec (a) **reconciles the README to reality**; (b) lands the **one** genuinely-new command this spec owns — `shell` — plus the small promised-but-missing flags that thread into already-built modules (`up --rebuild/--skip-clone`, exposing the **already-present** `UpDeps.HealthTimeout`, `self update --force`); (c) adds the **one** net-new state concept (a thin machine-wide `workspace` registry behind `workspace list`); (d) closes the `tunnel up/down` surface gap **honoring spec 05's design** (not re-deriving it); and (e) reserves the post-1.0 verbs in the tree as milestone-tagged stubs per [spec 07](07-cli-and-aliasing.md) lines 43-48.

**This is the completeness spec, not a redesign of other specs.** Two commands that the surface lists are already **owned, in full, by their feature specs** — this spec **cross-references them and does NOT re-specify them**:
- **`logs`** — owned by [spec 16](16-logs-and-dashboard.md) (read-only SDK multiplex + demux + ring-buffer + re-attach; v2). spec 07 line 27 tags `logs # spec 16`. This spec only **re-tags the `logs` stub** to `v2 (spec 16)` and reconciles the README row — it does **not** ship a contradictory shell-out design.
- **`workspace destroy --purge-data`** — owned by [spec 13](13-doctor-diagnostics-and-teardown.md) (the teardown spec, line 79 + acceptance line 127). This spec cross-references it; the destructive tenant/volume teardown is spec 13's to design.

Nothing here introduces a new engine, provisioner, or generation path; determinism of generated artifacts is unaffected (none of these verbs write generated files).

## Decisions
- **README is wrong, not the code — fix the docs.** Rewrite the Commands table to ✅ everything wired in `root.go`, document the undocumented-but-shipped verbs (`init`, `import`, `status`, `uninstall`, `template new`, `secrets ingest`, `shared gc/doctor`), mark `shell` as the one closing gap, and mark `logs` as **v2 (spec 16)** — not "shipped".
- **`shell` is the only genuinely-new command this spec owns.** spec 07 lists `shell <service>` (line 26) with `ValidArgsFunction` completion (line 13) but no dedicated behavior spec — so its design lands here. It is host-side, over the compose project, reusing `internal/docker` — no new module.
- **`logs` is deferred to spec 16, not re-designed.** spec 16 already specifies the full read-only-SDK pipeline and JSON contract. Re-implementing it here as a `docker compose logs` shell-out would contradict spec 16's read-only/no-flock/demux/re-attach design and its `ts/service/project/stream/container/line` JSON shape. This spec re-tags the stub and reconciles the README; if a thin non-TUI cut is pulled forward, it lands **under spec 16** with spec 16's approach.
- **No new lifecycle phases.** `tunnel up/down` are standalone cobra commands managing the cloudflared container directly (mirroring the standalone-resource-command pattern, **not** a saga phase), and they obey [spec 05](05-networking.md)'s `[]Route`-derived ingress + the `secret://` refusal guard. `up --rebuild/--skip-clone/--health-timeout` are flags threaded into the existing `orchestrate.UpDeps` (semantics owned by [spec 09](09-orchestration-and-onboarding.md) / [spec 10](10-health-readiness-and-ordering.md)).
- **`workspace list` needs a registry; nothing records workspace roots today.** The ledger is keyed by Docker context and rows are *project*-scoped (`service_ref.project`), with no path back to a workspace root. Add one **thin pointer table** (`workspace(ctx, name, root, …)`) written at `up`; derive everything else by re-reading each `workspace.yaml` at list time. Migration discipline is owned by [spec 08](08-state-locking-and-lifecycle.md) (additive, forward-only).
- **Reserve, don't implement, the post-1.0 verbs.** `db` (children), `dashboard`, `ide`, `template push`, `telemetry` register as milestone-tagged `stub()` placeholders (existing `internal/cli/stubs.go` helper) exactly as [spec 07](07-cli-and-aliasing.md) lines 43-48 intend. `shell` graduates *out* of `stubs.go`; `logs` stays a stub, re-tagged to `v2 (spec 16)`.
- **`--json`/`--quiet` contract is non-negotiable for the new headline verbs** (`workspace list --json`, `tunnel up --json`) — same posture as `doctor --json`/`status --json` (ARCHITECTURE §7.9).

## README reconciliation (the corrected Commands table)
Replace the stale `README.md` Commands table with the true status:

| Command | New status | Note |
|---|---|---|
| `up` / `down` | ✅ | saga lands `up`/`down` (M2/M6); add `--rebuild/--skip-clone` + expose `--health-timeout` (this spec; semantics spec 09/10). |
| `status` | ✅ | multi-repo git + service + ref-graph table; was undocumented. |
| `init` | ✅ | workspace/project authoring entry point; was undocumented. |
| `import` | ✅ | devdock → two-file migrator ([spec 14](14-self-update-and-migration.md)); was undocumented. |
| `secrets login/keygen/ingest/status/logout` | ✅ | M4 landed; **`ingest`** (`.env` import) was undocumented. |
| `trust install/uninstall/status` | ✅ | M5 landed; README said planned. |
| `dns setup/status/remove` | ✅ | M5 landed; README said planned. |
| `tunnel login/create/route` | ✅ | landed; **add `up`/`down`** (this spec; design [spec 05](05-networking.md)). |
| `template list/lint/test/init/new` | ✅ | **`new`** (scaffold) was undocumented. |
| `shared status/gc/doctor` | ✅ | `gc`/`doctor` were undocumented. |
| `ws clone/sync/status/git` | ✅ | documented. |
| `workspace destroy` | ✅ | landed (data-preserving); **`--purge-data` is [spec 13](13-doctor-diagnostics-and-teardown.md)**. **`workspace list`** added (this spec). |
| `uninstall` | ✅ | machine-global teardown ([spec 13](13-doctor-diagnostics-and-teardown.md)); was undocumented. |
| `self check/update` | ✅ | add `--force` (this spec; design [spec 14](14-self-update-and-migration.md)). |
| `store` / `alias` | ✅ | documented. |
| `shell` | ✅ (was 🚧) | the one stub closed by this spec. |
| `logs` | 🔭 v2 (spec 16) | full read-only-SDK design owned by [spec 16](16-logs-and-dashboard.md). |
| `db` / `dashboard` / `ide` / `template push` / `telemetry` | 🔭 v2 | reserved-in-tree stubs (this spec, per [spec 07](07-cli-and-aliasing.md)). |

Also update the README "Status" block and the quickstart "working today" list to reflect that `up`/`down`/`shell`/secrets/networking are present, and that `logs`/dashboard are the v2 observation layer.

## The one new command, designed

### `shell <service> [--project P] [-- cmd...]`
Interactive exec into a running service container of the current (or `--project`) project stack.
```
devstack shell api               # bash→sh in project "api"'s primary service
devstack shell api -- psql        # run a one-off command instead of a login shell
```
- Resolve the compose project (`devstack-<project>`) and the **service name**. With one service, default to it; with many, require an explicit `<service>` (completion via `ValidArgsFunction`, [spec 07](07-cli-and-aliasing.md) line 13). `compose exec <service>` lets compose resolve the container itself — no manual SDK container enumeration is needed here.
- Shell out to `docker compose -p devstack-<project> -f <genfile> exec <service> <cmd>` with **all three std streams inherited** (interactive TTY). This is a **new** path on the `Runner` seam: `ExecRunner.Run` (`internal/docker/compose.go:28`) wires `Stdout=os.Stdout` (line 32) and `Stderr=io.MultiWriter(os.Stderr, &buf)` (line 34, **captures** stderr) but **never sets `Stdin`** — reusing it would hang the shell with no input echo. Add `Compose.Exec` + an `InteractiveRunner` (or an `interactive bool` on the runner) that sets `cmd.Stdin = os.Stdin`, requests a real TTY (`-it`), does **no** stderr capture (the child owns the terminal), and returns the child's exit code verbatim.
- Default command when no `-- cmd`: probe `bash`, fall back to `sh` (Q-SHELL-DEFAULT-CMD). Propagate the container exit code as the process exit code.

## The promised-but-missing flags & verbs

### `up` flags (thread into `orchestrate.UpDeps`)
Semantics are owned by [spec 09](09-orchestration-and-onboarding.md) (saga flags) and [spec 10](10-health-readiness-and-ordering.md) (`--health-timeout`); this spec only wires the CLI flags.
- `--rebuild` — force `compose build --no-cache` for the project's images before `up` (distinct from the existing `--build`, which honors the generate-ledger's selective-rebuild hash). Adds a **new** `UpDeps.Rebuild` field; `composeUpPhase` (`internal/orchestrate/up.go:461`) currently calls `c.Build(ctx, false, …)` (noCache=false), so `--rebuild` must pass `noCache=true`.
- `--skip-clone` — skip the clone/sync phase for a workspace whose repos are already on disk. Adds a **new** `UpDeps.SkipClone` field so `BuildUp` omits the clone phase.
- `--health-timeout D` — **the field already exists** (`UpDeps.HealthTimeout`, `internal/orchestrate/up.go:67`, already consumed in `gateShared`, up.go:414-416); this spec only exposes the CLI flag and assigns it. `0`/unset → spec-10 default.

### `self update --force`
Design owned by [spec 14](14-self-update-and-migration.md). `selfupdate.Options` (`internal/selfupdate/update.go:21`) currently has only `Version` — add `Force bool`. `--force` re-installs the resolved release even when `res.UpToDate` (repair a corrupted/partial binary) but **still refuses package-managed installs** — the `CanSelfReplace()` refusal fires in **both** `update.go:44` and the CLI re-check in `newSelfUpdateCmd`; `--force` overrides neither. Threads as `selfupdate.Options{Force: true}`.

### `tunnel up` / `tunnel down`
Design owned by [spec 05](05-networking.md) (lines 13/32); today `internal/cli/tunnel.go` wires only `login/create/route`. This spec closes the standalone-command surface gap **without re-deriving the design**:
- `tunnel up [--detach]` — bring the managed `cloudflared` container up against the shared stack, ingress rendered from the proxy `[]Route` ([spec 05](05-networking.md)). **Default-DOWN stays the default.** Honor spec 05's guard: **refuse to tunnel a service whose env carries a non-local `secret://` value without an explicit override** (spec 05 acceptance line 32), printing the override hint. Mirrors the standalone resource-command shape, not a saga phase.
- `tunnel down` — stop the managed tunnel container; leave credentials/routes intact (reversible). `--json` reports `{tunnel, state}`.

### `workspace destroy --purge-data` — DEFERRED to [spec 13](13-doctor-diagnostics-and-teardown.md)
Not designed here. Today `destroy` is **data-preserving by design** (`internal/cli/destroy.go:117` `destroyWorkspace` never passes `-v`; the `g.JSON && !yes` guard is at destroy.go:54; the `Type 'yes'` prompt at destroy.go:65). The destructive `--purge-data` opt-in — drop this workspace's provisioned roles/dbs/buckets and remove its named volumes, under a stronger confirm — is **owned by [spec 13](13-doctor-diagnostics-and-teardown.md)** (line 79, acceptance line 127), which also owns the per-engine drop seam (internal/provision today is Postgres-only `EnsureProject`; MinIO bucket lifecycle is unbuilt) and the host-reachability concern (host-side pgx tenant drops need the 127.0.0.1 up-time provision overlay/port live). This spec only documents in the README that `--purge-data` is spec-13 territory.

## `workspace list` + the registry (the one new state concept)
Nothing records where a workspace root lives; the ledger only knows projects. Add a **thin pointer table** (additive, forward-only migration — append `schemaV3` to the `migrations` slice at `internal/state/migrations.go:14`; never edit `schemaV1`/`schemaV2`; discipline owned by [spec 08](08-state-locking-and-lifecycle.md)):

```sql
CREATE TABLE IF NOT EXISTS workspace (
    ctx        TEXT NOT NULL,    -- Docker context (same keying as the rest of the ledger)
    name       TEXT NOT NULL,    -- workspace.yaml `name`
    root       TEXT NOT NULL,    -- absolute path to the workspace root (where workspace.yaml lives)
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    last_up_at TEXT,             -- refreshed on each successful `up`
    PRIMARY KEY (ctx, root),
    FOREIGN KEY (ctx) REFERENCES docker_context(name) ON DELETE CASCADE
);
```
- **Written on `up`** (inside the flock, alongside the other ledger mutations): upsert `(ctx, name, root)` and set `last_up_at`. Read-only commands do **not** write it (reads stay lock-free, ARCHITECTURE §"reads are lock-free snapshots"). New CRUD on `*state.DB`: `RegisterWorkspace(name, root)`, `ListWorkspaces()`, `PruneWorkspace(root)`.
- **`workspace list [--json] [--prune]`** reads the table lock-free; for each row: if `root` no longer exists on disk, mark it `stale` (and `--prune` drops it); otherwise re-read `<root>/workspace.yaml` to get the live project set and join the ledger (`DB.ProjectsUsing`, `internal/state/ledger.go:151`; `DB.AllRefs`, `internal/state/ledger.go:170`) to show each workspace's shared-service refs. The registry stays a **pointer**, never a denormalized cache — the committed `workspace.yaml` remains the single source of truth (Q-WS-REGISTRY). A workspace.yaml that fails to parse degrades to a flagged row, not a failed command.
- Plain output: a table of `NAME · ROOT · PROJECTS · SHARED REFS · LAST UP`. `--json`: `[{name, root, projects:[...], shared:[{service, refs}], last_up_at, stale}]`.

```
$ devstack workspace list
NAME   ROOT                       PROJECTS  SHARED                       LAST UP
acme   ~/src/acme                 api, web  postgres-16 (2), redis (1)   2026-06-30 09:41
demo   ~/play/demo  [stale: gone] —         —                            2026-05-02 14:10
```

## Reserved post-1.0 verbs (tree-only stubs)
Register via the existing `stub()` helper (`internal/cli/stubs.go`) so help/completions stay consistent, matching [spec 07](07-cli-and-aliasing.md) lines 43-48 exactly; **remove** `shell` from `addStubCommands` (it graduates), and **re-tag** the `logs` stub from `M2` to `v2 (spec 16)`:
```go
root.AddCommand(
    stub("dashboard", "Live TUI cockpit",                                     "v2"),   // spec 16
    stub("ide",       "Generate devcontainer/.code-workspace/launch configs", "v2"),   // spec 17
    stub("telemetry", "Opt-in usage telemetry (default OFF)",                 "later"),// spec 20
    // db: parent hosting `gc` (v1, spec 13) + snapshot|restore|reset|list|pull as v2 stubs (spec 15)
    // template push|add|update|diff|ls|verify hang off the existing `template` cmd as v2 stubs (spec 19)
)
```
`db gc` is v1-scoped (its provisioned-ledger substrate exists — `DB.OrphanedProvisioned`, `internal/state/ledger.go:313`) and is **owned by [spec 13](13-doctor-diagnostics-and-teardown.md)** (line 63); it may land as a real subcommand under the otherwise-stubbed `db` parent when spec 13 ships it. The `db snapshot/restore/reset/list/pull` children stay stubs ([spec 15](15-db-snapshot-restore.md), v2).

## Behavior
1. **README rewrite** — replace the stale Commands table, Status block, and quickstart list; `logs` → v2 (spec 16); `workspace destroy --purge-data` → spec 13. No code dependency, ships independently first.
2. **`shell`** — resolve project+service → build `Compose{Project, File}` → `Exec(service, cmd, interactive=true)` shelling `docker compose … exec` with all three std streams inherited → propagate the child exit code.
3. **`up` flags** — `--rebuild` (new `UpDeps.Rebuild`, forces `noCache=true`), `--skip-clone` (new `UpDeps.SkipClone`, omits the clone phase), `--health-timeout` (expose flag for the **existing** `UpDeps.HealthTimeout`).
4. **`self update --force`** — add `selfupdate.Options.Force`; re-installs over an up-to-date binary; the package-manager refusal in both `update.go` and the CLI is unchanged.
5. **`tunnel up/down`** — standalone commands bringing the managed cloudflared container up/down against the shared stack; default-DOWN; spec 05 `secret://` refusal guard on `up`; `--json` headline.
6. **`workspace` registry + `list`** — apply the additive `schemaV3` migration; `up` upserts the row under the flock; `workspace list` reads lock-free, re-reads each root's `workspace.yaml`, joins refs, flags stale roots, supports `--prune`/`--json`.
7. **Reserved stubs** — register `dashboard`/`ide`/`telemetry` (+ `db`/`template` v2 children) as `stub()`s per spec 07; promote `shell` out of `stubs.go`; re-tag `logs` to `v2 (spec 16)`.

## Verified constraints / gotchas
- **Most of the "stubs" the README lists aren't stubs.** Only `shell`/`logs` are in `internal/cli/stubs.go`; `up`/`down`/`secrets`/`trust`/`dns`/`tunnel`/`status`/`init`/`import`/`uninstall`/`self` are fully wired in `root.go:79-101`. Reconcile docs *first*; don't re-implement working verbs.
- **`logs` is spec 16's, not this spec's.** spec 16 (`16-logs-and-dashboard.md`) owns the full read-only-SDK design (`ContainerLogs(Follow,…)` + `stdcopy` demux + ring-buffer + die/start re-attach, **lock-free, never shells `docker compose`**, JSON `ts/service/project/stream/container/line`). Do **not** ship a contradictory `docker compose logs -f` shell-out here. The current `ContainerLogs` wrapper (`internal/docker/inspect.go:136`) returns a finished string and cannot follow — extending it for follow is spec 16's job.
- **`shell` needs a TTY/stdin path that `ExecRunner` doesn't provide.** `ExecRunner.Run` (`compose.go:28`) wires `Stdout`/`Stderr` and *captures* stderr into a buffer, never sets `Stdin`. An interactive `compose exec` requires `cmd.Stdin = os.Stdin`, a real TTY (`-it`), and **no** stderr capture (the child owns the terminal). Add a distinct interactive runner/`Exec` method; don't reuse the capturing path.
- **`tunnel up` must keep spec 05's secret guard.** Default state is DOWN; `up` refuses (with override hint) any service whose env carries a non-local `secret://` value (spec 05 acceptance line 32). Ingress is rendered from the same `[]Route` as local routing — no re-derivation.
- **`workspace destroy --purge-data` is spec 13's.** internal/provision is Postgres-only `EnsureProject` (no drop), MinIO `mc rb` is unbuilt, and host-side tenant drops need the 127.0.0.1 up-time provision overlay live — all design concerns spec 13 owns. Don't invent the drop seam here.
- **`workspace list` must be keyed by Docker context like everything else.** The same workspace root under WSL2's Desktop daemon vs in-distro `dockerd` has different refs; the `workspace` table PK includes `ctx` ([spec 03](03-workspaces-and-shared-services.md)). Listing reads only the current context's rows.
- **Don't denormalize the workspace registry.** Store only the root pointer + timestamps; re-derive projects/refs from the committed `workspace.yaml` at list time. Roots that vanished are flagged `stale`, never silently dropped — a moved checkout shouldn't erase history without `--prune`.
- **`self update --force` does not override package management.** Force means "re-install even if up-to-date"; the `CanSelfReplace()` refusal (both `update.go:44` and the CLI) holds for Homebrew/dpkg/rpm-managed binaries.
- **The migration is forward-only and additive.** Append `schemaV3` to the `migrations` slice (`migrations.go:14`); never edit `schemaV1`/`schemaV2`. The `workspace` table CASCADEs on `docker_context` deletion like every other ledger table. Migration discipline is owned by [spec 08](08-state-locking-and-lifecycle.md).
- **Reserved stubs must exit 0 with a clear notice** (the existing `stub()` RunE) so `completion`/scripts that probe `--help` don't see spurious failures.
- **Single-static-binary rule holds:** `shell`/`tunnel up` shell out to `docker`/`cloudflared` (external binaries already required, DECISIONS D5/D12); no new pure-Go-vs-CGO dependency. Generated-artifact determinism is unaffected — none of these verbs write generated files (ledger/runtime ops are not determinism-gated).

## Acceptance criteria
- [ ] `README.md` Commands table marks `up`/`down`/`status`/`secrets`/`trust`/`dns`/`tunnel`/`init`/`import`/`uninstall`/`template new`/`secrets ingest`/`shared gc`/`shared doctor`/`self` as implemented; `shell` is documented as shipped; `logs` is marked **v2 (spec 16)**; `workspace destroy --purge-data` is marked **spec 13**; only post-1.0 verbs are flagged future.
- [ ] `devstack shell api` opens an interactive shell (bash→sh) in project `api`'s service, forwards stdin/TTY, and propagates the container's exit code; `shell api -- psql` runs the one-off command.
- [ ] `up --skip-clone` runs no clone phase; `up --rebuild` forces a `--no-cache` build; `up --health-timeout 5s` overrides the spec-10 readiness deadline via the existing `UpDeps.HealthTimeout`.
- [ ] `self update --force` re-installs over an up-to-date binary but still refuses (with the upgrade hint) on a Homebrew/dpkg/rpm-managed install.
- [ ] `tunnel up` starts the managed cloudflared container with default-DOWN as the default and **refuses (with override hint) any service carrying a non-local `secret://`**; `tunnel down` stops it, leaving credentials/routes intact; both honor `--json`.
- [ ] `up` records the workspace in the new `workspace` table under the flock; `workspace list` enumerates every workspace for the current Docker context with projects + shared-service refs; a removed root shows `stale` and `--prune` drops it; an unparseable `workspace.yaml` degrades to a flagged row, not a failed command.
- [ ] The `workspace` table lands as an additive `schemaV3` migration that re-applies idempotently and CASCADE-deletes with its `docker_context`.
- [ ] `dashboard`/`ide`/`telemetry` (and the `db`/`template` v2 children) appear in `--help`/completions as milestone-tagged stubs (exit 0, clear notice); `shell` no longer appears in `stubs.go`; `logs` remains a stub re-tagged `v2 (spec 16)`.
- [ ] `make ci` + `make determinism` stay green (no generated-artifact change).

## Dependencies / consumers
Consumes `internal/docker` (the `Compose` driver + a new interactive `Runner`/`Exec` seam for `shell` and `tunnel up`; `compose.go`), `internal/state` + `internal/lock` (the new `workspace` table + CRUD, written under the flock; migration discipline per [spec 08](08-state-locking-and-lifecycle.md)), `internal/orchestrate` (the `UpDeps` flags), `internal/selfupdate` (`Options.Force`), `internal/tunnel` (cloudflared lifecycle, design [spec 05](05-networking.md)), `internal/config` (re-reading each `workspace.yaml` at list time). **Cross-references (does not re-specify):** [spec 16](16-logs-and-dashboard.md) (`logs`), [spec 13](13-doctor-diagnostics-and-teardown.md) (`workspace destroy --purge-data`, `db gc`, `uninstall`), [spec 05](05-networking.md) (`tunnel up/down`), [spec 09](09-orchestration-and-onboarding.md)/[spec 10](10-health-readiness-and-ordering.md) (`up` flag semantics), [spec 14](14-self-update-and-migration.md) (`self update`), [spec 07](07-cli-and-aliasing.md) (reserved verbs). Consumed by end users, by [spec 16](16-logs-and-dashboard.md) (the dashboard reuses the `workspace list` projection), and by CI scripts (the `--json` contracts). No new external dependency; no generation-path change.

## Open questions
**Q-WS-REGISTRY** (new) — thin pointer table vs richer denormalized cache. **Decision:** thin pointer (name+root+ctx+timestamps), re-derive projects/refs at list time; never duplicate the committed `workspace.yaml`. **Q-WS-REGISTER-WHEN** (new) — which commands write the registry row. **Decision:** write on `up` under the flock; read-only commands never write (reads stay lock-free). **Q-SHELL-DEFAULT-CMD** (new) — default shell when `shell <service>` gets no `-- cmd`. **Decision:** probe bash→sh. **Q-LOGS-OWNERSHIP** (new) — keep `logs` fully owned by spec 16 (read-only-SDK design) rather than re-spec it here. **Decision:** spec 26 re-tags the stub + reconciles the README only. Inherits [Q-PLATFORM](../OPEN-QUESTIONS.md) (native-Windows `shell`/TTY is best-effort; WSL2 is the supported path) and [Q-DAEMON](../OPEN-QUESTIONS.md) (no daemon → registry refreshed lazily on `up`).