# Spec 04 — Secrets management & provider interface

**Module:** `internal/secrets` · **Milestone:** M4 · **Effort:** ~5w

## Purpose
Resolve secrets from pluggable backends and feed them into containers **without writing plaintext to disk** and **without putting secrets in committed config**.

## Interface (keep it tiny so new providers are cheap)
```go
package secrets

type Ref struct { Provider, Path, Key string; Opts map[string]string } // parsed from a reference string

type Provider interface {
    Name() string
    // Batch resolve — exploits Infisical List, SSM GetParameters (10/call), SOPS whole-file decrypt.
    Resolve(ctx context.Context, refs []Ref) (map[Ref]string, error)
}

type Factory func(cfg ProviderConfig) (Provider, error) // Register(name, factory) at init
```
**Batch `Resolve` (not single-key `Get`) is the core design choice** — every named backend is naturally batched; single-key providers just loop internally.

## Reference syntax
URI-style, opaque to the template engine until a post-render resolution pass:
```
secret://<provider>/<path>#<key>?opt=val
```
Examples: `secret://infisical/prod/DB_PASSWORD`, `secret://aws-sm/myapp/db#password`, `secret://aws-ssm/myapp/redis-url`, `secret://sops/secrets.enc.yaml#postgres.password`. In project YAML: `environment: { DB_PASSWORD: "secret://infisical/prod/DB_PASSWORD" }`. A post-render pass collects every `secret://` ref across all services, groups by provider, calls `Resolve` once per provider, substitutes.

## v1 providers — confirm the set ([Q-S](../OPEN-QUESTIONS.md))
Research recommends shipping **two**: **SOPS+age** (offline, no-account default — the "works on a plane" path) + **AWS** (Secrets Manager + SSM). You originally named **Infisical + AWS**. Recommendation: SOPS+age + AWS + Infisical if effort allows; else SOPS+age + AWS with Infisical as the first 1.x plugin.
- **Infisical** — `github.com/infisical/go-sdk`, Universal Auth machine identity, `Secrets().List` batch.
- **AWS** — `aws-sdk-go-v2/service/secretsmanager` (`BatchGetSecretValue`) + `.../ssm` (`GetParameters`, `WithDecryption`), both via `aws-sdk-go-v2/config.LoadDefaultConfig` (SSO/profile/env/IMDS — **no custom credential code**).
- **SOPS+age** — `github.com/getsops/sops/v3/decrypt` (`decrypt.File`), `filippo.io/age` for `secrets keygen`.
- **Deferred behind the same Factory:** Vault (`hashicorp/vault/api`), 1Password (`onepassword-sdk-go`), Doppler (the `doppler` CLI).

## Injection — no plaintext on disk (the core safety property)
⚠️ **The naive "host env auto-propagates into compose containers" assumption is FALSE** (verified). Mechanism that actually works:
- For each secret, emit a **valueless** `environment: [SECRET_NAME]` key into the generated compose **per service that needs it** (Compose resolves the value from the process running Compose; unresolved → unset and removed).
- Pass resolved values via **`exec.Cmd.Env`** on the `docker compose up` child process.
- Generated compose/`.env` files contain only the key name (or `${VAR}` placeholders) — **never** literal secret values. Non-secret config may live in generated `.env`.
- `up` has **no `-e` flag** — only `compose run` does, and that's a one-off, not the service set. So the key *must* appear in the YAML per service. **This couples secrets ↔ generation** ([spec 02](02-templating-and-generation.md)) — one owner, one CI leak-test.
- Rare file need: write `0600` under `$XDG_RUNTIME_DIR` (tmpfs on Linux), delete on exit; document the weaker guarantee on macOS/WSL2.

## Credentials & caching
- **Provider credentials are not secrets-in-config.** Resolve in order: explicit env vars → the OS keyring entry the tool manages (`secrets login <provider>`) → provider-native defaults (AWS shared config, `SOPS_AGE_KEY_FILE`, `OP_SERVICE_ACCOUNT_TOKEN`). Keeps `workspace.yaml` committable with zero secrets.
- **Caching:** in-memory per invocation (resolve once, reuse across services). Optional cross-invocation cache is **encrypted** in the OS keyring (`zalando/go-keyring`), TTL default 0 (disabled).
- **Log redaction middleware:** scrub resolved values from any debug output; `--show-secrets` opt-in for diagnostics only.

## Verified constraints / gotchas
- **Exposure remains** via `docker inspect`, `/proc/<pid>/environ` (same-user), container env — this design narrows but doesn't eliminate it. **Document the threat model honestly**; don't over-engineer `mlock` for a local dev tool.
- **Infisical SDK is pre-1.0** (breaking changes, e.g. `AutoTokenRefresh` became a pointer in v0.8.0; requires Go 1.24; free tier caps *total* identities at 5; pkg.go.dev mislabels its license) — pin + wrap.
- **SOPS pulls all cloud-KMS SDKs transitively** even for age-only use (binary bloat) — consider build tags or shelling to the `sops` binary; only `…/v3/decrypt` has a (convention-level) stable API; `SOPS_AGE_KEY_FILE` must be set before `decrypt.File`; macOS/Linux default key paths differ — set it explicitly.
- **WSL2 frequently lacks Secret Service/D-Bus** → keyring cache + `secrets login` storage break; detect and degrade to env-var/in-memory with a one-line warning.
- **1Password** desktop auth needs `CGO_ENABLED=1` (breaks static build) — service-account auth only for pure-Go; `nikoksr/doppler-go` is **archived** — use the CLI (`doppler secrets download --no-file --format json`).

## Acceptance criteria
- [ ] A `secret://` ref in a project's env resolves and reaches the container, with **no secret value present in any generated file** (asserted by a CI integration test).
- [ ] A batch of N refs to one provider makes one `Resolve` call, not N.
- [ ] SOPS+age path works fully offline with no account.
- [ ] AWS provider authenticates purely via `LoadDefaultConfig` (SSO profile) with no custom credential code.
- [ ] On WSL2 without D-Bus, `secrets login` degrades to env-var mode with a clear warning (tool still works).
- [ ] Debug logs never print a resolved secret unless `--show-secrets`.

## Dependencies / consumers
Consumes `internal/config` (refs in config) + `internal/state` (optional encrypted cache). Consumed by `internal/generate` (per-service key emission), `internal/workspace` (stores generated per-project DB/minio creds), `internal/git` (HTTPS token via `GIT_ASKPASS` shim — [spec 06](06-git.md)).

## Open questions
[Q-S](../OPEN-QUESTIONS.md) (which two/three providers).
