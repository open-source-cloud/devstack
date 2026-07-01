# Environment variables

[devstack](../../README.md) · [Guide](./README.md) › Environment variables

Plain, local key→value environment variables — **no external store, no provider,
no login**. Everything on this page lives in your committed `devstack.yaml`, is
resolved at `generate`/`up` time, and is emitted straight into the container's
compose `environment:`.

If a value is an actual **secret**, don't put it here — see [Secrets](secrets.md)
and `secret://`. This page is for ordinary configuration.

---

## Start here: `env.raw`

`env.raw` is the zero-setup mechanism. It's a literal map of `KEY: value` on a
service; each pair is emitted **verbatim** into that service's environment.

```yaml
# devstack.yaml
apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: php.laravel.nginx
    env:
      raw:
        APP_NAME: "Acme API"
        LOG_CHANNEL: stderr
        CACHE_STORE: redis
```

That's the whole feature. No store to initialize, nothing external. The three
keys land in the `api` container exactly as written.

Values may contain `${...}` interpolation, resolved by devstack before the file
is written (strict — an undefined reference is an error; escape a literal dollar
as `$$`):

| Expression | Resolves to |
|---|---|
| `${profile}` | the active env-overlay profile (default `dev`) |
| `${workspace.name}` | the workspace name |
| `${env.NAME}` | host environment variable `NAME` (errors if unset) |
| `${self.ATTR}` | an attribute of the current service (e.g. `${self.host}`) |
| `${ref:PATH}` | a cross-service reference, e.g. `${ref:workspace.shared.postgres.host}` |

```yaml
    env:
      raw:
        APP_ENV: "${profile}"
        APP_URL: "https://${self.host}"
        WORKSPACE: "${workspace.name}"
        PG_HOST: "${ref:workspace.shared.postgres.host}"
```

> Note the sub-delimiters: `env.` and `self.` use a **dot**; `ref:` uses a
> **colon**.

---

## `env.prefixed` — avoid name collisions

`env.prefixed` works exactly like `env.raw`, except the generated compose key is
namespaced with the **service name** in upper-case: `<SERVICE>_<KEY>`.

```yaml
services:
  api:
    template: php.laravel.nginx
    env:
      prefixed:
        URL: "https://${self.host}"   # → container sees API_URL
        PORT: "8080"                   # → container sees API_PORT
```

Use it when several services in a stack would otherwise export the same bare key
and you want them disambiguated.

---

## `${env.NAME}` — pull a value from your host shell

`${env.NAME}` reads a variable from **your host environment** at generate time.
If it isn't set, generation fails fast (so a missing value can't silently ship as
empty).

```yaml
    env:
      raw:
        GITHUB_TOKEN: "${env.GITHUB_TOKEN}"   # taken from your shell
        REGION: "${env.AWS_REGION}"
```

```bash
export AWS_REGION=eu-west-1
devstack generate     # REGION is baked as eu-west-1
```

This keeps host-specific config out of the committed file while still being plain
(non-secret) values. For real secrets, prefer `secret://` over `${env.*}`.

---

## `env.import` vs. `env.raw`

`env.raw`/`env.prefixed` give you values you **write**. `env.import` pulls
connection attributes **exported by another service** — a shared engine or
another project — so you never copy host/port/credentials by hand.

```yaml
services:
  api:
    template: php.laravel.nginx
    uses:
      - workspace.shared.postgres
    env:
      import:
        - from: workspace.shared.postgres
          vars: [host, port, user, password, database]
```

The imported vars are emitted as `<SOURCE>_<VAR>` in upper-case — the example
above yields `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_USER`,
`POSTGRES_PASSWORD`, `POSTGRES_DATABASE`. The source can also be another
project's service:

```yaml
    env:
      import:
        - { from: workspace.api.api, vars: [host] }   # → API_HOST
```

Rule of thumb: **`env.raw` for values you own, `env.import` for values another
service owns.** (Note: a `password` attribute imported from an engine that
provisions a real secret is emitted as a valueless key and injected at runtime,
never written to disk.)

The full resolution order applied at generate time is: the template's own
`environment:` → `env.raw` → `env.prefixed` → `env.import`.

---

## Profiles: `${profile}` overlays (dev vs test)

`${profile}` resolves to the **active env-overlay profile**, `dev` by default.
Switch it per invocation with `up --profile` / `generate --profile`. This lets one
committed file serve several environments.

```yaml
services:
  api:
    env:
      raw:
        APP_ENV: "${profile}"        # dev, test, …
        LOG_LEVEL: "${profile}"      # or map it however you like
```

```bash
devstack generate                 # APP_ENV=dev (default overlay)
devstack generate --profile test  # APP_ENV=test
devstack up -p test               # bring the stack up under the test overlay
```

> `--profile`/`-p` on `up` is also the service-slice selector (which services to
> start). The `${profile}` value tracks the same overlay name, so `up -p test`
> both starts the `test` slice and sets `${profile}` to `test`.

---

## When to graduate to `secret://`

Keep it simple: **plain config → `env.raw`; real secrets → `secret://`.** There is
no separate "env store" command — plain env lives entirely in the committed
`devstack.yaml`. Reach for [`secret://`](secrets.md) only when a value must not be
committed in plaintext (API keys, DB passwords, tokens):

```yaml
    env:
      raw:
        STRIPE_KEY: "secret://sops/app.enc.yaml#stripe.key"
```

A `secret://` value in `env.raw`/`env.prefixed` is emitted as a **valueless** env
key; the resolved value is injected at container start via the process env and
never written to any generated file.

Already have a `.env`? `devstack secrets ingest .env --from-host KEY` can classify
plain keys as config (emitted as `${env.KEY}`) and route real secrets to a
provider. See [Secrets](secrets.md) for the full ingest flow.

---

## Recipes

### 1. Per-service static config

```yaml
services:
  api:
    env:
      raw:
        CACHE_STORE: redis
        QUEUE_CONNECTION: redis
        MAIL_MAILER: log
```

### 2. Host-derived value (kept out of the committed file)

```yaml
services:
  api:
    env:
      raw:
        SENTRY_DSN: "${env.SENTRY_DSN}"
```

```bash
export SENTRY_DSN='https://examplePublicKey@o0.ingest.example.com/0'
devstack generate
```

### 3. Different value per profile (dev vs test)

```yaml
services:
  api:
    env:
      raw:
        APP_ENV: "${profile}"
        DB_DATABASE: "app_${profile}"   # app_dev, app_test
```

```bash
devstack up            # APP_ENV=dev,  DB_DATABASE=app_dev
devstack up -p test    # APP_ENV=test, DB_DATABASE=app_test
```

### 4. Avoid collisions with `prefixed`

```yaml
services:
  api:
    env:
      prefixed: { PORT: "8080" }   # → API_PORT
  worker:
    env:
      prefixed: { PORT: "9090" }   # → WORKER_PORT
```

---

## See also

- [Projects](projects.md) — where `services:` and `env:` are declared
- [Templates](templates.md) — where a template's own `environment:` comes from
- [Shared services & host access](shared-services.md) — what `env.import` sources
- [Secrets](secrets.md) — `secret://`, providers, and `secrets ingest`
- [Config reference](config-reference.md) — the full `${...}` grammar

---

◀ [Projects](projects.md) · [Guide index](./README.md) · [Templates](templates.md) ▶
