# Secrets

[devstack](../../README.md) · [Guide](./README.md) › Secrets

Secrets are values you must **not** commit or write to disk — API keys, tokens,
DB passwords. devstack keeps them out of your generated compose files: a
`secret://` reference resolves to a **valueless environment key** in the compose
file, and the real value is injected into the container process at `up` time.

> Not every value is a secret. Plain local config (a base URL, a feature flag,
> `APP_ENV`) belongs in `env.raw` — no provider, no backend. See
> [Environment variables](environment.md). Reach for `secret://` **only** for
> actual secrets.

```yaml
# devstack.yaml — a secret reference on a service
services:
  api:
    template: php.laravel.nginx
    env:
      raw:
        STRIPE_KEY: "secret://vault/api/stripe#live.secret_key"
```

At `up`, `STRIPE_KEY` is emitted into the compose file as a **valueless** key
(`environment: [STRIPE_KEY]`) and the value is passed to the container via the
process environment. A CI test asserts no secret value ever lands in a generated
file.

## Declaring providers in `workspace.yaml`

A `secret://` URL names a provider. Providers live under `secrets.providers` in
`workspace.yaml`:

```yaml
apiVersion: devstack/v1
kind: Workspace
name: acme
secrets:
  providers:
    - { name: vault,  kind: sops }
    - { name: aws,    kind: aws-sm,    region: us-east-1 }
    - { name: params, kind: aws-ssm,   region: us-east-1 }
    - { name: infis,  kind: infisical, projectId: "5f3c...", env: dev }
```

Each provider entry:

| Field | Notes |
|---|---|
| `name` | how you address it in `secret://<name>/...` (must be a `dsname`) |
| `kind` | one of `sops` \| `aws-sm` \| `aws-ssm` \| `infisical` \| `keyring` |
| `region` | AWS region (for `aws-sm` / `aws-ssm`) |
| `env` | environment selector (Infisical) |
| `projectId` | project identifier (Infisical) |

## The `secret://` URL grammar

```
secret://<provider>/<path>#<key>?opt=val
```

- `<provider>` — a name declared under `secrets.providers`.
- `<path>` — required; its shape depends on the backend kind (below).
- `#<key>` — optional; selects a field within the fetched payload.

Per-backend path shapes:

| Kind | Example | Meaning |
|---|---|---|
| `sops` | `secret://vault/config/db.enc.yaml#postgres.password` | file path `#` dotted key inside the decrypted document |
| `aws-sm` | `secret://aws/prod/api-key#json.token` | Secrets Manager secret-id `#` JSON key |
| `aws-ssm` | `secret://params/prod/db-url` | SSM parameter name (optionally `#json.key`) |
| `infisical` | `secret://infis/DATABASE_URL` | secret NAME (optionally `#json.key`) |

Secret values are fetched **batched** at generation and never written to a
generated file.

## Offline secrets: SOPS + age (no account)

The `sops` kind works fully offline with an [age](https://age-encryption.org)
keypair — nothing to log into.

```bash
# 1. Generate a keypair and write the private key (mode 0600)
devstack secrets keygen -o ~/.config/devstack/age.key

# 2. Point SOPS at it so devstack can decrypt at generate time
export SOPS_AGE_KEY_FILE=~/.config/devstack/age.key
```

`secrets keygen` flag:

| Flag | Default | Description |
|---|---|---|
| `--output` / `-o` | "" (stdout) | write the key to this file (0600) instead of printing it |

The public recipient (`age1...`) printed by `keygen` is what you encrypt *to* —
pass it as `--recipient` to [`secrets ingest`](#ingesting-an-existing-env-file)
or use it directly with the `sops` CLI. With `SOPS_AGE_KEY_FILE` set, any
`secret://vault/<file>#<key>` reference decrypts locally.

## Provider login (keyring)

For account-backed providers (`aws-sm`, `aws-ssm`, `infisical`), store the
credential in the OS keyring:

```bash
devstack secrets login infisical --token "$INFISICAL_TOKEN"
devstack secrets status                # keyring availability + per-provider source
devstack secrets logout infisical      # remove the stored credential
```

| Command | Args | Flag | Notes |
|---|---|---|---|
| `secrets login <provider>` | provider name | `--token` (required) | stores a credential in the OS keyring |
| `secrets logout <provider>` | provider name | — | removes the stored credential |
| `secrets status [provider...]` | optional filter | — | reports keyring availability + where each credential comes from |

On **keyring-less hosts** (some headless Linux, CI), `login`/`logout` degrade to
a warning rather than failing — supply the credential another way (e.g. the
provider's own env var) and rely on `secrets status` to see the source.

## Ingesting an existing `.env` file

Already have a committed `.env`? `secrets ingest` classifies each key as a
**secret** (routed to a `secret://` provider) or **config** (inlined as an env
var), then rewrites your `devstack.yaml` — never leaving plaintext secrets on
disk.

```bash
# Preview the classification without writing anything
devstack secrets ingest .env --dry-run

# Encrypt secrets to SOPS/age, keep DB_HOST as host-derived config
devstack secrets ingest .env \
  --to sops \
  --recipient age1qz... \
  --from-host DB_HOST \
  --secret 'STRIPE_*' \
  --service api
```

`secrets ingest [path]` (default path `.env`) flags:

| Flag | Default | Description |
|---|---|---|
| `--to` | sops | destination backend for secrets: `sops` \| `aws-sm` \| `infisical` |
| `--dest` | "" | explicit destination path/id within the backend |
| `--service` | "" | target service (required if the project has more than one) |
| `--recipient` | "" | age recipient (`age1...`) to encrypt SOPS secrets to |
| `--secret` | (stringArray) | force-classify matching keys as secrets (glob, repeatable) |
| `--public` | (stringArray) | force-classify matching keys as config (glob, repeatable) |
| `--from-host` | (stringArray) | emit matching config keys as `${env.KEY}` (glob, repeatable) |
| `--prefixed` | false | route secrets to `env.prefixed` instead of `env.raw` |
| `--dry-run` | false | show the plan, write nothing |
| `--yes` | false | skip the interactive wizard |
| `--keep-env` | false | keep the original `.env` file |
| `--force` | false | overwrite committed files (backed up first) |

Classification rules of thumb:

- `--from-host KEY` → the value stays on the host; the compose file references it
  as `${env.KEY}` (config, not a secret). Good for host-specific values like
  `DB_HOST`.
- `--public 'FEATURE_*'` → inlined verbatim into `env.raw` (plain config).
- `--secret 'STRIPE_*'` → encrypted to the `--to` backend and referenced via
  `secret://`.

Run interactively (a TTY, no `--yes`) and `ingest` opens a wizard to classify
each key; add `--yes` for a non-interactive, flags-only run.

## The valueless-env-key coupling

This is the core safety property, worth stating plainly: a secret reaches a
container **only because the compose file references its name**. devstack emits
each secret as a **valueless** `environment: [NAME]` key per service, and passes
the actual value through the child process environment at `up`. Therefore:

- No secret value is ever written into `.devstack/`-generated compose files.
- The reference (`secret://...`) lives in your committed `devstack.yaml`; the
  value lives only in the provider.

See [Environment variables](environment.md) for how `env.raw` interpolation and
secret references share the same env block, and the exact resolution order.

## Recipes

**Offline, zero-account setup (SOPS + age)**

```bash
devstack secrets keygen -o ~/.config/devstack/age.key
export SOPS_AGE_KEY_FILE=~/.config/devstack/age.key
# add provider `vault` (kind: sops) to workspace.yaml, then reference:
#   env.raw.DB_PASSWORD: "secret://vault/db.enc.yaml#password"
```

**Cloud-backed secret (AWS Secrets Manager)**

```yaml
# workspace.yaml
secrets:
  providers:
    - { name: aws, kind: aws-sm, region: us-east-1 }
```
```yaml
# devstack.yaml
env:
  raw:
    API_TOKEN: "secret://aws/prod/api-token#json.token"
```

**Migrate a legacy `.env`**

```bash
devstack secrets ingest .env --to sops --recipient age1... \
  --from-host DB_HOST --public 'APP_*' --secret 'STRIPE_*,JWT_SECRET' \
  --service api
```

## See also

- [Environment variables](environment.md) — plain local config with `env.raw` (use this for NON-secret values).
- [Databases](databases.md) — provisioned Postgres roles and generated credentials.
- [config-reference.md](config-reference.md) — the `secrets.providers` schema and `secret://` grammar.
- [command-reference.md](command-reference.md) — every `secrets` verb, terse.

---

◀ [Multi-repo git](multi-repo-git.md) · [Guide index](./README.md) · [Local HTTPS & DNS ▶](networking.md)
