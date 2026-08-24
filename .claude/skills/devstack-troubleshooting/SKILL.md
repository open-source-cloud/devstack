---
name: devstack-troubleshooting
description: >-
  Use when a devstack command fails or a service is unhealthy — the Docker
  daemon is unreachable, the devstack_shared network is missing, a host port
  is in use, generate --check reports drift, config errors point at
  file:line:col, secret:// resolution fails, shared-service ref counts look
  wrong, or file watching misbehaves on WSL2. Maps each symptom to the
  diagnostic command and the fix.
license: Apache-2.0
allowed-tools: Bash(devstack:*)
---

Commands below are written as `devstack`. If this machine installed an alias
(`rq`, `uranus`), substitute it.

## Start here

```bash
devstack doctor          # the host preflight matrix: docker, compose, git, ports, paths
devstack doctor --fix    # repair what is safely repairable
devstack status          # per-service health + the shared-service ref graph
devstack logs <service>  # the actual error, usually
```

Add `--debug` to any command for structured logs on stderr, including the exact
external command devstack ran and its exit code.

## Symptom → diagnose → fix

| Symptom | Diagnose | Fix |
|---|---|---|
| `Cannot connect to the Docker daemon` | `devstack doctor` | Start Docker. On WSL2 confirm which daemon you mean — Desktop and an in-distro `dockerd` are separate contexts with separate ledgers. |
| `network devstack_shared not found` | `docker network ls` | `devstack up` recreates it. Never `docker network rm` it yourself. |
| A host port is already in use | `devstack ports` | `devstack expose --off`, or let devstack allocate a different port. On Windows, an excluded port range can also be the cause. |
| `generate --check` reports drift | `devstack generate --check` | Run `devstack generate`. If drift returns immediately, something is editing `.devstack/` by hand. |
| A config error with `file:line:col` | `devstack config validate` | Read the position — it points at the exact YAML node. `devstack config schema` gives the full field contract. |
| `unknown interpolation ${...}` | — | `${ref:...}` takes a **colon**; `${env.NAME}` and `${self.attr}` take a **dot**. |
| A shared service will not start | `devstack logs shared-<engine>` | Often a volume from an older major version. Check the template's `params.version`. |
| A service cannot reach Postgres | `devstack status` | Connect to the alias `shared-postgres`, not `localhost` and not the bare service name. Both containers must be on the shared network. |
| Ref counts look wrong | `devstack shared status` | `devstack shared doctor` reconciles from live containers; `devstack shared gc` releases orphans. |
| A secret will not resolve | `devstack secrets status` | Confirm the provider is declared in `workspace.yaml` and that you are logged in (`devstack secrets login <provider>`). |
| A template change has no effect | `devstack template lint <dir> --show` | A `[[ ]]` action in a metadata key is a hard lint error — only `service:` and `volumes:` are rendered. |
| A parent template's list disappeared | — | Deep-merge **replaces** lists. Use `$merge: append`. |
| File watching does not fire on WSL2 | — | The app templates set polling env vars for this. Confirm the repo is on the Linux filesystem — `/mnt/*` working directories are refused. |
| Everything is confusing | `devstack doctor --json` | Escalate: `doctor --fix` → `shared doctor` → `shared gc` → as a last resort `workspace destroy` (destructive, needs `--yes`). |

## Never do these

- **Do not `docker compose ...` against a devstack stack.** The project name,
  labels and external network are tool-owned; you will fork a parallel stack.
- **Do not hand-edit `.devstack/`.** It is generated output.
- **Do not `docker network rm devstack_shared`.**
- **Do not pass `--yes` to a destructive verb on the user's behalf** without
  asking. `workspace destroy`, `db drop`, `db reset`, `s3 rb` and friends are not
  reversible.
- **Do not run two mutating devstack commands concurrently.** They coordinate
  through a cross-process lock; racing them is what corrupts ref counts.

## Reading further

```bash
devstack ai docs guide/recovery      # doctor --fix, gc, teardown
devstack ai docs troubleshooting     # the top-level troubleshooting page
devstack ai docs --search "<error text>"
```
