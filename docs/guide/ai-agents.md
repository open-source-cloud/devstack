# AI agents

[devstack](../../README.md) · [Guide](./README.md) › AI agents

devstack ships everything an AI coding agent needs to drive it correctly: the
whole documentation corpus is compiled into the binary, the command surface is
available as data, and the config file formats have published JSON Schemas. An
agent working in your repository can therefore learn devstack from devstack
itself, instead of guessing.

This page covers the `ai` command group. For the config contract see
[config-reference.md](config-reference.md); for template authoring see
[templates.md](templates.md).

## Why this exists

A coding agent dropped into a devstack workspace has a specific failure mode: it
recognises Docker, does not recognise devstack, and reaches for
`docker compose up` or hand-writes a `docker-compose.yaml`. Both are wrong here —
compose files under `.devstack/` are generated and get overwritten, and the
compose project name, labels and external network are tool-owned, so running
compose directly forks a parallel stack that shares nothing with your workspace.

The `ai` group closes that gap by making devstack self-describing.

## Installing the integration into your repo

```bash
devstack ai install          # write the files
devstack ai check            # CI gate: exits non-zero if they are stale
```

`ai install` writes seven files:

| Path | Ownership |
|---|---|
| `.claude/skills/devstack/SKILL.md` + `reference.md` | generated — overwritten |
| `.claude/skills/devstack-templates/SKILL.md` | generated — overwritten |
| `.claude/skills/devstack-troubleshooting/SKILL.md` | generated — overwritten |
| `AGENTS.md` | **yours** — only devstack's fenced block is replaced |
| `CLAUDE.md` | **yours** — only devstack's fenced block is replaced |
| `.mcp.json` | **yours** — only the `mcpServers.devstack` key is set |

The last three use a marker fence or a single JSON key, so your own instructions
and your other MCP servers survive every regeneration. Commit all seven: they are
what makes the whole team's tooling devstack-aware.

`AGENTS.md` is the portable surface — Codex, Cursor, Copilot, Gemini CLI,
Windsurf, Zed and Aider read it natively. Claude Code reads `CLAUDE.md`, which is
why devstack's block there is an `@AGENTS.md` import rather than a second copy.
There is deliberately no `.cursorrules`, `GEMINI.md` or
`.github/copilot-instructions.md`: five near-duplicate files is exactly the drift
this design avoids.

Select a subset with `--target skills,agents,mcp`. Two caveats worth telling your
team: they need `devstack` on `PATH`, and in Claude Code an MCP server from
`.mcp.json` has to be approved once per user.

The emitted files are deliberately **workspace-independent** — they describe
devstack, not your current project list, so they never go stale when someone adds
a project. Live facts come from `devstack status --json` and
`devstack config show --json`, which the agent runs.

## Reading the documentation from the binary

```bash
devstack ai docs                       # every document, with a one-line summary
devstack ai docs guide/templates       # print one document as markdown
devstack ai docs 23                    # specs are addressable by number
devstack ai docs --search "port conflict"
devstack ai docs --section specs
```

Slugs mirror the repository layout:

| Slug form | Example | Covers |
|---|---|---|
| `guide/<page>` | `guide/lifecycle` | the task-oriented book |
| `<name>` | `architecture`, `decisions` | the top-level design documents |
| `specs/<nn>-<name>` or `<nn>` | `specs/02-templating-and-generation`, `02` | the per-component specs |

The corpus is the same markdown that renders in the repository — there is no
separately-authored set of "agent docs" to drift out of date. Because it is
compiled in, `devstack ai docs` works offline and without a checkout, and it
always describes *the binary you are running*.

`--json` returns the document plus its metadata, which is the form to use from a
script:

```bash
devstack --json ai docs guide/templates | jq -r .body
devstack --json ai docs --search "shared network" | jq -r '.hits[].doc.slug'
```

## The command surface as data

```bash
devstack ai commands                   # every command, one line each
devstack --json ai commands            # paths, summaries, arg specs, flags
devstack ai commands --runnable        # skip the group commands
```

The catalog is derived from the live command tree at call time, so it can never
name a verb this binary does not have. Use it instead of scraping `--help`:

```bash
# what can I do with databases?
devstack --json ai commands | jq -r '.commands[] | select(.path | startswith("db ")) | "\(.path) — \(.short)"'
```

Global flags (`--json`, `--quiet`, `--debug`, `--verbose`) are reported once under
`globalFlags` rather than repeated on every command.

## The MCP server

```bash
devstack ai mcp                      # stdio; started by your agent, not by you
devstack ai mcp --read-only          # only tools that cannot change anything
devstack ai mcp --allow-destructive  # additionally expose the irreversible verbs
```

`ai install` registers this in `.mcp.json`, so an MCP-capable agent starts it
automatically. It exposes three things:

**Tools** are the devstack commands, run exactly as the CLI runs them — a fresh
command tree per call, with `--json`. There is no parallel API to drift, tool
behavior matches the CLI by construction, and the cross-process lock is taken and
released inside each call, so the long-lived server never holds it and devstack
remains the stateless, no-daemon CLI it has always been.

| Class | Default | Examples |
|---|---|---|
| Read | on | `status`, `context`, `doctor`, `config_show`, `config_validate`, `config_schema`, `generate_check`, `template_list`, `template_lint`, `shared_status`, `ports`, `logs`, `docs`, `commands` |
| Write | on | `up`, `down`, `generate`, `run`, `db_create`, `s3_mb`, `project_new`, `expose`, `ai_install` |
| Destructive | **absent** | `db_drop`, `db_reset`, `workspace_destroy` — only with `--allow-destructive` |
| Never | — | the entire `secrets` group, `aws --`, `shell` |

Every tool carries MCP annotations (`readOnlyHint`, `destructiveHint`,
`idempotentHint`) so the host can gate it. Because MCP has no terminal, an allowed
mutating tool injects `--yes` — which is exactly why the irreversible verbs are
*absent from the tool list* by default rather than merely flagged. The `secrets`
group is never registered at any setting, so provider material cannot reach a
model's context.

**Resources** are pulled without spending a tool call:

```
devstack://docs/index          devstack://template/{name}
devstack://docs/<slug>         devstack://schema/project.json
devstack://commands.json       devstack://schema/workspace.json
```

`devstack://template/{name}` is the most useful one: "write me a template like
postgres" returns the real, currently-shipping `postgres/template.yaml` instead of
a plausible invention.

**Prompts** are guided workflows. In Claude Code they appear as
`/mcp__devstack__<name>`:

| Prompt | Does |
|---|---|
| `onboard-repo` | Add an existing repository to the workspace and bring it up |
| `add-service` | Add a service wired to the shared infrastructure |
| `write-template` | Author a template, with the lint rules up front |
| `debug-up-failure` | Work through a failing workspace in the right order |
| `migrate-from-compose` | Convert a docker-compose.yaml to the two-file model |

## Config schemas

```bash
devstack config schema                      # devstack.yaml (the default)
devstack config schema --kind workspace     # workspace.yaml
```

These are published draft-2020-12 JSON Schemas, hand-authored and round-trip
tested against the Go structs in CI. `devstack ide` already points
`yaml-language-server` at them, so an editor gives you completion and inline
validation for both files; an agent can use the same document as an exact field
contract.

The Go validator remains the source of truth — the schema describes structure,
while the cross-reference, cycle and interpolation rules are checked by
`devstack config validate`, which reports problems as `file:line:col`.

## Rules an agent should follow

These are the mistakes that actually happen, in rough order of cost:

1. **Never hand-edit anything under `.devstack/`.** It is generated. Edit
   `workspace.yaml` or `devstack.yaml` and run `devstack generate`.
2. **Never run `docker compose` against a devstack stack**, and never
   `docker network rm devstack_shared` — devstack owns the external network's
   creation and cleanup.
3. **Shared services are reached by DNS alias** (`shared-postgres`), never the
   bare service name, and by default publish no host ports. Use
   `devstack expose` when a GUI client needs one.
4. **Route mutations through devstack**, which takes a cross-process lock; do not
   run two `up`s in parallel.
5. **Destructive verbs require `--yes` under `--json`.** Do not pass `--yes` on a
   user's behalf without asking.
6. **`secret://` values never land in a generated file.** Do not try to inline
   them.
7. **`${ref:...}` uses a colon; `${env.NAME}` and `${self.attr}` use a dot.**
8. **Deep-merge replaces lists by default** — opt into `$merge: append`.
9. **Template metadata keys are parsed unrendered**, so a `[[ ]]` action in
   `description`, `provides` or `params` is a hard lint error.
10. **An engine template uses `image:` plus `provides:`/`exports:` and never
    `build:`; an app template uses `build:` and never `provides:`.**

## Orienting in an unfamiliar workspace

```bash
devstack context          # active workspace, project, docker context
devstack status           # service health and the shared-service ref graph
devstack config show      # the resolved configuration
devstack generate --check # is anything stale?
```

## See also

- [Concepts & the mental model](concepts.md) — the four nouns and the two-file model.
- [Templates](templates.md) — authoring a `template.yaml`.
- [Full config reference](config-reference.md) — every field and grammar.
- [Global flags & scripting](global-flags.md) — the `--json`/`--quiet`/`--yes` contract.

---

◀ [Aliases & argv[0] dispatch](aliases.md) · [Guide index](./README.md) · [Full config reference](config-reference.md) ▶
