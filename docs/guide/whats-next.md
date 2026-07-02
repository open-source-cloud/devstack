# What's next: roadmap & current gaps

[devstack](../../README.md) · [Guide](./README.md) › What's next: roadmap & current gaps

This page is an honest map of what devstack does **today** versus what's on the
roadmap or still a genuine gap. It's grounded in [ROADMAP.md](../ROADMAP.md) and
the specs under [`docs/specs/`](../specs/) — where something doesn't exist yet,
this page says so plainly.

> **Now designed (proposed M10 "Interactive-DX" lane).** The three gaps below —
> a projects/env TUI, the monorepo task-runner, and framework templates — now have
> written RFCs: [spec 30](../specs/30-interactive-dx-and-shell.md) (active context,
> shell integration, project + env/secrets TUIs, console header, logo) and
> [spec 31](../specs/31-js-monorepo-templates-and-run.md) (JS/monorepo templates +
> hot reload + `devstack run` task graph), with ADRs
> [D18](../DECISIONS.md)/[D19](../DECISIONS.md). Still design-stage, not shipped.

## Shipped today

The core is real and in daily use:

- **Shared services + workspace lifecycle** — one warm Postgres/Redis/MinIO (and
  Kafka/NATS/RabbitMQ/LocalStack) on the `devstack_shared` network, ref-counted,
  reclaimed with `shared gc`. See [shared-services.md](shared-services.md).
- **The deterministic generation pipeline** — config + templates → compose,
  byte-identical output, `generate --check` in CI.
- **A full data plane** — `db`, `s3`, `queue`/`topic`/`stream`, the declarative
  `resources:` block, snapshots/restore. See [resources.md](resources.md).
- **Multi-repo git** (`ws`), **secrets** (`secret://` + providers), **local
  HTTPS/DNS** (`trust`, `dns`), **tunnels** (`tunnel`), self-update, the global
  store, and aliases.
- **Interactive TUIs** for a handful of authoring flows (below).

devstack ships on the **0.x beta line** (currently ~v0.18.0); 1.0 is a
deliberate, later owner call, not something automation reaches.

## The TUIs that exist today

All are Bubble Tea v2 behind `internal/prompt`, each with a non-TTY / `--json` /
`--no-input` fallback so CI never drives an interactive UI:

| TUI | Command | Spec |
|---|---|---|
| Workspace init wizard | `init` | [spec 22](../specs/22-init-wizard.md) |
| Template authoring | `template new` | [spec 23](../specs/23-template-authoring.md) |
| `.env` ingestion | `secrets ingest` | [spec 24](../specs/24-env-ingestion.md) |
| Live monitoring cockpit | `dashboard` | [spec 16](../specs/16-logs-and-dashboard.md) |

## Not yet / gaps you asked about

### A project-management TUI (create / edit / manage projects)

**Does not exist today.** The TUIs above cover workspace *init*, *template*
authoring, secrets *ingest*, and *monitoring* — there is no create/edit-projects
TUI. It is a natural extension of the existing `internal/prompt` stack (same
Bubble Tea theme, same non-TTY fallback), but it hasn't been built. For now,
projects are authored by editing `devstack.yaml` directly
([projects.md](projects.md)) or scaffolded via `init`.

### "Command-runner" / task projects & monorepo orchestration — ✅ shipped

**Now built-in.** A `tasks:` block in `devstack.yaml` declares non-container
commands with `deps:` edges; `devstack run <task>` plans the dependency graph and
runs it — independent tasks in parallel, output streamed and prefixed per task.
`run: host` runs on your host toolchain; `run: exec` runs inside a service
container via `compose exec`. Monorepo/Turborepo pipelines are covered two ways:
the `turborepo` template runs `turbo run` inside its container, or you map each
package's scripts into `tasks:` so `devstack run` owns the graph.

```yaml
# devstack.yaml
tasks:
  build: { run: host, command: ["pnpm", "build"] }
  test:  { run: host, command: ["pnpm", "test"], deps: [build] }
  lint:  { run: host, command: ["pnpm", "lint"] }
```

```bash
devstack run test          # runs build → test
devstack run test lint     # build+lint in parallel, then test
devstack run test --dry-run
```

### Framework dev servers with watch mode (Next.js, NestJS) — ✅ shipped

**Now built-in.** `node.express`, `node.nestjs`, `node.next`, `react.vite`,
`bun.app`, and `turborepo` ship as templates with **dev-mode hot reload** — they
bind-mount the project source (`..:/app`), keep an anonymous `node_modules`
volume, run `install` + the dev server, and set the framework's file-watch polling
env for WSL2. See [templates.md](templates.md). You can still author your own
variant with `template new`; a minimal `node.next`-style template:

```bash
devstack template new node.next --kind app
```

A minimal template that runs `next dev` with hot reload:

```yaml
# ~/.devstack/templates/node.next/template.yaml
schemaVersion: 1
description: "Next.js dev server (next dev) with hot reload."
params:
  nodeVersion: { type: string, default: "20" }
service:
  build: { context: build, dockerfile: Dockerfile, args: { NODE_VERSION: "[[ .params.nodeVersion ]]" } }
  command: ["npm", "run", "dev"]      # package.json: "dev": "next dev"
  environment:
    NODE_ENV: development
  volumes:
    - "./:/app"                        # bind-mount source for hot reload
```

A `node.nest` template is the same shape with `command: ["npm", "run",
"start:dev"]` (i.e. `nest start --watch`). See [templates.md](templates.md) for
the authoring workflow, delimiters, and `extends`. First-class,
built-in framework templates aren't in the box yet — but nothing blocks you
shipping them today.

### The logs / dashboard cockpit is still evolving

`logs` and `dashboard` are real, wired commands (read-only Engine SDK) — not
stubs. What's still in flight is the **fuller cockpit** ([spec 16](../specs/16-logs-and-dashboard.md)):
richer multiplexed views, more filtering, and deeper stats. Treat today's `logs`
and `dashboard` as solid-but-growing.

## Where the roadmap goes from here

At a glance ([ROADMAP.md](../ROADMAP.md) has the detail):

- **M8 (beta DX)** — release automation, the `init`/`template new`/`secrets
  ingest` TUIs. *Landed.*
- **M9 (local-cloud platform)** — the resource layer, cloud-emulation engines,
  and imperative `db`/`s3`/messaging verbs. *Shipped (v0.5.0–v0.9.0).*
- **Beyond** — the observability cockpit ([spec 16](../specs/16-logs-and-dashboard.md)),
  and the two big conceptual additions above (a projects TUI; a task/monorepo
  runner) if and when they're prioritized.

## See also

- [ROADMAP.md](../ROADMAP.md) — milestones, effort, and sequencing.
- [Templates](templates.md) — author `node.next` / `node.nest` today.
- [Lifecycle](lifecycle.md) — `logs`, `dashboard`, and the `up` saga.
- [Guide index](./README.md) — the full guide.

---

◀ [Global flags & scripting](global-flags.md) · [Guide index](./README.md) · [Guide index ▶](./README.md)
