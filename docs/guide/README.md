# devstack usage guide

[devstack](../../README.md) › Guide

devstack is a single static Go binary that manages Docker-based dev environments
and **shares infrastructure across projects** — one warm Postgres/Redis/MinIO
(and Kafka/NATS/RabbitMQ/LocalStack) on a tool-owned network for many repos,
instead of a duplicate stack per repo. This guide is the task-oriented companion
to the specs: examples first, real flags and fields only. Work through it
top-to-bottom, or jump to the page you need.

> **New here?** Read them in this order:
> [Concepts](concepts.md) → [Installation](installation.md) →
> [Workspaces](workspaces.md) → [Projects](projects.md) →
> [Lifecycle](lifecycle.md). That's the shortest path from zero to a running
> workspace.

## Contents

### Getting started

| # | Page | What it covers |
|---|---|---|
| 1 | [Concepts & the mental model](concepts.md) | The four nouns, the two-file model, the shared network, no daemon, the `up` saga. |
| 2 | [Installation, doctor & updates](installation.md) | Install the binary, verify your host with `doctor`, `self update`. |

### Setup

| # | Page | What it covers |
|---|---|---|
| 3 | [Workspaces](workspaces.md) | `workspace.yaml` and `init`. |
| 4 | [Projects](projects.md) | `devstack.yaml` and its services. |
| 5 | [Environment variables](environment.md) | Local key-value env vars with no external store (`env.raw`/`prefixed`/`import`). |
| 6 | [Templates](templates.md) | Using and authoring service templates. |

### Operating

| # | Page | What it covers |
|---|---|---|
| 7 | [Lifecycle](lifecycle.md) | `up`, `down`, `status`, `shell`, `logs`, `dashboard`. |
| 8 | [Shared services & host access](shared-services.md) | The shared engines, `shared expose`, `shared ports`. |

### Data plane

| # | Page | What it covers |
|---|---|---|
| 9 | [Databases (Postgres)](databases.md) | The `db` group — tenant databases, roles, grants. |
| 10 | [Object storage (S3/MinIO)](object-storage.md) | The `s3` group — tenant buckets, lifecycle, policy, CORS. |
| 11 | [Messaging](messaging.md) | `queue`, `topic`, `stream` on NATS/Kafka/Redis/LocalStack. |
| 12 | [The resource layer](resources.md) | The `resource` group + declarative `resources:`. |
| 13 | [Snapshots, restore & reset](data-lifecycle.md) | `db snapshot`/`restore`/`reset`/`pull`. |

### Multi-repo

| # | Page | What it covers |
|---|---|---|
| 14 | [Multi-repo git](multi-repo-git.md) | The `ws` group — clone, sync, status across repos. |

### Platform

| # | Page | What it covers |
|---|---|---|
| 15 | [Secrets](secrets.md) | The `secrets` group, `secret://`, and `.env` ingest. |
| 16 | [Local HTTPS & DNS](networking.md) | `dns`, `trust`, the local proxy. |
| 17 | [Public tunnels](tunnels.md) | The `tunnel` group (cloudflared). |

### Maintenance

| # | Page | What it covers |
|---|---|---|
| 18 | [Recovery, teardown & housekeeping](recovery.md) | `doctor --fix`, `shared doctor`, `gc`, `workspace destroy`, `uninstall`. |
| 19 | [The global store (~/.devstack)](store.md) | The `store` group — config, templates, shared, snapshots. |
| 20 | [Aliases & argv[0] dispatch](aliases.md) | The `alias` group and `--as`. |

### Reference

| # | Page | What it covers |
|---|---|---|
| 21 | [Full config reference](config-reference.md) | Every `workspace.yaml` / `devstack.yaml` field + the grammars. |
| 22 | [Command reference](command-reference.md) | Every command, terse, grouped by area. |
| 23 | [Global flags & scripting](global-flags.md) | `--json`/`--quiet`/`--debug`/`--verbose`, the `--yes` rule, `--check`, CI recipes. |

### Roadmap

| # | Page | What it covers |
|---|---|---|
| 24 | [What's next: roadmap & current gaps](whats-next.md) | What ships today vs. what's on the roadmap or still a gap. |

---

◀ [devstack index](../../README.md) · Guide index · [Concepts & the mental model ▶](concepts.md)
