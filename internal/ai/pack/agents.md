## devstack

This repository uses [devstack](https://github.com/open-source-cloud/devstack) to
run its local development environment. devstack shares infrastructure across
projects: one warm Postgres/Redis/MinIO on a tool-owned Docker network serves
every repo in the workspace, and each project gets its own database, role and
bucket.

Two committed files describe everything — `workspace.yaml` at the workspace root
(what infrastructure is provided) and one `devstack.yaml` per repo (what that
project consumes). **Everything under `.devstack/` is generated output.**

### Commands

```bash
devstack up          # start: network → shared engines → provision → compose up
devstack down        # stop this workspace's stacks (data is preserved)
devstack status      # service health and the shared-service ref graph
devstack logs [svc]  # streamed logs across project and shared stacks
devstack shell [svc] # a shell inside a service container
devstack run <task>  # the project's tasks: graph, dependency-ordered
devstack generate    # re-render compose + build artifacts after a config change
devstack doctor      # check the host is set up correctly
```

Every headline command supports `--json` and `--quiet`.

### Rules

1. **Never hand-edit anything under `.devstack/`** — it is generated and will be
   overwritten. Edit `workspace.yaml` or `devstack.yaml` and run
   `devstack generate`.
2. **Never run `docker compose` against these stacks**, and never
   `docker network rm devstack_shared`. The project name, labels and the external
   network are tool-owned; using compose directly forks a parallel stack.
3. **Shared services are reached by DNS alias** (`shared-postgres`,
   `shared-redis`), never by bare service name, and publish no host port by
   default. Use `devstack expose` when a host client needs one.
4. **Destructive verbs require `--yes` under `--json`.** Do not pass `--yes` on
   the user's behalf without asking.
5. **`secret://` values never belong in a committed or generated file.**
6. **`${ref:...}` uses a colon; `${env.NAME}` and `${self.attr}` use a dot.**

### Learning more

devstack documents itself — the whole corpus is compiled into the binary:

```bash
devstack ai docs                        # list every document
devstack ai docs guide/templates        # how to author a service template
devstack ai docs guide/config-reference # every config field and grammar
devstack ai docs --search "<question>"  # search titles and bodies
devstack ai commands --json             # the command surface as data
devstack config schema                  # the JSON Schema for devstack.yaml
```
