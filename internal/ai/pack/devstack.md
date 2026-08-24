Commands below are written as `devstack`. If this machine installed an alias
(`rq`, `uranus`), substitute it — the command tree is identical.

## What devstack is

devstack runs Docker development environments where **infrastructure is shared
across projects**. One warm Postgres, one Redis, one MinIO on a tool-owned Docker
network serve every repo in the workspace, and each project still gets its own
database, role and bucket. That is the whole point: eight microservices, one
Postgres container, eight isolated databases.

Two committed files describe everything:

- **`workspace.yaml`** at the workspace root — what infrastructure is *provided*,
  and which repos belong to the workspace.
- **`devstack.yaml`**, one per repo — what that project *consumes*.

Everything else is generated. `devstack up` renders the compose files, starts the
shared engines once, provisions each project's isolated data, and brings the
project stacks up on the shared network.

## Orient before acting

In an unfamiliar workspace, run these first. They are read-only and fast.

```bash
devstack context             # active workspace, project, docker context
devstack status              # service health + the shared-service ref graph
devstack config show --json  # the resolved configuration
devstack ai commands         # every command this binary has
```

If `devstack config validate` reports an error it will be a `file:line:col`
pointing at the exact node. Read it; do not guess.

## The rules that matter

Violating any of these produces a broken or confusing workspace, and most are not
guessable from Docker experience alone.

1. **Never hand-edit anything under `.devstack/`.** Those compose files and
   Dockerfiles are generated and are overwritten on the next `generate` or `up`.
   Change `workspace.yaml` / `devstack.yaml` and regenerate.
2. **Never run `docker compose` against a devstack stack.** The project name,
   labels and the external network are tool-owned; running compose directly forks
   a second, parallel stack that shares nothing with the workspace.
3. **Never `docker network rm devstack_shared`.** Compose refuses to manage
   `external: true` networks, so devstack owns creating and removing it.
4. **Shared services are reached by DNS alias** — `shared-postgres`,
   `shared-redis`, `shared-minio` — never by bare service name. By default
   nothing publishes a host port; run `devstack expose` when a GUI client on the
   host needs one.
5. **Let devstack perform mutations.** Anything that changes the ledger or the
   shared stack takes a cross-process lock. Do not run two `up`s concurrently.
6. **Destructive verbs need `--yes` under `--json`** (`workspace destroy`,
   `uninstall`, `db drop`/`reset`/`restore`/`gc`, `resource rm`/`gc`, `s3 rb`,
   `queue`/`topic`/`stream rm`). Never pass `--yes` on the user's behalf without
   asking them first.
7. **`secret://` values never land in a generated file.** A secret reaches a
   container because the compose file lists the variable name with no value and
   devstack passes the value through the process environment. Do not try to
   inline a resolved secret.
8. **`${ref:...}` uses a colon. `${env.NAME}` and `${self.attr}` use a dot.**
   This asymmetry is the single most common config typo.
9. **Deep-merge replaces lists by default.** Opt into appending with
   `$merge: append`.

## The commands that cover most work

```bash
devstack up [project...]     # network → shared engines → provision → compose up
devstack down [project...]   # stop this workspace's stacks, keep the data
devstack status              # health + ref graph
devstack logs [service...]   # streamed across project and shared stacks
devstack shell [service]     # a shell inside a service container
devstack run <task...>       # the project's tasks: graph, dependency-ordered
devstack generate --check    # is anything stale? (exit non-zero if so)
```

Every headline command supports `--json` and `--quiet`. Use `--json` when you
need to parse the result.

## Common tasks

| Goal | Do this |
|---|---|
| Add a repo to the workspace | `devstack project new <name> --path <dir>` |
| Add a service to a repo | add an entry under `services:` in its `devstack.yaml`, then `devstack generate` |
| Add a shared engine | add an entry under `shared:` in `workspace.yaml`, then `devstack up` |
| Give a project a database | declare it under `resources:`, or `devstack db create <name>` |
| Reach a shared engine from the host | `devstack expose` then `devstack ports` |
| Set an env var on a service | `devstack env set KEY=VALUE --service <svc>` |
| See why a service is unhealthy | `devstack status`, then `devstack logs <service>` |
| Check the host is set up | `devstack doctor` (add `--fix` to repair) |

## Reading further

The whole documentation corpus is compiled into the binary. Do not guess at
behavior — read it:

```bash
devstack ai docs                       # list every document
devstack ai docs guide/templates       # authoring service templates
devstack ai docs guide/config-reference # every config field and grammar
devstack ai docs guide/lifecycle       # up / down / status / logs / shell
devstack ai docs guide/shared-services # the shared engines and host access
devstack ai docs guide/databases       # the db group
devstack ai docs guide/secrets         # secret:// and .env ingestion
devstack ai docs --search "<question>" # search titles and bodies
```

`reference.md` next to this file is a condensed config and flag reference.
