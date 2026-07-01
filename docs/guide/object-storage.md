# Object storage (S3 / MinIO)

[devstack](../../README.md) · [Guide](./README.md) › Object storage (S3/MinIO)

The `s3` command group manages **tenant-scoped buckets** on the shared MinIO
engine (S3-compatible). Like databases, buckets are namespaced per project so
many repos share one MinIO without colliding.

```bash
devstack s3 mb uploads               # → bucket "acme-uploads" (project "acme")
devstack s3 ls                       # this project's buckets
devstack aws -- s3 ls s3://acme-uploads   # host-side, via the aws CLI shim
```

## Bucket naming: the `<project>-` prefix

By default a bucket's physical name is **`<project>-<name>`** (a hyphen, unlike
Postgres which uses `_`). So project `acme` + `uploads` → `acme-uploads`. Pass
`--no-prefix` to use the literal name.

## `s3 mb <bucket>` — make a bucket

```bash
devstack s3 mb uploads                     # → acme-uploads
devstack s3 mb media --versioning          # keep every object version
devstack s3 mb shared-assets --no-prefix   # literal name
```

Idempotent — re-running is a no-op.

| Flag           | Default              | Description |
|----------------|----------------------|-------------|
| `--project`    | single/first project | owning project |
| `--versioning` | false                | enable object versioning on the new bucket |
| `--no-prefix`  | false                | use the literal name (skip the `<project>-` prefix) |

## `s3 rb <bucket>` — remove a bucket

```bash
devstack s3 rb uploads --yes
devstack s3 rb media --force --yes         # delete even if non-empty
```

Destructive; confirmation required (`--yes` mandatory under `--json`).

| Flag          | Default              | Description |
|---------------|----------------------|-------------|
| `--project`   | single/first project | owning project |
| `--force`     | false                | remove even if the bucket is non-empty |
| `--yes`       | false                | confirm (required for `--json`) |
| `--no-prefix` | false                | treat `<bucket>` as literal |

## `s3 ls` — list buckets

```bash
devstack s3 ls              # just this project's buckets
devstack s3 ls --all        # every bucket on the shared MinIO
```

Lock-free (read-only).

| Flag        | Default              | Description |
|-------------|----------------------|-------------|
| `--project` | single/first project | scope to a project |
| `--all`     | false                | list every bucket, not just this project's |

## Lifecycle rules: `s3 lifecycle`

Expire (and optionally transition) objects automatically.

```bash
devstack s3 lifecycle set uploads --expire-days 30
devstack s3 lifecycle set uploads --prefix tmp/ --expire-days 7
devstack s3 lifecycle set media  --transition "days=30,tier=STANDARD_IA"
devstack s3 lifecycle get uploads
devstack s3 lifecycle rm  uploads
```

`s3 lifecycle set` flags:

| Flag            | Default              | Description |
|-----------------|----------------------|-------------|
| `--project`     | single/first project | owning project |
| `--expire-days` | 0                    | delete objects after N days |
| `--transition`  | ""                   | transition rule, e.g. `days=30,tier=STANDARD_IA` |
| `--prefix`      | ""                   | scope the rule to a key prefix |

`s3 lifecycle get` prints the current rules (lock-free); `s3 lifecycle rm`
removes the whole lifecycle configuration (takes `--project`).

## Versioning: `s3 versioning`

```bash
devstack s3 versioning uploads on
devstack s3 versioning uploads off      # suspend
```

| Flag        | Default              | Description |
|-------------|----------------------|-------------|
| `--project` | single/first project | owning project |

## Bucket policy: `s3 policy`

```bash
devstack s3 policy set assets --public-read       # anonymous read
devstack s3 policy set assets --file policy.json  # a full policy document
devstack s3 policy get assets                     # print current policy JSON
```

`s3 policy set` flags:

| Flag            | Default              | Description |
|-----------------|----------------------|-------------|
| `--project`     | single/first project | owning project |
| `--public-read` | false                | apply a canned public-read policy |
| `--file`        | ""                   | path to a policy JSON document |

## CORS: `s3 cors`

```bash
devstack s3 cors set uploads --file cors.json     # array of CORS rules
devstack s3 cors get uploads
```

| Flag        | Default              | Description |
|-------------|----------------------|-------------|
| `--project` | single/first project | owning project |
| `--file`    | ""                   | JSON file: an array of CORS rules |

## Host-side access: `devstack aws`

The `s3` group covers provisioning. To actually **put and get objects from your
host**, use the `aws` shim — it runs the host `aws` CLI against the local
LocalStack/MinIO endpoint, injecting the endpoint URL, region and dev credentials
for you:

```bash
devstack aws -- s3 ls
devstack aws -- s3 cp ./photo.png s3://acme-uploads/photo.png
devstack aws -- s3 ls s3://acme-uploads
```

Everything after `--` is passed straight to `aws`. (For DNS-only in-container
access, apps reach MinIO at `shared-minio:9000`; to point a desktop S3 browser at
it, run `devstack shared expose` first — see [shared-services.md](shared-services.md).)

## Recipes

**An uploads bucket for the app.**

```bash
devstack s3 mb uploads
devstack aws -- s3 cp ./seed/logo.png s3://acme-uploads/logo.png
```

**Auto-expire temp objects after 7 days.**

```bash
devstack s3 lifecycle set uploads --prefix tmp/ --expire-days 7
devstack s3 lifecycle get uploads
```

**A public-read assets bucket.**

```bash
devstack s3 mb assets
devstack s3 policy set assets --public-read
```

**Keep every version of an important bucket.**

```bash
devstack s3 mb archive --versioning
# or, on an existing bucket:
devstack s3 versioning archive on
```

## Declarative alternative

You can also declare buckets in `devstack.yaml` under `resources:` so they're
provisioned as part of `up`, instead of running `s3 mb` by hand — see
[the resource layer](resources.md).

## See also

- [Shared services & host access](shared-services.md) — expose MinIO to a desktop S3 browser.
- [Databases](databases.md) — the parallel `db` group on Postgres.
- [Messaging](messaging.md) — queues/topics/streams (incl. SQS/SNS on LocalStack).
- [The resource layer](resources.md) — declare buckets in config.
- [Command reference](command-reference.md) — every command, terse.

---

◀ [Databases (Postgres)](databases.md) · [Guide index](./README.md) · [Messaging](messaging.md) ▶
