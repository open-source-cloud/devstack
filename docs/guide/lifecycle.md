# Lifecycle

[devstack](../../README.md) · [Guide](./README.md) › Lifecycle

The day-to-day loop: bring the workspace **up**, watch it with **status** /
**logs** / **dashboard**, drop into a **shell**, and take it **down**. These are
the commands you'll run dozens of times a day.

```bash
devstack up            # network + shared infra + generate + compose up + hooks
devstack status        # health + last saga outcome + shared ref graph
devstack logs -f       # tail everything
devstack down          # stop this workspace's stacks, release refs
```

---

## `up [project...]`

Brings the workspace up. It's an **idempotent saga**: each phase records state, so
a re-run skips already-satisfied work and a crash mid-run **resumes** from where
it stopped. On failure, mutating phases (ref counts, containers) are
**compensated/rolled back**, but data (volumes, databases) is never destroyed.

```bash
devstack up               # everything (default profile or all services)
devstack up api           # just the api project (+ shared infra it needs)
devstack up api web       # two projects
```

The saga phases, in order:

1. **Preflight** — Docker/compose/git capability checks.
2. **Clone/sync** — ensure workspace repos are on disk.
3. **Network** — ensure the external `devstack_shared` bridge network.
4. **Shared infra** — start the shared engines and wait for health.
5. **Generate** — render compose + build artifacts (deterministic).
6. **Provision** — per-project Postgres role/db.
7. **Compose up** — start the project stacks.
8. **Hooks** — run `firstRun`/`postUp` lifecycle hooks.

Flags:

| Flag | Default | Description |
|---|---|---|
| `--build` | false | build images before starting (`compose build`) |
| `--rebuild` | false | force a no-cache rebuild before starting (`compose build --no-cache`) |
| `--skip-clone` | false | skip the clone/sync phase (repos already on disk) |
| `--health-timeout` (duration) | 0 | per-shared-service readiness deadline (0 → spec-10 default) |
| `--no-hooks` | false | skip lifecycle hooks |
| `--no-preflight` | false | skip the preflight checks |
| `--no-provision` | false | skip per-project Postgres role/db provisioning |
| `--profile` / `-p` (repeatable) | nil | service slice(s) to start — comma-separated & repeatable; empty → `defaultProfile` or all |

`--profile` selects **which services** start and also sets the `${profile}`
env-overlay value used by [environment variables](environment.md).

---

## `down [project...]`

Stops this workspace's project stacks and releases their shared-service refs.

```bash
devstack down          # all projects in the workspace
devstack down api      # just the api project
```

It **never removes volumes**, and shared services keep running (other workspaces
may still reference them — reclaim them with `shared gc`, see
[Shared services](shared-services.md)). No local flags.

---

## `status`

Read-only snapshot: per-service health, the last saga outcome, and the
shared-service reference graph (which projects hold each engine).

```bash
devstack status
devstack status --json   # machine-readable
```

No local flags.

---

## `shell [service] [-- cmd...]`

Exec into a running service container. With one service in scope it's picked
automatically; with several, name it. Requires a TTY and propagates the
container's exit code.

```bash
devstack shell                       # login shell in the only/default service
devstack shell api                   # login shell in the api service
devstack shell api -- php artisan tinker   # run one command, then exit
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | project whose stack to exec into (default: the only project, else required) |

With no `-- cmd`, you get an interactive login shell (`bash`, falling back to
`sh`). With `-- <cmd...>`, it runs that one command.

---

## `logs [service...]`

Streams container logs across the workspace's project **and** shared stacks,
color-keyed by service. This is a **real, wired command** backed by the read-only
Engine SDK (not a stub). `--json` emits one `{ts,service,project,stream,container,line}`
object per line.

```bash
devstack logs                # last 200 lines per service, then exit
devstack logs -f             # follow (stream until Ctrl-C)
devstack logs api web        # only these services
devstack logs -f --since 10m # follow, starting 10 minutes back
```

| Flag | Default | Description |
|---|---|---|
| `--follow` / `-f` | false | follow log output (stream until interrupted) |
| `--tail` (int) | 200 | trailing lines per service (`<=0` = all) |
| `--since` | "" | logs since a duration (e.g. `10m`) or a timestamp |
| `--timestamps` | false | prefix each line with the container timestamp |
| `--no-color` | false | disable the color-keyed gutter |

---

## `dashboard`

A live TUI cockpit: shared + project services, health, and a log tail. Also real
and read-only (Engine SDK).

```bash
devstack dashboard             # interactive TUI
devstack dashboard --no-stats  # skip per-container CPU/mem sampling
```

| Flag | Default | Description |
|---|---|---|
| `--no-stats` | false | disable per-container CPU/mem stats fetch |

In a non-TTY context (or with `--json`/`--quiet`) it degrades to a one-shot
snapshot, so it's still usable in scripts.

> `logs` and `dashboard` work today. The fuller log/dashboard **cockpit** (richer
> filtering, saved views) is still evolving — see [What's next](whats-next.md).

---

## Recipes

### Bring up a single project

```bash
devstack up api
```

Starts `api` plus any shared engines it `uses`, leaving unrelated projects
untouched.

### Rebuild images after a Dockerfile change

```bash
devstack up --rebuild        # no-cache rebuild, then start
devstack up api --build      # ordinary build for just one project
```

### Switch profiles (dev vs test)

```bash
devstack up -p test          # start the test slice; ${profile}=test
devstack up -p core,frontend # start two slices at once
```

### Run a one-off command in a container

```bash
devstack shell api -- php artisan migrate
devstack shell -- npm run lint
```

### Tail logs for two services

```bash
devstack logs -f --tail 50 api worker
```

### Stop for the day

```bash
devstack down                # stops stacks; volumes and shared infra survive
```

---

## See also

- [Environment variables](environment.md) — how `${profile}` and `-p` interact
- [Shared services & host access](shared-services.md) — `shared status`/`gc`, host ports
- [Multi-repo git](multi-repo-git.md) — the clone/sync phase (`ws` group)
- [Recovery & housekeeping](recovery.md) — `doctor --fix`, teardown, `shared gc`
- [Command reference](command-reference.md) — every lifecycle flag

---

◀ [Templates](templates.md) · [Guide index](./README.md) · [Shared services & host access](shared-services.md) ▶
