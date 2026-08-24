# devstack reference

A condensed field and flag reference. The authoritative versions are compiled
into the binary: `devstack ai docs guide/config-reference` and
`devstack config schema`.

## workspace.yaml

| Field | Type | Notes |
|---|---|---|
| `apiVersion` | string | Required. Always `devstack/v1`. |
| `kind` | string | Required. `Workspace`. |
| `name` | string | Required. Lowercase, starts with a letter, `[a-z0-9_-]`, ≤63 chars. |
| `aliases` | list | Alternate argv[0] names. |
| `profiles.default` | string | The env overlay name. Default `dev`. Readable as `${profile}`. |
| `defaultProfile` | string | The service slice `up` activates with no `--profile`. |
| `groups` | map | Named service slices: `{services: [...], memoryHintMB: N}`. |
| `memoryBudgetMB` | int | Warn above this total. |
| `secrets.providers` | list | `{name, kind, env, projectId, region}`. |
| `network.proxy` | object | `{engine: caddy\|traefik\|nginx, httpsLocal: bool}`. |
| `network.tunnel` | object | `{provider, hostname}`. |
| `backend` | object | `{context}` XOR `{host}`. Omit for the local daemon. |
| `shared` | map | `<name>: {template, params, resources, platform}`. Template must declare `provides:`. |
| `projects` | list | `{name, path, git}`. |
| `hooks` | object | See hooks below. |

## devstack.yaml

| Field | Type | Notes |
|---|---|---|
| `apiVersion` / `kind` / `name` | string | Required. `kind: Project`. |
| `services` | map | **Required.** `<name>: {…}` — see below. |
| `resources` | list | `{uses, kind, name, engine, params, credentials}`. |
| `tasks` | map | `<name>: {command, run, service, deps, workdir, env, watch}`. |
| `hooks` | object | See hooks below. |

### services.\<name\>

| Field | Type | Notes |
|---|---|---|
| `template` | string | **Required.** e.g. `node.next`. |
| `params` | map | Template parameters. |
| `uses` | list | `workspace.shared.<name>` entries. |
| `env.raw` / `env.prefixed` | map | Literal vars, with `${...}` interpolation. |
| `env.import` | list | `{from, vars}` — pull exported attrs from another service. |
| `ports` | map | `{http: 3000}` — in-container ports. |
| `profiles` | list | Compose profile tags. |
| `memoryMB` | int | Shorthand for `resources.memoryMB`. |
| `resources` | object | `{cpus, memoryMB, memoryReserveMB, pidsLimit}`. |
| `platform` | string | `linux/amd64`, `linux/arm64/v8`. |
| `healthcheck` | object | `{kind, port, path, expectStatus, host, command, user, db, auth, interval, timeout, retries, startPeriod}`. `kind` ∈ tcp, http, https, exec, pg_isready, redis. |
| `dependsOn` | list | `{service, condition}` — condition ∈ healthy (default), started. |

### hooks.\<phase\>

Phases: `preUp`, `firstRun`, `postUp`, `postPull`, `preDown`. Each is a list of
`{name, run, service, command, workdir, env, timeout, retries, onFailure, once}`.
`run` ∈ host, exec (`service` required for exec). `command` is an argv array,
never shell-split. `onFailure` ∈ abort, warn, continue.

Hook and task lists **replace** on overlay merge unless the YAML opts into
`$merge: append`.

## Interpolation grammar

| Form | Resolves to |
|---|---|
| `${profile}` | The active profile name. |
| `${workspace.name}` | The workspace name. |
| `${env.NAME}` | A host environment variable. **Hard error if unset.** |
| `${self.<attr>}` | An attribute of the service being rendered. |
| `${ref:workspace.shared.<name>[.<attr>]}` | A shared service's attribute. |
| `${ref:workspace.<project>.<service>[.<attr>]}` | Another service's attribute. |
| `$$` | A literal `$`. |

Note the asymmetry: `env.` and `self.` use a **dot**; `ref:` uses a **colon**.

Secrets are referenced as `secret://<provider>/<path>#<key>`. Values are never
written to a generated file — the compose file lists the variable name with no
value and devstack supplies it through the process environment.

## Global flags

| Flag | Effect |
|---|---|
| `--json` | Machine-readable output on stdout. |
| `--quiet` | Suppress human output; errors still go to stderr. |
| `--verbose` / `--debug` | Info / debug logging on stderr (`--debug` adds source positions). |
| `--as <name>` | Pre-parsed argv[0] override. |

`--check` (on `generate`, `ide`, `ws status`) reports drift and exits non-zero
without writing — the CI form.

`--yes` is **required** for destructive verbs under `--json`: `workspace destroy`,
`uninstall`, `db drop`/`reset`/`restore`/`gc`, `resource rm`/`gc`, `s3 rb`,
`queue`/`topic`/`stream rm`.
