# Spec 24 — `.env` ingestion → secrets & config vars

**Module:** `internal/envingest` (new) · `internal/secrets` (new write capability) · `internal/prompt` (new non-TTY-degradable TUI wrapper) · `internal/cli` (`secrets ingest`) · **Milestone:** M8 — post-M7 polish lane (0.x line; ships in a later 0.x minor, **not** v1.0) · **Effort:** ~2.5w

## Purpose
Get committed `.env` files out of the repo. One command reads an existing `.env`, **classifies each key** secret vs non-secret, **encrypts the secret half** into a SOPS+age file (or pushes it to an already-configured provider) so **no plaintext ever lands on disk or in committed config**, **inlines the non-secret half** as ordinary config vars, **rewrites `devstack.yaml`** so every former env value becomes a literal config value or a `secret://` ref ([spec 04](04-secrets.md)), and then fences/deletes the original `.env`. The output is byte-for-byte the env shape the existing **read** path ([spec 04](04-secrets.md) injection, [spec 02](02-templating-and-generation.md) §7.5 coupling) already resolves — `internal/generate/secrets.go SecretRefs` already scans `env.raw`/`env.prefixed` for `secret://` values and emits valueless keys — so this spec builds only the missing **write** side. It mirrors the `devstack import` UX contract ([spec 14](14-self-update-and-migration.md)): dry-run that writes nothing, no-clobber without `--force`, and a loud conversion report.

## Decisions
- **New module `internal/envingest`** owns the pipeline (parse → classify → route → encrypt/push → rewrite → fence). The secret-vs-config policy and the YAML rewrite live here, not in `internal/secrets` (which stays a thin provider boundary) and not in `internal/cli`.
- **Reuse the Compose dotenv parser** — `github.com/compose-spec/compose-go/v2/dotenv` (already in `go.mod` via `compose-go v2.12.1`). `Parse(io.Reader) (map[string]string, error)` (or `ParseWithLookup` if source-side `${VAR}` interpolation must be honored). **Zero new parser dependency**, and byte-for-byte parity with how `docker compose` reads `.env` (quoting, multiline, `export ` prefix, comments). Do **not** add `joho/godotenv` or `hashicorp/go-envparse`. **Note:** `Parse` returns an *unordered* `map`, so source order is not recoverable from it; the report, the encrypted payload, and the rewritten YAML are emitted in **sorted-key order** for determinism (the same `sortedKeys` discipline `internal/generate` uses) — not "source order".
- **Default destination = a SOPS+age file** (`secrets.enc.yaml` at the workspace root) — the offline, no-account, "works on a plane" path, identical to spec 04's default provider. Pushing to AWS/Infisical is **opt-in** via `--to aws-sm|infisical` and requires the new write capability below.
- **SOPS is encrypted via `sops --encrypt` over stdin**, never a plaintext temp file in the repo. The assembled plaintext map is YAML-marshalled and piped to `sops --encrypt --input-type yaml --output-type yaml /dev/stdin --age <recipient>`; stdout (ciphertext) is written via `writeIfChanged`. Output-type **yaml/json**, not dotenv (dotenv flattens to flat string `KEY=VALUE` and can't express nested keys — the existing read path resolves `secret://sops/secrets.enc.yaml#postgres.password` dot-paths, and the decrypt side already shells `sops -d --output-type json`). Encryption needs only the **recipient** (`age1…` public key); no private key is read for the write half.
- **Keep shelling out to the `sops` binary; do NOT vendor `getsops/sops/v3`.** `internal/secrets/sops.go` deliberately shells `sops -d` (its header: "shells out to the `sops` binary (NOT the getsops Go SDK)") because the Go SDK pulls all cloud-KMS SDKs transitively (binary bloat, larger surface against `CGO_ENABLED=0`). The encrypt half follows the same rule: `exec.Command("sops", "--encrypt", …)` with plaintext on stdin.
- **The `Provider` interface gains an optional `Pusher`** (capability interface, not a contract change): `internal/secrets` adds `type Pusher interface { Push(ctx, []SecretEntry) error }`; the AWS/Infisical providers implement it where a write CLI exists (`aws secretsmanager put-secret-value` / `aws ssm put-parameter` / `infisical secrets set`). The current `Provider` interface is `Resolve`-only — this write capability is **net-new code**. The SOPS provider is the default, no-account, write-to-file path and needs no `Pusher`. Providers without a writer stay `Resolve`-only and ingest refuses `--to` against them with a one-line reason.
- **Classification is policy-driven, default-deny (safe):** an unknown key is treated as **secret**. Order of precedence: explicit `--public`/`--secret` glob lists → name-pattern matcher (extends the existing `secretAttrs` seed) → value-shape heuristics (high-entropy, credentialed URL, PEM/JWT shape) → interactive confirm (TTY) → default secret.
- **Non-secret (config) keys default to an inline literal**, **not** `${env.<KEY>}`. The source of those values is the `.env` file, which this command deletes — so a `${env.<KEY>}` interpolation would resolve to empty at the next `generate`. `${env.<KEY>}` is therefore **opt-in only** for keys the operator marks as still supplied by the ambient host/CI environment (wizard toggle or `--from-host <glob>`); the default for every non-secret key is to write its literal value verbatim into `env.raw`.
- **Interactive wizard is a Bubble Tea v2 model behind `internal/prompt`**, reusing the shared `internal/prompt`/`internal/tui` theme and components from [spec 22](22-init-wizard.md)/[spec 23](23-template-authoring.md) so it looks like one product. The per-key classification UI is a `charm.land/bubbles/v2` **`table`** (one row per `.env` key: name · class · reason · emitted ref/literal) with inline secret/config toggles, over a `charm.land/lipgloss/v2` theme; `charm.land/huh/v2` is embedded for the destination/recipient sub-form. The wizard is **never the only path**: `--yes`/`--json`/`--quiet`/non-TTY/`CI` all bypass it and use the computed classification + flag overrides. `internal/prompt` gates on `term.IsTerminal` and falls back to non-interactive **before** the Bubble Tea runtime ever starts.
- **`devstack.yaml` rewrite is position/comment-preserving** via the goccy AST (config already parses with `goccy/go-yaml`, which exposes positions): only the touched `environment` values are replaced; the `apiVersion: devstack/v1` header, key order, and comments survive. This is a committed, hand-edited file, so the rewrite obeys no-clobber + backup like `import`.
- **Ensure the destination provider is declared.** A `secret://sops/…` ref only resolves if a provider instance named `sops` exists in `workspace.yaml` (`secrets.providers[]`). Ingest checks for it and, if absent, **scaffolds a default `sops` provider entry** (pointing at the `$DEVSTACK_HOME` age key file) into `workspace.yaml` — backing that file up too — so the round-trip resolve in step 7 can succeed. If the provider exists but its name differs, ingest uses the declared name in the emitted refs.
- **No flock is taken in any mode.** The #1 rule governs mutations of the **ledger** or the **shared stack** ([CLAUDE.md](../CLAUDE.md)); a project-local ingest (write `secrets.enc.yaml` + rewrite `devstack.yaml`/`workspace.yaml`) touches neither, and a remote `--to aws-sm|infisical` push touches neither (it mutates an external secrets store, which a local advisory flock cannot protect anyway). Remote-push idempotency relies on the provider's put/overwrite semantics, not a lock.
- **`.env` is fenced before it is removed.** Ingest appends `.env` to `.gitignore` (marker-fenced), **refuses to proceed if `.env` is already git-tracked** (the plaintext is already in history — a different remediation), re-resolves every newly written ref to prove round-trip, and only then deletes `.env` (or, with `--keep-env`, leaves it and just prints the deletion command). The age **private** key lives under `$DEVSTACK_HOME` (outside the repo tree), so it needs no repo `.gitignore` entry; the fence covers an in-repo key file only if one was created there.

## Reference & data shapes
```go
package envingest

type Class int
const ( ClassSecret Class = iota; ClassConfig )

type Decision struct {
    Key      string   // e.g. DB_PASSWORD
    Class    Class
    Reason   string   // "name matches *PASSWORD", "--public override", "high-entropy value", "default-deny"
    Ref      string   // emitted value: secret://sops/secrets.enc.yaml#DB_PASSWORD  (secret)
                      //   or the inline literal (config, default)
                      //   or ${env.PORT}  (config, only when marked host-sourced)
    HostFrom bool     // config key whose value still comes from the ambient host/CI env (→ ${env.KEY})
    Service  string   // which devstack.yaml service the key is rewritten into
}

type Plan struct {
    Source    string       // path to the .env
    Decisions []Decision   // emitted in sorted-key order (deterministic)
    Dest      string       // "sops" | "aws-sm" | "infisical"
    DestPath  string       // secrets.enc.yaml | secret-id prefix | infisical path
    Provider  string       // declared provider instance name used in the refs
}
```
```go
package secrets

// New optional capability — providers implement it only where a write CLI exists.
type SecretEntry struct{ Path, Key, Value string }
type Pusher interface{ Push(ctx context.Context, entries []SecretEntry) error }
```

## CLI surface
```
devstack secrets ingest [<path/to/.env>] \
    [--to sops|aws-sm|infisical] [--dest <file|secret-id-prefix|path>] \
    [--service <name>] [--recipient age1...] \
    [--secret <glob>...] [--public <glob>...] [--from-host <glob>...] \
    [--prefixed] [--keep-env] \
    [--dry-run] [--yes] [--force] [--json]
```
- `<path>` defaults to `./.env`; with no TTY and no `--yes`, ingest runs non-interactively using computed classification + flag overrides.
- `--to` selects the destination backend (default `sops`); `--dest` overrides the file/secret-id/path.
- `--recipient` supplies the age public key; default discovery order: `--recipient` → `.sops.yaml` creation rule → the recipient printed by `secrets keygen` (the local age key under `$DEVSTACK_HOME`, spec 04).
- `--secret`/`--public` force classification; `--from-host` marks a config key as still sourced from the ambient host/CI env so it is emitted as `${env.KEY}` instead of an inline literal.
- `--prefixed` routes secrets to `env.prefixed` (compose key becomes `<SERVICE>_<KEY>`, exactly what `generate/secrets.go SecretRefs` emits) instead of `env.raw` (verbatim key).
- `--dry-run` writes nothing and prints the full plan + would-be diffs (the [spec 14](14-self-update-and-migration.md) contract); `--json` emits the `Plan` as machine-readable and suppresses the TUI.

## Behavior
1. **Discover & guard.** Resolve the workspace root (walk up for `workspace.yaml`) and the target `devstack.yaml` for `--service`. Refuse early if `.env` is **git-tracked** (`git ls-files --error-unmatch .env` exits 0) with the "already in history; rotate these secrets, this tool can't un-commit them" remediation. Refuse if `--to` names a provider whose builder doesn't implement `Pusher`. If the destination provider instance is not declared in `workspace.yaml`, plan to scaffold it (step 6).
2. **Parse.** `dotenv.Parse` the file into `map[string]string`; sort keys for a deterministic report and stable refs. Reject duplicate keys with `file:line` ([spec 01](01-config-schema.md) error style).
3. **Classify.** For each key, run the policy ladder (override globs → name patterns → value heuristics → default-deny). Produce a `Decision` per key with a human reason. On a TTY without `--yes`, open the `internal/prompt` wizard pre-populated with the computed classes; the user toggles secret/config per key, marks host-sourced config keys, and picks the destination. Non-TTY/`--yes`/`--json` skip the wizard.
4. **Assemble destination payload (no plaintext on disk).**
   - **SOPS:** build a YAML map of `{key: value}` for the secret subset (sorted), marshal in-memory with `goccy/go-yaml`, **pipe to `sops --encrypt … /dev/stdin`** via `exec.Cmd.Stdin`; capture ciphertext stdout; `writeIfChanged` it to `secrets.enc.yaml`. Plaintext exists only in process memory and the `sops` child's stdin pipe — never a repo temp file. If a `0600` scratch file is ever unavoidable on a platform, it goes under `$XDG_RUNTIME_DIR` (tmpfs) and is deleted on exit, with the weaker macOS/WSL2 guarantee documented ([spec 04](04-secrets.md)).
   - **aws-sm / infisical:** call the provider's `Push` (shelling `aws secretsmanager put-secret-value` / `aws ssm put-parameter --type SecureString` / `infisical secrets set`); values travel via env/stdin, never as logged argv. No flock.
5. **Compute refs.** Each secret key → `secret://<provider>/<destPath>#<key>` (SOPS dot-path; AWS `secret-id#json-key`; Infisical `path/KEY`) — byte-identical to what `ParseRef` accepts and what `generate/compose.go` then emits **valueless**. Each non-secret key → its **inline literal** in the chosen service's `env.raw` (or `${env.<KEY>}` only when the key was marked host-sourced).
6. **Rewrite committed YAML (in place, AST-preserving).** Replace the touched `environment` values in `devstack.yaml` only; keep header, order, and comments. If the destination provider is missing, add a default `sops` provider entry to `workspace.yaml`. No-clobber: back up each touched file → `<file>.bak.<ts>` before writing (skipped under `--dry-run`; required even with `--force`, which only suppresses the prompt).
7. **Verify round-trip.** Re-load config, `Collect` the new refs, and `Resolve` them once per provider ([spec 04](04-secrets.md) batch path). If any new ref fails to resolve to its original value, **abort before deleting `.env`** and roll the YAML back from the backups.
8. **Fence & remove `.env`.** Marker-fence-append `.env` to `.gitignore`; delete `.env` (or, with `--keep-env`, print the `git rm --cached .env && rm .env` command). Print the conversion report: every key, its class, its reason, its emitted ref/literal, and the destination.

## Worked example
Before — committed `./.env`:
```dotenv
export DB_PASSWORD="s3cr3t-p@ss"
REDIS_URL=redis://shared-redis:6379/0
APP_ENV=local
STRIPE_SECRET_KEY=sk_live_51Hxxxxxxxxxxxx
PORT=8080
```
`devstack secrets ingest --service api` (TTY → wizard, defaults shown):
```
key                 class    reason
DB_PASSWORD         secret   name matches *PASSWORD
STRIPE_SECRET_KEY   secret   name matches *SECRET*  +  high-entropy value
APP_ENV             config   no secret signal
PORT                config   no secret signal
REDIS_URL           config   no secret signal (DNS alias, no credentials)
→ destination: sops  (secrets.enc.yaml, recipient age1ql3z…)
```
After — `secrets.enc.yaml` (ciphertext, committable) holds `DB_PASSWORD` + `STRIPE_SECRET_KEY`; `devstack.yaml` rewritten (config keys **inlined** as literals, since `.env` is being deleted):
```yaml
apiVersion: devstack/v1
kind: Project
services:
  api:
    env:
      raw:
        DB_PASSWORD: "secret://sops/secrets.enc.yaml#DB_PASSWORD"
        STRIPE_SECRET_KEY: "secret://sops/secrets.enc.yaml#STRIPE_SECRET_KEY"
        APP_ENV: "local"
        PORT: "8080"
        REDIS_URL: "redis://shared-redis:6379/0"   # ${env.REDIS_URL} only with --from-host REDIS_URL
```
`.gitignore` gains a marker-fenced `.env`; `./.env` is deleted; the two secret refs are proven to re-resolve to their original values before deletion.

## Verified constraints / gotchas
- **Don't add a dotenv parser.** `github.com/compose-spec/compose-go/v2/dotenv` is **already vendored** (via `compose-go v2.12.1`), is the maintained fork Docker Compose itself uses, and gives byte-for-byte parity with `docker compose`'s `.env` handling (quoting, multiline, `export`, comments). Use `Parse`; use `ParseWithLookup` only if `${VAR}` interpolation in the source `.env` must be honored. (`joho/godotenv` is the redundant upstream; `hashicorp/go-envparse` is stricter and lacks substitution — both wrong here.) **Determinism:** `Parse` returns an unordered map; emit everything in **sorted-key order** (source order is unrecoverable from the map and not relied upon).
- **SOPS output-type must be yaml/json, NOT dotenv.** dotenv flattens everything to flat string `KEY=VALUE` and can't express nested keys — yet the existing read path (`internal/secrets/sops.go`, and the `postgres.password` test fixture) resolves **dot-path** keys. Encrypt with `--output-type yaml` so nested refs stay expressible and the decrypt side (`sops -d --output-type json`) keeps working unchanged.
- **Keep shelling out to the `sops` binary; do NOT vendor `getsops/sops/v3`.** The repo's `sops.go` deliberately shells `sops` because the Go SDK pulls all cloud-KMS SDKs transitively (binary bloat, larger surface against `CGO_ENABLED=0`). The encrypt half follows suit: `exec.Command("sops", "--encrypt", …)` with plaintext on stdin.
- **`huh` must be the v2 / `charm.land` line, not v1.** Use `charm.land/huh/v2` (v2.0.3), built on `charm.land/bubbletea/v2` + `charm.land/lipgloss/v2`, **byte-aligned with the vendored `charm.land/lipgloss/v2 v2.0.1` + `charm.land/fang/v2 v2.0.1`**. `github.com/charmbracelet/huh` (v1) pulls bubbletea v1/lipgloss v1 and would **double-vendor a conflicting charm stack**. The whole family is pure-Go (x/term, x/ansi, ultraviolet — already vendored), so it stays `CGO_ENABLED=0` across darwin/linux {amd64,arm64} with **no build tags**; keep `make vuln` in CI after adding.
- **The TUI must never be the only path and must no-op on non-TTY.** `huh.Form.Run()` errors when stdin isn't a TTY; gate on `term.IsTerminal` in `internal/prompt` and fall back to the flag/computed-classification path **before** invoking the bubbletea runtime — `--json`/`--quiet`/`CI`/non-interactive never start it ([ARCHITECTURE §7.9](../ARCHITECTURE.md) headline-output contract).
- **No plaintext in any committed file — and a CI leak-test must prove it.** Mirror `generate_test.go:TestSecretCoupling_NoValueInOutput`: after ingesting a fixture `.env`, assert that none of the original secret values appear in `secrets.enc.yaml`, `devstack.yaml`, `workspace.yaml`, `.gitignore`, any `*.bak`, or the report. The only legitimate place a value travels is `sops`'s stdin pipe / the provider `Push` CLI (via stdin/env, never argv).
- **Non-secret keys are inlined, not `${env}`-referenced.** Because ingest deletes `.env`, a `${env.KEY}` for a value that lived in `.env` would resolve to empty at the next `generate`. Default to writing the literal; only emit `${env.KEY}` when the operator marks the key host/CI-sourced (`--from-host`/wizard).
- **Refuse on already-tracked `.env`.** If `.env` is in git history, encrypting a copy doesn't remove the plaintext from the repo's past — the correct remediation is rotate-then-`git rm --cached`/history-rewrite, which this tool won't do silently. Detect with `git ls-files --error-unmatch` and stop with that guidance.
- **Routing to AWS/Infisical needs the NEW `Pusher` capability — it does not exist today.** The `Provider` interface is `Resolve`-only; `--to aws-sm|infisical` must check the built provider implements `Pusher` and otherwise refuse with "provider X is read-only in this build; use `--to sops`." Don't fake a push by writing plaintext anywhere.
- **Ensure the destination provider is declared.** The emitted `secret://<name>/…` ref resolves only if `workspace.yaml` declares a provider instance with that name. Ingest scaffolds a default `sops` provider (and backs `workspace.yaml` up) when absent, or reuses the declared name otherwise.
- **age private key stays uncommitted; only the recipient is shareable.** The `AGE-SECRET-KEY-…` identity remains `0600` under `$DEVSTACK_HOME` (the `secrets keygen` convention, outside the repo tree, so no repo `.gitignore` entry is needed); ingest reads only the `age1…` **recipient** to encrypt.
- **Idempotent write-back (no `make determinism` impact).** `make determinism` asserts byte-identical *generated* artifacts under `.devstack/`; ingest writes **committed** files (`secrets.enc.yaml`, `devstack.yaml`, `workspace.yaml`) and so is outside that harness. SOPS adds a per-file MAC/nonce, so the ciphertext bytes change on every encrypt — therefore ingest's idempotency is enforced by **decrypt-and-compare**: re-running the same ingest decrypts the existing destination, compares it to the freshly assembled plaintext plan (sorted), and **skips re-encryption** when they match, yielding a clean diff.

## Acceptance criteria
- [ ] `devstack secrets ingest .env` on a TTY opens the `huh` v2 wizard pre-populated with computed classes; `--yes`/`--json`/non-TTY/`CI` run the identical classification with **no** TUI and identical output.
- [ ] Secret keys land in `secrets.enc.yaml` as **ciphertext only**; the CI leak-test finds **no** original secret value in any written or committed file (`secrets.enc.yaml`, `devstack.yaml`, `workspace.yaml`, `.gitignore`, `*.bak`, report).
- [ ] Emitted refs parse via `secrets.ParseRef` and, placed in the rewritten `env.raw`/`env.prefixed`, are emitted **valueless** by `generate/compose.go` (proven by `SecretRefs`) and resolved by the up saga — proven by a round-trip resolve before `.env` deletion.
- [ ] Non-secret keys are written as **inline literals** by default (not `${env.X}`); `--from-host`/the wizard switches an individual key to `${env.X}` for ambient-sourced values.
- [ ] `--dry-run` writes nothing and prints the full plan + unified diffs of `devstack.yaml`/`workspace.yaml`/`secrets.enc.yaml`; `--json` emits the `Plan` and suppresses the TUI.
- [ ] `devstack.yaml` rewrite preserves the `apiVersion` header, key order, and comments; the run backs up each touched committed file before writing and refuses to overwrite without `--force`.
- [ ] Default classification treats an unknown key as **secret**; `--public`/`--secret` globs and per-key wizard toggles override, each with a printed reason.
- [ ] If no matching secrets provider is declared, ingest scaffolds a default `sops` provider in `workspace.yaml` (backed up first) so the round-trip resolve succeeds.
- [ ] `--to aws-sm|infisical` pushes via the provider's `Pusher` and is refused with a clear reason for any `Resolve`-only provider; **no mode takes a flock** (ingest touches neither the ledger nor the shared stack).
- [ ] Ingest refuses when `.env` is git-tracked, and on success marker-fences `.env` in `.gitignore` and deletes it (or prints the removal command under `--keep-env`).
- [ ] `make ci` + `make determinism` stay green (ingest writes committed files, not `.devstack/` artifacts); re-running the same ingest yields a clean diff via decrypt-and-compare; `make vuln` passes with the new `huh`/`bubbletea` v2 deps.

## Dependencies / consumers
**New deps (all direct, v2 line):** `charm.land/bubbletea/v2` + `charm.land/bubbles/v2` + `charm.land/huh/v2` (v2.0.3) (pinned for charm-stack alignment). **No** new dotenv or SOPS-SDK dep (reuse `compose-go/v2/dotenv`; shell `sops`). Consumes `internal/config` (goccy AST + the `Env{Raw,Prefixed,Import}` / `Secrets{Providers}` model — [spec 01](01-config-schema.md)), `internal/secrets` (the new `Pusher`, `ParseRef`/`IsRef`, `keygen` recipient, the SOPS shell-out — [spec 04](04-secrets.md)), `internal/generate` (`SecretRefs` key-naming + `writeIfChanged` — [spec 02](02-templating-and-generation.md)), `internal/git` (track-check + `GIT_ASKPASS` for a push — [spec 06](06-git.md)), `internal/xdg` (`$XDG_RUNTIME_DIR` scratch + `$DEVSTACK_HOME` key path). Consumed by `internal/cli` (the `secrets ingest` verb under the existing `secrets` subtree — [spec 07](07-cli-and-aliasing.md)). New `internal/prompt` is the reusable non-TTY-degradable TUI wrapper that later interactive flows (e.g. onboarding, [spec 09](09-orchestration-and-onboarding.md)) can adopt.

## Open questions
- **Q-INGEST-DEST** — default destination for ingested secrets. *Recommendation:* SOPS+age file at the workspace root (offline, no-account, matches spec 04's default provider); AWS/Infisical opt-in via `--to` + the new `Pusher`. **Decision:** SOPS default; `Pusher` ships for AWS-SM + Infisical in the same milestone, SSM behind it.
- **Q-INGEST-CLASS** — default class for an unrecognized key. *Recommendation:* **secret** (default-deny is the safe failure mode; a wrongly-secret key is an easy wizard/`--public` correction, a wrongly-public secret is a leak). **Decision:** default-deny, overridable by glob + wizard.
- **Q-INGEST-CONFIG** — how non-secret keys are emitted. *Recommendation:* inline literal by default (the `.env` source is deleted, so `${env.X}` would resolve to empty); `${env.X}` only when the operator marks the key host/CI-sourced. **Decision:** inline literal default; `--from-host`/wizard opt-in for `${env.X}`.
- **Q-INGEST-VERB** — `secrets ingest` vs a top-level `devstack import-env`. *Recommendation:* hang it on the existing `secrets` subtree (credentials + keygen already live there) and keep top-level `import` for the devdock `project.yaml` path ([spec 14](14-self-update-and-migration.md)). **Decision:** `devstack secrets ingest`.
- **Q-INGEST-MULTI** — where non-secret keys land when `.env` is repo-root but the repo has multiple services. *Recommendation:* require `--service` (or wizard pick) when ambiguous; otherwise default to the single declared service. **Decision:** require an explicit target when >1 service; error with the service list otherwise.