# Spec 32 — AI-agent integration (MCP, skills & self-describing docs)

**Module(s):** `docs` (embed) · `internal/aidocs` · `internal/ai` · `internal/mcpserve` · `internal/config` (schema) · `internal/cli` (`ai`, `config schema`) · **Milestone:** M11 · **Effort:** ~2w · **ADR:** [D20](../DECISIONS.md)

> **What this is not.** It is not an agent, an LLM integration, or a devstack that
> calls a model. Nothing here sends anything anywhere. It is the inverse: devstack
> describing itself precisely enough that an AI coding agent already sitting in the
> user's repository can drive it correctly.

## Purpose

devstack has 39 top-level commands, a 22-template engine with its own authoring
grammar, a two-file config model with four interpolation grammars, and ~9,400
lines of documentation — none of it reachable by the coding agents that now sit
in most developers' editors.

The failure mode is specific and repeatable. An agent dropped into a devstack
workspace recognizes Docker, does not recognize devstack, and reaches for
`docker compose up` or hand-writes a `docker-compose.yaml`. Both are wrong here:
compose files under `.devstack/` are generated and get overwritten, and the
compose project name, labels and external network are tool-owned, so driving
compose directly forks a parallel stack that shares nothing with the workspace.
The same agent will put a `[[ ]]` action in a template's `description:` (a hard
lint error, because metadata keys are parsed unrendered), or write `${ref.foo}`
instead of `${ref:foo}`.

Every ingredient to prevent that already exists — `template list --json` is a
catalog, `Describe` reads metadata without rendering, `scaffold.Spec` is a
byte-stable authoring IR, every headline command has `--json`. What was missing is
**discovery**.

## Decisions

### The shape

- **One corpus, three surfaces, nothing re-authored.** `docs/` is compiled into
  the binary and served identically through `ai docs` (for agents that only have a
  shell), MCP resources (for MCP clients), and links from the emitted files.
  *Rejected a separately-authored "agent docs" set*: a second corpus is a second
  thing to keep true, and the drift would be invisible.

- **The MCP tools ARE the CLI.** Every tool handler builds a fresh
  `cli.NewRootCmd`, sets `--json` plus argv, and captures stdout — the same
  harness the CLI test suite already uses. *Rejected a parallel tool API*: it
  would duplicate 39 commands' semantics, and every future flag would have to be
  added twice. The reuse also buys the two properties below for free.

- **A fresh command tree per call, so the lock discipline is inherited.** Each
  call takes and releases the cross-process flock inside its own `RunE`. The
  long-lived MCP process never holds it, so ARCHITECTURE's "stateless CLI, no
  daemon" model survives having a server in front of it, and
  [Q-DAEMON](../OPEN-QUESTIONS.md) is not reopened.

- **Emitted files are workspace-independent.** They describe devstack, never a
  snapshot of the current projects and shared services. *Rejected embedding live
  workspace facts*: these files get committed, so a snapshot goes stale the moment
  someone adds a project, with no CI anywhere to catch it. Live facts come from
  `status --json` and `config show --json`, which the agent runs. The happy
  consequence is that the guidance is useful *before* a workspace exists —
  precisely when an agent has to learn `devstack init` — and the goldens are
  hermetic.

- **Emitted files carry navigation, not documentation.** A SKILL.md body links to
  `devstack ai docs <slug>` rather than copying prose. The corpus is therefore
  always the running binary's, `devstack self update` produces a zero-line diff in
  the user's repo, and there is no version skew between a committed `.claude/` and
  an upgraded binary.

### Ownership and merging

- **Three merge modes, not one.** `internal/ide` writes whole files, which is
  right for artifacts devstack owns and *wrong* for `AGENTS.md`, `CLAUDE.md` and
  `.mcp.json`, which belong to the user. `MergeWhole` covers
  `.claude/skills/devstack*/`; `MergeFence` replaces only a marker-fenced block;
  `MergeJSONKey` sets only `mcpServers.devstack`. Spec 17 described managed blocks
  but `internal/ide` never implemented them, so the fence generalizes the idiom
  that has been in production in [`internal/dns/hosts.go`](../../internal/dns/hosts.go)
  for `/etc/hosts`.

- **`AGENTS.md` is the portable surface; `CLAUDE.md` imports it.** AGENTS.md is
  the Linux Foundation cross-tool standard read natively by Codex, Cursor,
  Copilot, Gemini CLI, Windsurf, Zed and Aider. Claude Code reads `CLAUDE.md`, so
  devstack's block there is an `@AGENTS.md` import rather than a second copy.
  *Rejected per-tool rule files* (`.cursorrules`, `GEMINI.md`,
  `.github/copilot-instructions.md`, `.windsurfrules`): five near-duplicates is
  exactly the drift this design exists to avoid.

- **`.mcp.json` names the bare binary, never an absolute path.** An absolute path
  is correct on the machine that ran `ai install` and wrong for every teammate who
  checks the file out — which is the whole point of committing it.

### Safety

- **Write tools on by default; irreversible verbs absent.** MCP has no terminal,
  so an allowed mutating tool must inject `--yes`. That is acceptable for
  recoverable operations (`up`, `generate`, `db create`) and unacceptable for
  `workspace destroy`, `db drop` and `db reset` — so those are **not registered**
  unless `--allow-destructive`, rather than merely annotated. *Rejected annotating
  them and relying on the host to prompt*: absence is a guarantee, an annotation is
  a hint.

- **The `secrets` group is never registered at any setting**, along with `aws --`
  and `shell`. The first would put provider material into a model's context; the
  other two are arbitrary command execution that the agent's own shell already
  offers with a per-command permission prompt.

- **Every tool carries MCP annotations** (`readOnlyHint`, `destructiveHint`,
  `idempotentHint`, `openWorldHint`) so a host can gate what devstack cannot.

- **Skill frontmatter is restricted to the six Agent Skills spec fields**
  (`name`, `description`, `license`, `compatibility`, `metadata`,
  `allowed-tools`). Claude Code accepts many more, but any of them is a hard error
  when the same file is uploaded to claude.ai or packaged with the Agent Skills
  tooling. One artifact set that works everywhere beats two behind a flag, so
  "when to use" folds into `description` — which is what the router reads anyway.

- **No `` !`cmd` `` dynamic context injection** in emitted skills: it runs a
  command on every skill load (latency, a Docker touch, a surprise permission
  prompt) and is Claude-Code-only, breaking portability.

### The JSON Schema

- **Hand-authored, per [D16](../DECISIONS.md), not derived from validator tags.**
  `dsname`/`duration`/`cpus`/`platform`/`dockerhost`, `oneof`, `dive`, the
  cross-field resolvers and the `${env./self./ref:/profile}` grammar do not
  round-trip through tag introspection. The Go validator stays the source of
  truth; two tests keep the schema honest — one validates every fixture through
  both paths, the other walks the structs by reflection and asserts every
  `yaml:` field is a schema property **and vice versa**.

- **This fixes a live defect.** `internal/ide/ide.go` emitted a `$schema` URL
  pointing at `schemas/devstack.schema.json`, a path that existed at no tag, so
  every `.vscode/settings.json` and `.code-workspace` devstack has ever generated
  pointed at a 404. It also mapped `workspace.yaml` to the *project* schema; the
  two are now distinct, and a dev build falls back to `main` rather than emitting
  `v<dev-version>/`.

## CLI surface (working)

```
devstack ai install [--target skills,agents,mcp] [--check]  # emit the integration files
devstack ai check                                           # drift gate for CI
devstack ai mcp [--read-only] [--allow-destructive]         # MCP server over stdio
devstack ai docs [slug] [--search Q] [--section S] [--limit N]
devstack ai commands [--runnable]                           # the command tree as data
devstack config schema [--kind project|workspace]           # the published JSON Schema
```

`config schema` lives with `config validate`/`config show` rather than under `ai`
because the schema is an editor and LSP artifact as much as an agent one.

## Emitted artifacts

| Path | Kind | Merge |
|---|---|---|
| `.claude/skills/devstack/SKILL.md` + `reference.md` | skill | Whole |
| `.claude/skills/devstack-templates/SKILL.md` | skill | Whole |
| `.claude/skills/devstack-troubleshooting/SKILL.md` | skill | Whole |
| `AGENTS.md` | agents-md | **Fence** |
| `CLAUDE.md` | claude-md | **Fence** |
| `.mcp.json` | mcp-config | **JSONKey** |

Three skills, not one and not ten: skills route on their `description`, and
"operate devstack", "author a template" and "something is broken" have disjoint
triggers. Per-group skills (`devstack-db`, `devstack-s3`, …) would blur that
routing; `reference.md` plus `--help` covers the detail.

## Behavior

1. `ai install` resolves the root: the workspace root when one is discoverable,
   otherwise the current directory — it never requires a workspace.
2. `Build` assembles every artifact **without touching the disk**, which is what
   makes the goldens hermetic. For the fenced and JSON modes, `Data` carries the
   block or the value, not a finished file.
3. `Write` composes each artifact against what is on disk according to its merge
   mode, then performs the same atomic `writeIfChanged` (temp → fsync → chmod →
   rename) `internal/generate` and `internal/ide` use.
4. `--check` runs the identical merge and compares, so drift detection can never
   disagree with what a write would produce.
5. `ai mcp` forces quiet mode, builds the server from injected `Deps`, and serves
   stdio. A client closing the connection is a normal shutdown and exits 0.

## Verified constraints & gotchas

- **stdout purity is the whole ballgame.** An MCP stdio server must emit nothing
  on stdout but framed JSON-RPC. Tool output is captured into a buffer, quiet mode
  is forced, and an e2e test parses an entire real session as JSON-RPC — a stray
  banner or log line fails it. This is the most common way a Go MCP server ships
  broken.
- **`docs/` is now a BUILD INPUT.** `.github/workflows/ci.yml` previously skipped
  every check for doc-only changes, which would let a broken cross-link ship
  inside a release binary. `paths-ignore` is narrowed to files that genuinely are
  not compiled in (`README.md`, `PROGRESS.md`, `LICENSE`, `NOTICE`), so `docs/**`,
  `AGENTS.md`, `CLAUDE.md` and `.claude/**` all trigger the full gate. No separate
  always-on lane is needed once the list is narrowed this way.

- **Workspace discovery walks UP, which is wrong for a command that writes.** A
  single stray `workspace.yaml` in a home directory silently redirected an entire
  install there — `AGENTS.md`, `CLAUDE.md` and `.mcp.json` scattered across `$HOME`
  and skills installed machine-wide. `ai install` therefore refuses the home
  directory and any ancestor of it, and ignores a discovered workspace root that
  fails that check. Two regression tests cover it.
- **The pack must never go through `internal/template`.** The template-authoring
  content contains literal `[[ .params.version ]]` examples, and `text/template`
  with `[[ ]]` delimiters and `missingkey=error` would try to execute them. Only
  Go-built wrappers carry the binary name.
- **Binary growth: ~2.6 MiB stripped** (45.2 → 47.9 MiB), of which ~845 KiB is
  embedded content (docs, schemas, pack) and ~1.8 MiB is the MCP SDK and its
  dependencies (`google/jsonschema-go`, `yosida95/uritemplate/v3`,
  `golang.org/x/oauth2`, `segmentio/encoding`). All pure Go — `CGO_ENABLED=0`
  holds. Not gated behind a build tag: the emitted `.mcp.json` points at
  `devstack ai mcp`, so a build without it would ship a broken registration.
- **MCP SDK volatility.** The Go SDK went 0.x → 1.x recently. It is pinned, and
  `internal/mcpserve` is the single seam, per the wrap-risky-dependencies rule.
- **`.mcp.json` merging re-marshals the file**, reformatting a user's hand
  formatting. Documented and visible through `--check`; the alternative (refusing
  to touch an existing file) is worse.
- **A bare `devstack` skill claims `/devstack`** in the user's project. Intentional;
  everything else is `devstack-` prefixed.

## Acceptance criteria

- [x] `docs/` is embedded; `ai docs` lists, prints and searches it with no workspace.
- [x] Every relative link in the corpus resolves (1,188 checked); CI covers doc-only changes.
- [x] `ai commands` is derived from the live cobra tree and is byte-deterministic.
- [x] `docs/guide/command-reference.md` is asserted against the tree in both directions.
- [x] `schemas/*.json` are published, embedded, and asserted against the structs by fixture round-trip **and** by reflection over every field.
- [x] The `$schema` URL resolves, is per-kind, and falls back to `main` for dev builds.
- [x] `ai install` is idempotent; a second run changes nothing.
- [x] Hand-written content in `AGENTS.md`/`CLAUDE.md` and other servers in `.mcp.json` survive regeneration.
- [x] `ai check` exits non-zero on drift and names the stale files.
- [x] Emitted skill frontmatter uses only the six Agent Skills spec fields.
- [x] An argv[0] alias (`rq`) is reflected in every emitted command and in `.mcp.json`.
- [x] The MCP tool set is pinned by test for each of the three option modes.
- [x] Destructive tools are absent by default; `secrets` is never registered.
- [x] An allowed destructive tool injects `--yes`.
- [x] A real `ai mcp` session's stdout is 100% JSON-RPC, and a client disconnect exits 0.
- [x] `devstack://template/{name}` returns a real built-in template's source.
- [x] `ai install` refuses the home directory and ignores a workspace root discovered above it.
- [x] devstack's own repository commits its emitted files; `make ai-check` is part of `make ci`.

## Dependencies

Builds on spec 01 (config schema), spec 02 (templates), spec 07 (CLI structure)
and spec 17 (`internal/ide`, whose generation-sink shape this mirrors). Adds
`github.com/modelcontextprotocol/go-sdk`.

## Open questions

- [Q-AI-SCOPE](../OPEN-QUESTIONS.md) — repo-scoped skills only, or also
  `ai install --global` into `~/.claude/skills/`? Global is genuinely useful and
  is the leading v1.1 candidate, but it is a second install location with its own
  uninstall story and a `devstack uninstall` interaction.
- [Q-AI-PLUGIN](../OPEN-QUESTIONS.md) — ship a Claude Code plugin plus a
  marketplace manifest from this repo, so users can install with zero files in
  their own repo? It is a second distribution channel with its own versioning,
  targeting people who by definition already have the binary installed.
