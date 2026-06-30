# Spec 22 — Interactive `init` wizard (workspace + shared services)

**Module:** `internal/cli` (`init`), `internal/scaffold` (the ordered emitter + wizard model), `internal/prompt` (the TUI interface + non-TTY fallback) · **Milestone:** post-GA / v0.2 polish lane (after M7, ships in v0.2.0 — the project is in BETA; this never forces a v1.0.0) · **Effort:** ~2w

## Purpose
Turn the cold-start of a workspace from "hand-author two YAML files against [spec 01](01-config-schema.md) from memory" into a guided, validated `devstack init` that emits a correct-by-construction `workspace.yaml` (and, optionally, per-repo `devstack.yaml` stubs). Today both files are hand-written; there is no `init` command (not even a stub), and the only existing config emitter is `devstack import` ([spec 14](14-self-update-and-migration.md), `internal/migrate`). This spec adds the missing onboarding front door: pick the shared engines (Postgres/Redis/MinIO/…), fill their typed params from template metadata, name the workspace, and write a file that is **structurally validated before it is written** — so the very next `devstack generate`/`up` starts from a parseable, well-formed config. The wizard is a thin TUI over the same data the rest of the tool already exposes (`template.Describe`, `builtinSource()`, `config.Workspace`); its hard requirement is a **fully scriptable, non-interactive equivalent** so CI, `--json`, and non-TTY shells never depend on the TUI.

## Decisions
- **One command, two faces.** `devstack init` runs a **Bubble Tea v2 TUI** on an interactive TTY and a **flag-/stdin-driven non-interactive path** everywhere else. The two paths build the *same* in-memory `config.Workspace` and call the *same* emitter — the TUI only collects inputs.
- **Authors `workspace.yaml` only (the shared layer).** Name, `aliases`, `profiles.default`, `shared`, and `projects[]` refs. Per-repo `devstack.yaml` stays a separate portable file ([spec 01](01-config-schema.md)); the wizard may scaffold a *minimal* `devstack.yaml` stub per declared project behind an opt-in, but never folds project service config into `workspace.yaml`.
- **The shared-engine catalogue is `builtinSource()` filtered by `Provides`.** Only templates with a non-empty `Provides` (today `postgres`/`redis`/`minio`; plus any store-custom engine that declares one) are offered as shared services. `php.*`/`node.*` (no `Provides`) are project templates and are hidden from the shared picker. Same source `generate` uses, so custom store templates appear automatically ([spec 02](02-templating-and-generation.md)).
- **Per-service param forms are generated from `ParamSpec`.** For each chosen engine, render one field per `template.Describe(...).Params[name]` (`Type` string/int/bool, `Default`, `Required`, `Description`). Required-without-default params are mandatory in the form; defaults are pre-filled and omitted from output when left at default (diff-stable, minimal YAML).
- **Pre-seed from the global store.** If `$DEVSTACK_HOME/config.yaml` exists, offer its `shared` set (`store.Load`, default `postgres@16`/`redis`/`minio`) as the default multi-select selection. The wizard never *writes* the store — it only reads it as a starting point (store authorship stays `store init`'s job).
- **Emit via a shared ordered emitter, not struct marshal.** New `scaffold.EmitWorkspaceYAML(config.Workspace) ([]byte, error)` builds `yaml.MapSlice`/`MapItem` in fixed key order (`apiVersion`, `kind`, `name`, `aliases`, `profiles`, `shared`, `projects`; empty sections omitted) and `yaml.Marshal`s it — the proven `internal/migrate` pattern (migrate.go already hand-builds the same `MapSlice`). `internal/migrate` is refactored to call this same emitter so there is **one** goccy builder, not two divergent ones, with byte-stability golden-tested.
- **Validate before write (structural).** The assembled model is rendered, then the bytes are run through `config.LoadWorkspaceOnly` (or a new exported `config.ValidateWorkspaceBytes`) *before the real file lands* — apiVersion/kind header, every `dsname`, and overall shape. A malformed selection is caught in the wizard / before the file is written, never as a parse failure on the next `generate`. **Note the limit:** this is *structural* validation only; the full shared-graph + `${ref}` cross-resolution that `validateModel`/`generate` perform needs each project's `devstack.yaml` on disk (which init does not require), so that layer is necessarily deferred to the next `generate` once the repos are present.
- **No-clobber + dry-run, mirroring `import`.** Refuse to overwrite the **target dir's** `workspace.yaml` (or any target stub) without `--force`; with `--force`, back up originals first. Separately, if `config.Discover` finds a workspace.yaml in a *parent* dir, refuse with an "already inside a workspace at <path>" error (nested workspaces are unsupported, [spec 01](01-config-schema.md)). `--dry-run` prints the would-be file(s) and a validation verdict, writes nothing. `--out <dir>` redirects output.
- **The TUI is a custom Bubble Tea v2 program — modern aesthetics are a first-class requirement, not a nicety** (owner directive: "a really nice modern TUI for CLIs"). The wizard is a hand-written `charm.land/bubbletea/v2` model composed from `charm.land/bubbles/v2` components — `list` (the shared-engine multi-select, each row showing `Provides`/`DefaultPort`/description), `textinput` (name/alias/param fields), a **`viewport` live-preview pane** that re-renders the would-be `workspace.yaml` on every keystroke, plus `spinner`/`progress`/`help`/`key`. `charm.land/huh/v2` is **embedded for the linear sub-forms** (per-service param entry) where a form is the right primitive — it is not the top-level driver. The layout is a cohesive two-pane `charm.land/lipgloss/v2` theme (picker left, live preview right) with adaptive color, rounded borders, and a persistent keymap footer — **one shared theme + model scaffolding across every devstack TUI**, owned by `internal/prompt`/`internal/tui` and reused by [spec 23](23-template-authoring.md)/[spec 24](24-env-ingestion.md).
- **New TUI dependencies: the v2 charm stack (all direct).** Add `charm.land/bubbletea/v2` + `charm.land/bubbles/v2` + `charm.land/huh/v2` on top of the already-vendored `charm.land/lipgloss/v2`/`charm.land/fang/v2` — all pure-Go, CGO-free, safe for the `CGO_ENABLED=0` static binary, pinned to the v2 line. Wrapped behind `internal/prompt` so the Bubble Tea runtime is never entered on a non-TTY.
- **Honors the global output contract.** Under `--json`, `--quiet`, `CI`, or a non-TTY stdin/stdout, the TUI **does not launch**: `init` either runs the flag-driven non-interactive path (if enough flags were given) or exits non-zero with a one-line "not a TTY — pass `--service`/`--name` or run in a terminal" guidance. `--json` emits a machine summary of what was written.

## CLI surface
```
devstack init [flags]

  # interactive (TTY): launches the huh wizard, ignores most flags as pre-fills
  devstack init

  # non-interactive / scriptable (no TUI):
  --name <dsname>              workspace name (default: basename of CWD, dsname-sanitized)
  --service <engine[@ver]>     add a shared service; repeatable
                               e.g. --service postgres@16 --service redis --service minio
  --param <svc>.<key>=<val>    set/override one shared-service param; repeatable
  --alias <dsname>             add a workspace alias; repeatable
  --profile <name>             profiles.default (default: dev)
  --project <name>=<path>[,git=<url>]   add a projects[] ref; repeatable
  --scaffold-projects          also write a minimal devstack.yaml stub per --project
  --from-store                 seed the shared set from $DEVSTACK_HOME/config.yaml
  --out <dir>                  output directory (default: CWD)
  --dry-run                    print result + validation verdict, write nothing
  --force                      overwrite existing files (backs up originals first)
  --no-input                   never launch the TUI; require flags (implied by --json/--quiet/non-TTY/CI)
  --accessible                 huh accessible mode (screen-reader friendly, no full-screen redraw)
```

The in-memory target is exactly `config.Workspace`:
```go
ws := config.Workspace{
    APIVersion: config.APIVersion,    // "devstack/v1"   (fixed)
    Kind:       config.KindWorkspace, // "Workspace"     (fixed)
    Name:       name,                 // dsname-validated, live
    Aliases:    aliases,              // each dsname
    Profiles:   config.Profiles{Default: profile},
    Shared: map[string]config.SharedSvc{ // key=service name (dsname)
        "postgres": {Template: "postgres", Params: map[string]any{"version": "16"}},
        "redis":    {Template: "redis"},
        "minio":    {Template: "minio"},
    },
    Projects: projects, // []config.ProjectRef{Name,Path,Git}
}
// scaffold.EmitWorkspaceYAML(ws) -> ordered goccy bytes (NOT yaml.Marshal(ws))
// config.ValidateWorkspaceBytes(b) / config.LoadWorkspaceOnly -> structural check BEFORE write
//   (NOT config.validateModel — unexported; NOT config.LoadAt — loads project dirs that may not exist)
```

## Behavior
The pipeline is identical for both faces; only step 2 differs (TUI vs flags).

1. **Detect + guard.** Resolve the output dir (`--out` or CWD). Check the **target dir** for an existing `workspace.yaml`; if present and neither `--force` nor `--dry-run` is set → refuse with "workspace.yaml already exists at <path>; use --force to overwrite or run from a fresh directory", exit non-zero. Separately, `config.Discover()` walks up for a *parent* workspace; if one is found above the target → refuse with "already inside a workspace at <path>" (nested workspaces unsupported). Decide the mode: TUI iff `term.IsTerminal(stdin)&&term.IsTerminal(stdout)` **and** none of `--json`/`--quiet`/`--no-input`/`CI` are set; else non-interactive.
2. **Collect inputs.**
   - **TUI path** (`internal/prompt`): a custom Bubble Tea v2 model with a left picker + right live `workspace.yaml` preview pane (embedded `huh` sub-forms for the linear groups) —
     (a) **Name** input (validated live against `dsNameRE ^[a-z][a-z0-9_-]{0,62}$`, pre-filled with the sanitized CWD basename);
     (b) **Shared services** multi-select listing `builtinSource().List()` filtered to non-empty `Provides`, each row showing name + `Provides`/`DefaultPort`/description (the `template list` render), default-checked from the store set if `--from-store`/store present;
     (c) **per-service param group** for each picked engine, one field per `ParamSpec` (text for `string`/`int`, confirm for `bool`), required fields enforced, defaults pre-filled;
     (d) optional **aliases** (repeatable text, each dsname-validated) and **profile** (default `dev`);
     (e) optional **projects** (name=dsname, path, git) — opt-in screen, skippable.
   - **Non-interactive path:** assemble the same struct from `--name`/`--service`/`--param`/`--alias`/`--profile`/`--project`, applying template defaults for any unset param and **failing fast** (mirroring `effectiveParams`) on a missing required param with no default.
3. **Assemble + default-overlay.** Build `config.Workspace`. For each shared service, overlay user params on top of the template defaults; **drop any param left at its default** so output stays minimal and diff-stable. Sort `shared` keys and `projects` deterministically.
4. **Validate in memory (structural).** Render via `scaffold.EmitWorkspaceYAML`, then run the bytes through `config.ValidateWorkspaceBytes` (or write to an **isolated** temp dir and call `config.LoadWorkspaceOnly` there, so Discover cannot escape upward to an unrelated parent). This checks the `apiVersion: devstack/v1` + `kind: Workspace` header and every `dsname`, without requiring project dirs to exist. On failure: TUI surfaces the `file:line:col`/field error inline and returns to the offending group; non-interactive prints the error and exits non-zero **without writing**. (Full `${ref}`/shared-graph validation runs at the next `generate`, once project repos are on disk.)
5. **Preview + confirm.** TUI shows the rendered YAML (+ "validates: ok") in a final confirm screen; `--dry-run` (either face) prints the same to stdout and stops. `--json` prints a summary object (`{workspace: <path>, shared: [...], projects: [...], wrote: bool}`).
6. **Write (no-clobber, atomic).** `MkdirAll(out, 0o755)`; if a target exists and `--force`, copy it to `<name>.bak.<ts>` first. Write `workspace.yaml` atomically (temp file in the same dir + `rename`, `0o644`) — the `store.Save`/`import` pattern. With `--scaffold-projects`, write each `projects[].path/devstack.yaml` stub (`apiVersion`/`kind: Project`/`name`/empty `services: {}`) under the same no-clobber rule. The file carries a leading `# Generated by \`devstack init\` — edit freely; re-run is safe with --force.` provenance comment.
7. **Report.** Print the written path(s) and a one-line next step (`run \`devstack up\` to start the shared stack`). Exit 0.

No ledger or shared-stack mutation happens here — `init` is pure YAML authorship, so **no flock is taken** (same as `import`).

## Verified constraints / gotchas
- **Add `charm.land/huh/v2`, NOT `github.com/charmbracelet/huh` (v1).** huh v2 is built on Bubble Tea v2 + Lip Gloss v2 (`charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`), the exact vanity stack already vendored (`charm.land/lipgloss/v2 v2.0.1`, `charm.land/fang/v2 v2.0.1`). The v1 module pulls bubbletea v1/lipgloss v1 and would **double-vendor a conflicting charm stack**. Pin to the v2 line (currently `charm.land/huh/v2 v2.0.3`) to stay byte-aligned. README import is literally `import "charm.land/huh/v2"`.
- **The whole charm terminal stack is CGO-free** — terminal I/O goes through `golang.org/x/sys` + `charmbracelet/x/term`/`x/ansi` + `ultraviolet`, all already vendored under `CGO_ENABLED=0`. huh adds no native deps. Safe for the single static binary across darwin/linux {amd64,arm64}; no build tags. Keep `make vuln`/govulncheck in CI after adding.
- **A TUI must never be the only path.** `form.Run()` errors when stdin is not a TTY; do not let that bubble as a crash. Gate on `golang.org/x/term.IsTerminal` (already a direct dep) **before** entering bubbletea and route to the flag path — `--json`/`--quiet`/`CI`/non-TTY must produce a deterministic result or a clear guidance error, never a half-drawn TUI. Offer `huh.WithAccessible()` via `--accessible` for screen readers.
- **Emit YAML via goccy ordered nodes, never `yaml.Marshal(struct)` / a `map`.** goccy randomizes map key order and renders `16` as `16.0` ([DECISIONS](../DECISIONS.md)); struct-marshal would also drop the fixed header ordering. Reuse the `internal/migrate` `MapSlice`/`MapItem` builder — extract it to `scaffold.EmitWorkspaceYAML` and have `migrate` call it, so there is exactly one emitter and its output is golden-tested for byte-stability.
- **Validate *before* writing — with the RIGHT entrypoint.** Use the EXPORTED `config.LoadWorkspaceOnly` (which structurally validates workspace.yaml and explicitly tolerates absent project dirs) or a new thin `config.ValidateWorkspaceBytes([]byte) error`. Do **NOT** use `config.validateModel` (it is unexported — lowercase, not callable from `internal/cli`/`internal/scaffold`) and do **NOT** use `config.LoadAt` (it reads every `projects[].path/devstack.yaml` and errors on a missing dir — wrong for an init where repos aren't cloned yet). This turns "produces a file that fails the next `generate`" into "fails in the wizard with an inline `file:line:col`".
- **Validate names live, per the real regex.** Workspace name, each shared key, alias, and project name must satisfy `dsNameRE ^[a-z][a-z0-9_-]{0,62}$`. Sanitize the CWD-basename default (lowercase, strip illegal chars) — a repo dir like `My.App` must become a valid pre-fill (`my-app`), not an invalid one the user must fix.
- **Only offer engines with a non-empty `Provides`.** A `SharedSvc` whose template lacks `Provides` isn't a shared engine (it's a project template) and would fail the shared-graph resolution in generation. Filter the picker on `template.Describe(...).Provides != ""`.
- **Enforce required params like `effectiveParams` does.** A chosen engine with a required, default-less param must block (TUI) or fail-fast (non-interactive) — emitting it unset produces a config that `generate` rejects. Conversely, drop params left at their template default so the file is minimal and re-running is diff-stable.
- **No-clobber is non-negotiable and atomic.** Overwriting a hand-tuned `workspace.yaml` silently is data loss; default-refuse, `--force` backs up first, write temp+rename so an interrupted run never leaves a truncated file (the `store.Save`/`import` contract).
- **`init` authors config; it does not touch shared state.** No ledger row, no network-ensure, no Docker call — therefore no flock, no Docker context dependency. It works with the daemon down (unlike `up`). This is what lets it ship in the v0.2 polish lane without the dind/integration lane.

## Acceptance criteria
- [ ] `devstack init` on an interactive TTY launches the huh wizard, lets the user pick shared engines + fill per-`ParamSpec` params, and writes a `workspace.yaml` that `devstack generate` accepts unchanged (against a workspace whose project repos, if any, are present).
- [ ] The shared-service picker lists exactly the `builtinSource()` templates with a non-empty `Provides` (postgres/redis/minio + any store-custom engine) and hides `php.*`/`node.*`.
- [ ] `devstack init --name app --service postgres@16 --service redis --service minio` (no TTY) writes a valid `workspace.yaml` with no prompts, deterministic byte-for-byte across runs.
- [ ] Under `--json`, `--quiet`, `CI`, or a non-TTY stdin/stdout, the TUI never launches; `init` either completes from flags or exits non-zero with TTY/flag guidance. `--json` emits a machine summary.
- [ ] A required, default-less param left unset fails fast (non-interactive) / blocks the form (TUI); a param left at its template default is omitted from the emitted file.
- [ ] Output is emitted via `scaffold.EmitWorkspaceYAML` (goccy `MapSlice`) with fixed key order and integer params rendered as `16`, not `16.0`; `internal/migrate` uses the same emitter (one builder, golden-tested).
- [ ] The rendered bytes pass `config.LoadWorkspaceOnly`/`config.ValidateWorkspaceBytes` (structural) **before** any file is written; a malformed selection is reported in-wizard / pre-write, never deferred to `generate`. (No use of the unexported `validateModel` or of project-loading `LoadAt`.)
- [ ] `init` refuses to overwrite the target dir's `workspace.yaml` without `--force`; with `--force` it backs up the original first; it also refuses when `Discover` finds a parent workspace; writes are atomic (temp+rename, `0o644`).
- [ ] `--dry-run` prints the rendered file(s) + a "validates: ok" verdict and writes nothing.
- [ ] `--from-store` seeds the default selection from `$DEVSTACK_HOME/config.yaml` (`store.Load`, `ok=false` ⇒ no seed) when present; `init` never writes the store.
- [ ] Adding `charm.land/huh/v2` keeps `make ci` green under `CGO_ENABLED=0` and `make vuln` clean; the static cross-build for darwin/linux {amd64,arm64} still succeeds.

## Dependencies / consumers
Consumes `internal/config` (the `config.Workspace`/`SharedSvc{Template,Params}`/`ProjectRef{Name,Path,Git}` target schema, `APIVersion`/`KindWorkspace`, `dsNameRE`, `Discover`, and the **workspace-only** validators `LoadWorkspaceOnly` / new `ValidateWorkspaceBytes` — explicitly NOT the unexported `validateModel` nor project-loading `LoadAt`), `internal/template` (`Describe`/`ParamSpec` for the param forms, the `Provides` filter, `effectiveParams` semantics), `internal/cli` `builtinSource()` (the live template catalogue), `internal/store` (`store.Load() (*store.Config, ok bool, err error)` + `DefaultConfig` for the `--from-store` seed — treat `ok=false` as "no store, no seed"), and `internal/xdg` (output-dir + store-path resolution). New: `internal/scaffold` (`EmitWorkspaceYAML`, shared with — and refactored into — `internal/migrate`, plus possibly the `ValidateWorkspaceBytes` helper landing in `internal/config`) and `internal/prompt` (the huh wrapper + non-TTY fallback, behind an interface so unit tests drive the flag path without a PTY). Wired into `NewRootCmd.AddCommand` as a vanilla cobra command taking `*GlobalOpts` (so fang stays removable). New module deps `charm.land/bubbletea/v2` + `charm.land/bubbles/v2` + `charm.land/huh/v2` (all direct, v2 line). Pairs with [spec 09](09-orchestration-and-onboarding.md) (the `up` onboarding promise + its planned bubbletea checklist — `init` is the file-authoring front door that precedes the first `up`, and shares the v2 charm stack) and [spec 14](14-self-update-and-migration.md) `import` (the *other* `workspace.yaml` emitter, now sharing one ordered builder). Git/OCI template sources for the picker arrive with [spec 19](19-template-registry.md).

## Open questions
- **Scope of v1: shared-only, or also `projects[]` + per-repo stubs?** Authoring `projects[]` (name/path/git) and scaffolding minimal `devstack.yaml` stubs adds real form surface but is what makes the output a *complete* workspace ([spec 01](01-config-schema.md)). **Decision: shared-services-first; projects opt-in (TUI screen + `--project`/`--scaffold-projects`), no project *service* authoring (that stays the per-repo file's job).**
- **Does `init` also offer to write/update the global store?** Tempting (one wizard for both), but it conflates two files with different lifecycles. **Decision: read the store as a seed, never write it.**
- **Where does the ordered emitter live?** A new `internal/scaffold` vs folding into `internal/config`. **Decision: `internal/scaffold.EmitWorkspaceYAML`, and refactor `internal/migrate` onto it.** The byte-level *validator* (`ValidateWorkspaceBytes`), by contrast, belongs in `internal/config` next to `LoadWorkspaceOnly` since it needs the unexported `structValidate`.
- **huh v2 vs a zero-dep `x/term` prompt loop.** A hand-rolled loop adds no dependency but reinvents validation/multi-select/redraw and diverges from the [spec 09](09-orchestration-and-onboarding.md) bubbletea intent. **Decision: `charm.land/huh/v2`, behind `internal/prompt` with a non-TTY fallback.**