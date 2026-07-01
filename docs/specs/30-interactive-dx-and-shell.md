# Spec 30 — Interactive DX: active context, shell integration & authoring TUIs

**Module:** `internal/cli` (`use`, `context`, `shell-init`, `project`, `env`), `internal/activectx` (the persisted-context store, new), `internal/branding` (logo asset, new), `internal/prompt`/`internal/tui` (the shared TUI substrate), `internal/state` (active-context row), `internal/alias` (`Branding`) · **Milestone:** post-GA DX lane (0.x beta line; never forces v1.0.0) · **Effort:** ~6w (phased) · **ADR:** [D18](../DECISIONS.md#d18-active-context--shell-integration)

## Purpose
devstack is fast and correct but **cold**: every command starts with no context banner, there is **no notion of an "active" workspace or project** (each data-plane command re-derives "the single/first project" via `defaultProject` — [`internal/cli/resource.go`](../../internal/cli/resource.go) line ~337 — and `defaultProjectFromModel`, [`internal/cli/messaging.go`](../../internal/cli/messaging.go) line ~113), switching context means `cd`-ing by hand, and the installer **only prints a PATH hint** ([`install.sh`](../../install.sh) lines ~128-134) — it never wires zsh/fish, completions, or an `eval` hook. There are also no TUIs for authoring **projects** or **env/secrets**, even though the interactive substrate already exists (`internal/prompt`, the `dashboard` Bubble Tea model). This spec adds the DX layer: an **active-context model**, a **`use`/`context`** pair, an opt-in **`shell-init` eval hook** (the "execute, don't print" fix — the enabler for real switching, completions, and a prompt segment), a **project TUI**, an **env/secrets TUI**, a **console context header**, and **ASCII-logo branding**. Everything is built on the substrate devstack already ships; nothing here changes the deterministic `generate` pipeline or the lock-first concurrency model.

This spec extends the interactive lineage of [spec 22](22-init-wizard.md) (init wizard), [spec 23](23-template-authoring.md) (`template new`) and [spec 24](24-env-ingestion.md) (`secrets ingest`), reusing their `internal/prompt` theme and the non-TTY fallback contract (ARCHITECTURE §7.9). The JS/monorepo template library and the task-graph runner are a **separate** RFC, [spec 31](31-js-monorepo-templates-and-run.md).

## Decisions

### Active context (the "current workspace/project", kubectl-style)
- **Two-layer resolution: a persisted default + a per-shell override.** A machine-global "active context" is the default; any terminal overrides it per-shell via env. Resolution order for every command, in precedence: `--project` flag → `DEVSTACK_PROJECT` env → persisted active project → the existing `defaultProject` fallback (single/first project). Workspace resolution likewise: `DEVSTACK_WORKSPACE` env (already honored, [`internal/config/discover.go`](../../internal/config/discover.go)) → persisted active workspace → the upward `workspace.yaml` walk. **No behavior changes when nothing is set** — the current single/first-project default still wins, so existing scripts are unaffected.
- **The persisted default lives in the SQLite ledger, keyed by Docker context.** A new single-row `active(ctx, workspace_root, project, updated_at)` table in `internal/state` via an **append-only, forward-only migration** (spec 08 rule; never edit a released migration). Keyed by Docker context like the rest of the ledger (DECISIONS D6) so WSL2's two daemons don't share an active context. Written under the flock (it is a state mutation); reads are lock-free snapshots. See `[Q-CTX-SCOPE]`.
- **`defaultProject`/`defaultProjectFromModel` become the single resolution seam.** Both helpers gain the env→persisted lookup ahead of their current sorted-first fallback, so **all** db/s3/queue/topic/stream/resource/shell commands inherit active-context awareness with no per-command change.

### `use` + `context`
- **`devstack use [name]` sets the active context; bare + TTY opens a fuzzy picker.** `name` may be a workspace (registered in the ledger — `workspace list`) or a project in the current workspace. With no arg on a TTY it launches a **Bubble Tea v2 fuzzy-list picker** (the `dashboard` model pattern, [`internal/cli/dashboard_model.go`](../../internal/cli/dashboard_model.go), `bubbles/v2/list`). Because **a child process cannot mutate its parent shell**, `use` behaves in two modes: (a) **under the shell hook** it prints `cd <root>` / `export DEVSTACK_WORKSPACE=… DEVSTACK_PROJECT=…` on stdout for the wrapper function to `eval`; (b) **without the hook** it writes the persisted default and prints a human confirmation + a one-line "add `eval \"$(devstack shell-init …)\"` for in-shell switching" hint. This is the direct answer to "today it only prints, it should execute".
- **`devstack context` prints the resolved context; honors `--json`.** Workspace name + root, active project, the project's Postgres **role + grant tier** ([`internal/provision`](../../internal/provision), read/write/admin from spec 29), Docker context + `backend` target (local vs remote — `config.BackendConfig.IsRemote()`), and version. All fields come from `buildManager` ([`internal/cli/shared.go`](../../internal/cli/shared.go) line ~147); no new data sources. `--json` emits the same as a machine object. This is the non-shell way to answer "which workspace/project/role am I in".

### Shell integration — `devstack shell-init <shell>`
- **One command emits everything a shell needs; the user `eval`s it.** `devstack shell-init zsh|fish|bash` prints, for the target shell: (a) the install-dir **PATH export** (replacing the print-only hint in `install.sh`), (b) **completion loading** via fang's already-available `completion` subcommand ([`internal/cli/root.go`](../../internal/cli/root.go) line ~124; fang provides `completion` — spec 07), (c) the **`devstack()` wrapper function** that runs the binary and, for `use`/`cd`, `eval`s its stdout so switching mutates the live shell, and (d) an **opt-in prompt-segment** function. Users add `eval "$(devstack shell-init zsh)"` to `~/.zshrc` / `~/.config/fish/config.fish`. This is a purely **opt-in** hook — it does **not** reintroduce mandatory direnv (spec 07 deliberately dropped direnv for `argv[0]` symlinks); the symlink aliasing keeps working untouched.
- **Ship real plugin artifacts + teach the installer.** Add a `completions/` tree (generated bash/zsh/fish) and a minimal oh-my-zsh plugin dir + a `*.plugin.fish`, packaged by `.goreleaser.yaml` (today it packages only docs). `install.sh` gains **shell detection** (`$SHELL`/`$ZSH_VERSION`/`$FISH_VERSION`) and, interactively, offers to append the `eval` line to the right rc file (never silently; prints the exact line under `--quiet`/non-TTY). Completions today are generatable but **installed nowhere** — this closes that gap.
- **A prompt segment surfaces context always-on.** The `shell-init` output defines a `devstack_prompt_info` helper (workspace ∕ project ∕ role, kube-ps1 style) users can splice into `PROMPT`/`fish_prompt`. It shells `devstack context --quiet --prompt` (a fast, lock-free path that reads the ledger snapshot only). See `[Q-PROMPT-COST]`.

### Authoring TUIs (project, env/secrets)
- **`devstack project` group — `new`/`edit`/`list`/`rm`.** TTY → a Bubble Tea v2 model (the `dashboard` pattern + `internal/prompt.IsInteractive` gate, [`internal/prompt/prompt.go`](../../internal/prompt/prompt.go) line ~17); non-TTY/`--json`/`--no-input` → a flag path. Follows the **"two faces, one builder"** rule from [spec 22](22-init-wizard.md) ([`internal/cli/init_tui.go`](../../internal/cli/init_tui.go) line ~23): wizard and flags feed **one** pure builder that emits or edits `devstack.yaml` (Service `template`/`params`/`uses`/`ports`/`env`/`healthcheck`), and registers the project in `workspace.yaml`'s `projects:`.
- **Editing existing files must preserve comments/order — an AST round-trip, not struct marshal.** `project edit`/`env` mutate a hand-written user `devstack.yaml`, so they use a goccy **AST-level** editor (surgical node updates) rather than the generate-side `writeIfChanged` (which owns *generated* artifacts and would reflow the file). New emitters live beside `scaffold.EmitWorkspaceYAML` (spec 22) as `scaffold.EditProjectYAML`. See `[Q-YAML-ROUNDTRIP]`.
- **`devstack env` group — a key→value editor for local vars AND secrets.** An interactive editor over a service's `env.raw`/`env.prefixed` (plain local KV — **no external store needed**, the mechanism documented in the usage guide) and `secret://` values, reusing the huh-form pattern of [`internal/cli/secrets_ingest_tui.go`](../../internal/cli/secrets_ingest_tui.go) + `prompt.Theme()`. Local vs secret classification mirrors `secrets ingest` ([spec 24](24-env-ingestion.md)); secret values route through the existing providers and are **never written to disk** (the spec 04 valueless-env-key coupling). **"Workspace-scoped env" is a TUI convenience** meaning "write this key to every project" — it is **written per-project into each `devstack.yaml`**, with **no `workspace.yaml` schema change** (deliberate decision — keeps [spec 01](01-config-schema.md) untouched).

### Console context header + branding
- **A shared `renderContextHeader(mgr, g)` helper, gated like the update notice.** A compact one/two-line header (workspace · project · role · docker-context · version) printed atop `status` and `up` output, suppressed `if g.JSON || g.Quiet` — the established precedent (the self-update notice returns early on `--json`/`--quiet`, [`internal/cli/root.go`](../../internal/cli/root.go) line ~61). The same projection backs `devstack context` and the prompt segment (one code path, three surfaces).
- **ASCII logo via cobra's help hook — fang has no logo option.** Embed a `devstack` logo (`internal/branding/logo.txt`, `go:embed`), rendered `lipgloss`-styled through cobra `root.SetHelpFunc`/`SetUsageTemplate` (**currently unused** — confirmed) on `--help`, on a bare invocation, and above `version`. **fang v2.0.1 exposes no banner/logo injection** (only `WithVersion`/`WithCommit`/`WithTheme`/`WithColorSchemeFunc`/… — verified in the module cache), so the cobra help func is the injection point. All decorative output is gated under `--json`/`--quiet`. Add optional `Logo`/`Tagline` fields to `alias.Branding` ([`internal/alias/alias.go`](../../internal/alias/alias.go) line ~243) so `rq`/`uranus` aliases can rebrand.

### Cross-cutting
- **No new mutation escapes the lock.** The only state write here is the active-context row → under the flock. Everything else (`context`, header, `shell-init`, logo) is read-only or pure output. TUIs never enter the Bubble Tea runtime on a non-TTY (`prompt.IsInteractive`), preserving the headline `--json`/`--quiet` contract.
- **New deps: none beyond the v2 charm stack** already added in [spec 22](22-init-wizard.md) (`bubbletea/v2`/`bubbles/v2`/`huh/v2`/`lipgloss/v2`/`fang/v2`) — all pure-Go, CGO-free.

## CLI surface
```
devstack use [name]                 # set active workspace/project; bare+TTY → fuzzy picker
  --project <name>                  #   force-select a project in the current workspace
  --print                           #   emit the eval script even without the shell hook

devstack context                    # print resolved workspace/project/role/context/version
  --json                            #   machine object
  --prompt                          #   terse single-line form for a shell prompt segment

devstack shell-init <zsh|fish|bash> # emit PATH + completions + devstack() wrapper + prompt helper
                                    # usage: eval "$(devstack shell-init zsh)"

devstack project new [name]         # author a devstack.yaml (+ register in workspace.yaml)
devstack project edit [name]        # AST round-trip edit of an existing devstack.yaml
devstack project list               # list workspace projects (+ --json)
devstack project rm <name>          # unregister a project (--yes; never deletes repo files)
  # shared: --template --uses --port --env k=v ... --dry-run --force --no-input

devstack env [service]              # interactive key→value editor (local env.raw + secret://)
  --project <name>                  #   target project (default: active/first)
  --all-projects                    #   apply a key to every project (written per-project)
  --set KEY=VALUE                   #   non-interactive set (repeatable)
  --secret KEY                      #   mark KEY as a secret:// value (routed to a provider)
  --unset KEY                       #   remove a key
  --json --no-input                 #   scriptable / CI

# branding: no new command — logo renders on `--help`, bare invocation, and `version`
```

## Acceptance criteria
- With `eval "$(devstack shell-init zsh)"` loaded, `devstack use <ws>` changes the current shell's directory and sets `DEVSTACK_WORKSPACE`/`DEVSTACK_PROJECT`; without the hook it persists the default and prints a hint (exit 0). `<TAB>` completes commands + live project/service names.
- `devstack context --json` returns the resolved workspace, project, Postgres role + grant tier, Docker context/backend, and version; the plain form prints the same as a header.
- All db/s3/queue/topic/stream/resource/shell commands honor `DEVSTACK_PROJECT`/the persisted active project; with nothing set, behavior is byte-identical to today (single/first project).
- `devstack project new`/`edit` produce a config that passes `config validate`; `edit` preserves comments and key order in the target `devstack.yaml`.
- `devstack env` sets `env.raw` keys for local values and routes `--secret` keys through a provider with **no secret value in any written file** (CI-asserted, as in spec 04).
- The logo renders on `--help` and bare invocation but **never** under `--json`/`--quiet`; `status`/`up` show the context header, suppressed under `--json`/`--quiet`.
- `make ci` + `make determinism` stay green; the active-context migration is append-only; no existing generated artifact changes.

## Open questions
- `[Q-CTX-SCOPE]` — persist the active context **per-Docker-context** (consistent with the ledger, and correct for WSL2's split daemons) vs. one global default. Lean: per-Docker-context.
- `[Q-YAML-ROUNDTRIP]` — the comment/order-preserving goccy AST editor for `project edit`/`env`: build it in `internal/scaffold`, or adopt a small third-party YAML-edit helper (must stay pure-Go/CGO-free).
- `[Q-PROMPT-COST]` — the prompt segment shells `devstack context` on every prompt render; measure cost and consider a cached, ledger-only fast path (or a background-refreshed cache file) so shells stay snappy.
- `[Q-USE-TARGET]` — should `use <name>` disambiguate workspace vs project by lookup order (project-in-current-workspace first, then registered workspaces), or require `use --project`/`use --workspace`? Lean: lookup order + a `--workspace`/`--project` disambiguator.
</content>
