# The resource layer

[devstack](../../README.md) · [Guide](./README.md) › The resource layer

The `db`, `s3`, `queue`, `topic`, and `stream` groups are ergonomic front-ends
over one engine-agnostic **resource layer**: per-project databases, buckets,
queues, and streams that live *inside* a shared engine and are tracked in an
ownership ledger. This page covers the layer directly — the imperative `resource`
group and the declarative `resources:` block — both of which call the **same
provisioners and the same ledger under the same lock**.

```bash
devstack resource list                                   # what have I provisioned?
devstack resource create postgres database analytics     # ad-hoc create
devstack resource show analytics --show-secrets          # connection attributes
```

## Two paths, one engine

| Path | When it runs | Best for |
|---|---|---|
| **Declarative** — a `resources:` block in `devstack.yaml` | provisioned automatically at `up` | the data plane your app always needs |
| **Imperative** — `devstack resource …` | on demand | one-off scratch resources, inspection, cleanup |

Because both routes reach the same provisioner and ownership ledger, a resource
you declare and one you create by hand are indistinguishable afterwards — they
show up together in `resource list` and are reclaimed by the same `gc`.

---

## The declarative path: `resources:` in `devstack.yaml`

Declare the databases, buckets, queues, streams, and topics a project owns.
`devstack up` provisions them (idempotently) after the shared engines are healthy.

```yaml
# devstack.yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx
    uses:
      - workspace.shared.postgres
      - workspace.shared.minio
resources:
  - uses: workspace.shared.postgres     # required — which shared engine
    kind: database                      # database|user|bucket|lifecycle|queue|stream|topic
    name: analytics                     # default: the project name
  - uses: workspace.shared.postgres
    kind: user
    name: reporter
    credentials: generated              # predictable | generated
  - uses: workspace.shared.minio
    kind: bucket
    name: uploads
    params: { versioning: true }
  - uses: workspace.shared.nats
    kind: queue
    name: jobs
```

### `resources[]` fields

| Field | Required | Notes |
|---|---|---|
| `uses` | yes | the shared engine: `workspace.shared.<name>` |
| `kind` | yes | `database` \| `user` \| `bucket` \| `lifecycle` \| `queue` \| `stream` \| `topic` |
| `name` | no | defaults to the project name |
| `engine` | no | inferred from `uses` |
| `params` | no | engine/kind-specific settings (e.g. `versioning`, `partitions`) |
| `credentials` | no | `predictable` (default) or `generated` |

No two declarations may collide on `(engine, kind, name)` within a project.

---

## The imperative path: the `resource` group

Manage per-project resources across every engine from one group.

### `resource list`

List provisioned resources from the ownership ledger (lock-free read).

```bash
devstack resource list
devstack resource list --engine postgres --kind database
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | filter to one project |
| `--engine` | "" | filter by engine |
| `--kind` | "" | filter by kind |

### `resource show <name>`

Show a resource's connection attributes. Secrets are masked unless
`--show-secrets`.

```bash
devstack resource show analytics
devstack resource show reporter --show-secrets
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | owning project |
| `--engine` | "" | disambiguate when the name exists on several engines |
| `--show-secrets` | false | reveal masked credential values |

### `resource create <engine> <kind> <name>`

Create a resource on a **running** shared engine (idempotent). Takes exactly
three positional args.

```bash
devstack resource create postgres database analytics
devstack resource create minio bucket uploads --param versioning=true
devstack resource create postgres user reporter --credentials generated
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | owning project (default: single/first project) |
| `--param` | none | `k=v`, repeatable — engine/kind-specific settings |
| `--credentials` | `predictable` | `predictable` \| `generated` |

### `resource rm <name>`

Un-track a resource, or `--purge-data` to also drop the underlying object
(destructive).

```bash
devstack resource rm analytics                 # untrack only
devstack resource rm analytics --purge-data --yes   # also DROP the data
```

| Flag | Default | Description |
|---|---|---|
| `--project` | "" | owning project |
| `--engine` | "" | disambiguate by engine |
| `--purge-data` | false | also DROP the underlying object (destructive) |
| `--yes` | false | required with `--json`/non-interactive |

### `resource gc`

Reclaim resources whose owner project has left the workspace (destructive).

```bash
devstack resource gc            # prompts
devstack resource gc --yes      # required with --json
```

| Flag | Default | Description |
|---|---|---|
| `--yes` | false | required for `--json` / non-interactive |

---

## Predictable vs generated credentials

The `--credentials` flag (imperative) and the `credentials:` field (declarative)
choose how a resource's login is derived:

| Mode | How the secret is set | Use it when |
|---|---|---|
| `predictable` (default) | derived deterministically (e.g. the role name), stable across machines | local dev, reproducible seeds, sharing a `devstack.yaml` with a team |
| `generated` | random via `crypto/rand`, stored in the ledger | you want a non-guessable credential; read it back with `resource show --show-secrets` |

Predictable is the default precisely because devstack is a **local** dev tool —
teammates who run the same config get the same connection string with no
coordination. Reach for `generated` when a resource should not have a guessable
password.

---

## Recipes

**Declare a database + a bucket a project always needs**

```yaml
# devstack.yaml
resources:
  - { uses: workspace.shared.postgres, kind: database, name: analytics }
  - { uses: workspace.shared.minio,    kind: bucket,   name: uploads }
```

```bash
devstack up          # both provisioned once engines are healthy
```

**Ad-hoc scratch database for an experiment**

```bash
devstack resource create postgres database scratch
devstack resource show scratch --show-secrets
devstack resource rm scratch --purge-data --yes    # clean up when done
```

**Audit and reclaim orphans**

```bash
devstack resource list --json          # everything in the ledger
devstack resource gc                   # drop resources whose project left
```

## See also

- [Databases](databases.md) — the `db` front-end (roles, grants, tenant DBs)
- [Object storage](object-storage.md) — the `s3` front-end (buckets, policies)
- [Messaging](messaging.md) — the `queue` / `topic` / `stream` front-ends
- [Projects](projects.md) — where the `resources:` block lives
- [Snapshots, restore & reset](data-lifecycle.md) — data lifecycle for these resources

---

◀ [Messaging](messaging.md) · [Guide index](./README.md) · [Snapshots, restore & reset](data-lifecycle.md) ▶
