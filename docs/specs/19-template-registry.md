# Spec 19 — Versioned template registry

**Module:** `internal/template` (`TemplateSource`) + `internal/registry` · **Milestone:** v2 · **Effort:** ~2.5w (feature #11)

## Purpose
Turn a personal pile of `go:embed`'d templates into **team infrastructure**: a platform team publishes "the way we run services" once (`template push oci://ghcr.io/acme/templates:1.4.0`), and every developer renders **byte-identical** compose+Dockerfiles from a **digest-pinned** lockfile in `workspace.yaml`. This adds the publish/consume/verify workflow — push, pin, diff, update, lint/test, init — around the remote `TemplateSource` that [spec 02](02-templating-and-generation.md) already abstracts.

> **Builds on the v1 substrate:** the `TemplateSource { Resolve(ref) (fs.FS, error) }` interface, the digest→cache mapping under `XDG_CACHE_HOME` with GC/TTL ([D15](../DECISIONS.md)), the deterministic generation pipeline ([ARCHITECTURE §3](../ARCHITECTURE.md)), and the editor JSON Schema that travels with a template ([D16](../DECISIONS.md)). Deferred to v2 because v1's built-in embedded templates and `template init|lint|test` ([spec 07](07-cli-and-aliasing.md)) already cover a solo user; remote distribution only earns its keep once *multiple* repos must share a curated set, and it carries a supply-chain surface (signed artifacts, trust model) that the four-pillar 1.0 should not block on.

## TemplateSource flavors (one interface, four schemes)
`Resolve(ref)` dispatches on the ref scheme; all return an `fs.FS` rooted at a template directory ([spec 02](02-templating-and-generation.md) §"Template = a directory"):

| Scheme | Example | When | v2 read path |
|---|---|---|---|
| `embedded` (built-in) | `php.laravel.nginx` | v1 default; ships in the binary | `go:embed` (always offline) |
| `oci://` | `oci://ghcr.io/acme/templates:1.4.0` | **the registry workflow** | pure-Go ORAS pull → digest cache |
| `git+https://` / `git+ssh://` | `git+ssh://git@github.com/acme/tmpl.git//php?ref=v1.4.0` | git-native teams; reuses `internal/git` auth | shallow clone at `ref`, then resolve to commit SHA |
| `file://` | `file://../local-templates/php` | authoring / `template diff` | direct fs (never cached, never pinned) |

OCI is the primary post-1.0 path (registry auth, content-addressing, and signatures are first-class there). Git is the lighter alternative for teams already living in a repo; it pins to the **resolved commit SHA**, not a moving branch/tag.

## Digest pinning preserves determinism (the whole point)
A render is reproducible across a team only if template content is addressed by **digest**, not a moving tag. `workspace.yaml` gains a `templates:` lockfile block — resolved at `template add`/`update`, **read** at generation:

```yaml
# workspace.yaml  (apiVersion: devstack/v1; additive key, forward-tolerant per spec 14)
templates:
  - name: acme            # local alias used in `template: acme/php.laravel`
    source: oci://ghcr.io/acme/templates
    version: "1.4.0"      # the human-facing tag (provenance only — NOT used to fetch)
    digest: "sha256:9c8b…"   # the OCI manifest digest — what is actually fetched/verified
    schemaVersion: 3      # the template-bundle schema version (see "what travels")
```

- **Generation never resolves a tag.** It fetches by `digest`; the `version` is recorded for human diffs/changelogs only. A moving tag is resolved-and-pinned exactly once (at `add`/`update`), never on the hot path — this is what makes `up` byte-stable for the whole team and keeps the CI determinism check ([spec 02](02-templating-and-generation.md) AC) green.
- **Cache key is the digest, never the tag.** `XDG_CACHE_HOME/devstack/templates/<sha256>/…` ([D15](../DECISIONS.md)). Keying on the tag would let a re-pushed `:1.4.0` poison every machine's cache silently; keying on the digest makes a re-push a *new* cache entry that a pinned workspace simply never reaches. GC/TTL evicts by digest; a digest still referenced by any workspace lockfile is pinned against eviction.
- **Lockfile is committed** (it lives in `workspace.yaml`). A teammate cloning the repo renders the same bytes offline once the digest is in cache; a cold cache triggers exactly one pull.

## CLI surface (extends [spec 07](07-cli-and-aliasing.md))
```
devstack template init [<dir>]            # scaffold template.yaml + build/ + schema + golden fixture (v1, kept)
devstack template lint [<dir>]            # schema + param-type + delimiter-collision checks (v1, kept)
devstack template test [<dir>]            # golden-render: params fixture → expected output tree (v1, kept)
devstack template push  oci://…:<tag> [<dir>] [--sign]    # v2: build bundle, push, optionally cosign-sign
devstack template add   oci://…:<tag>     # resolve tag→digest, verify, write/pin templates: entry
devstack template update [<name>]         # re-resolve tag→new digest; show diff; rewrite lockfile (opt --to <tag>)
devstack template diff  <name>            # render-diff pinned-digest vs latest-tag (or vs file://) — no writes
devstack template ls                      # list pinned templates + resolved digests + cache state
devstack template verify                  # re-pull each pinned digest, re-check signature, assert no drift
```
`add`/`update`/`verify` **mutate or read the cache** and so take the `gofrs/flock` ([spec 08](08-state-locking-and-lifecycle.md)) around the cache write (concurrent `up` invocations must not race a half-written cache dir); the write itself is tmp-dir-then-`os.Rename` atomic, mirroring `writeIfChanged`. `push`/`lint`/`test`/`diff`/`ls` are read-only and lock-free. All carry the `--json` contract ([ARCHITECTURE §7.9](../ARCHITECTURE.md)).

## What travels in a template bundle
A bundle is an OCI artifact (or a git subtree) carrying the [spec 02](02-templating-and-generation.md) template directory **plus the editor schema** ([D16](../DECISIONS.md)) so autocomplete/validation stay in lockstep with the template version a workspace actually pins:

```
template.yaml          # extends:, schemaVersion, typed params: with defaults
build/…                # file templates (Dockerfiles, conf, scripts)
post_init.yaml         # optional
params.schema.json     # JSON Schema for this template's params (editor + lint authority)
NOTICE                 # carried licenses (compose-go Apache-2.0, etc.)
```
The **template-bundle `schemaVersion`** is independent of config `apiVersion` and state `schema_version` ([spec 14](14-self-update-and-migration.md) — a fourth versioned artifact): a newer binary tolerates an older bundle; a bundle whose `schemaVersion` is *newer* than the binary understands fails `add`/generation with a "upgrade devstack to use this template" message (never a silent partial render). An `extends:` chain may cross bundles — each base is resolved through `TemplateSource` and pinned by its own digest, so the merge inputs are fully content-addressed.

## Supply-chain posture (templates execute as part of generation)
Pulled templates become build instructions (Dockerfiles, entrypoint scripts) — the **same trust model as any compose file** ([spec 02](02-templating-and-generation.md) gotcha). The registry makes the trust boundary explicit:
- **Digest verification is mandatory.** A pulled manifest whose computed digest ≠ the pinned `digest:` is **refused**, generation aborts, the cache entry is not written. (Cheap, always-on, no keys required.)
- **Signature verification is optional but first-class.** `template push --sign` cosign-signs the artifact; `verify.cosign: { enabled, keyless|key, identities }` in `workspace.yaml` makes `add`/`update`/`verify`/generation require a valid cosign signature over the manifest digest before trusting it. Pure-Go path via `sigstore-go` verification (no `cosign` binary required); keyless (Fulcio/Rekor) and keyed both supported. A signature failure aborts like a digest mismatch.
- **Trust is opt-in and loud.** With no `verify` block, devstack pins+verifies the digest only and prints a one-line "templates are digest-pinned but unsigned" notice on `add` (mirrors the [spec 14](14-self-update-and-migration.md) checksum-only self-update warning). Adding a template from an unknown registry never silently grants it build-time execution.

## Verified constraints / gotchas
- **OCI artifacts need correct media types + a pure-Go client.** Use `oras-go/v2` (`oras.land/oras-go/v2`, Apache-2.0, pure-Go — preserves `CGO_ENABLED=0`); never shell to a `docker`/`oras` binary for pull (the binary may be absent and it muddies offline behavior). Push a custom `artifactType` (`application/vnd.devstack.template.v1`) with each layer's `mediaType` set so non-devstack tooling treats the artifact as opaque rather than mis-rendering it as an image.
- **A moving tag breaks determinism.** Never fetch by tag at generation — resolve to a digest once and pin it; `:latest` is rejected by `add` without an explicit `--allow-floating`.
- **Cache poisoning if the key is the tag.** The cache MUST key on the digest ([D15](../DECISIONS.md)); a tag-keyed cache lets a re-pushed tag serve stale-or-malicious content to a pinned workspace. (Restates the §"Digest pinning" rule because getting it wrong silently defeats the whole feature.)
- **Private-registry auth rides the existing credential store.** ORAS reads `~/.docker/config.json` (and OS credential helpers / `GITHUB_TOKEN` for GHCR) — do **not** invent a devstack token store. Git templates ride `internal/git`'s system-git auth ([spec 06](06-git.md), [D9](../DECISIONS.md)) — SSH agent / `GIT_ASKPASS` — for free.
- **The editor schema must travel with the template** ([D16](../DECISIONS.md)): if `params.schema.json` is not bundled+versioned, a developer pinning `1.4.0` gets autocomplete for whatever version their editor last cached — schema/template skew. The `yaml-language-server` modeline still points at the *config* schema; the param schema is consumed by `lint` and editor param hints.
- **OCI digests are over the canonical manifest**, not the rendered output — verify the pulled bytes match the digest *before* unpacking into the cache, and unpack atomically (tmp dir + rename) so a `kill -9` mid-pull never leaves a half-populated, wrongly-trusted cache dir.
- **WSL2/9p:** template cache lives on the **Linux** side ([ARCHITECTURE §8](../ARCHITECTURE.md)); never under `/mnt/*`. A registry pull is network I/O — `doctor` should treat registry unreachability as non-fatal when every pinned digest is already cached (offline-first).

## Acceptance criteria
- [ ] `template push oci://localhost:5000/t:1.0` then `template add` writes a `templates:` entry with the resolved `sha256:` digest; the human `version` is recorded but generation fetches by digest.
- [ ] Two machines with the same committed lockfile render **byte-identical** compose+Dockerfiles offline once the digest is cached (determinism check, [spec 02](02-templating-and-generation.md)).
- [ ] Re-pushing the **same tag** with changed content does **not** alter a pinned workspace's render until `template update` re-resolves the digest; the new content lands under a new cache key.
- [ ] A digest mismatch (tampered/corrupt pull) aborts generation, writes nothing to cache, and reports the expected vs actual digest.
- [ ] With `verify.cosign.enabled: true`, an unsigned or wrongly-signed artifact is refused on `add` and generation; a valid signature passes — no `cosign` binary required.
- [ ] `template update` shows a render-diff (old digest → new tag's digest) and rewrites the lockfile only on confirm; `--dry-run` writes nothing.
- [ ] An `extends:` chain spanning two OCI bundles pins **both** digests and merges deterministically.
- [ ] A bundle with a newer `schemaVersion` than the binary fails `add` with an upgrade message, not a partial render.
- [ ] Private GHCR pull succeeds using `~/.docker/config.json` creds with no devstack-specific token config.
- [ ] Cache GC evicts an unreferenced digest but never one still pinned by any workspace lockfile.

## Dependencies / consumers
Consumes `internal/template` (`TemplateSource` + `extends` resolver, [spec 02](02-templating-and-generation.md)), `internal/git` (git-scheme auth, [spec 06](06-git.md)), `internal/xdg` (digest-keyed cache + GC/TTL, [D15](../DECISIONS.md)), `internal/lock` (cache-write serialization, [spec 08](08-state-locking-and-lifecycle.md)), `internal/config` (the `templates:`/`verify:` schema blocks + unknown-key forward-compat, [spec 01](01-config-schema.md)/[spec 14](14-self-update-and-migration.md)), `internal/cli` (the `template` subtree + `--json`, [spec 07](07-cli-and-aliasing.md)). Consumed by `internal/generate` (resolves pinned digests as render inputs). **Thinner v2 vs full:** a thin slice (OCI pull/push via ORAS + digest lockfile + mandatory digest verify + `add`/`update`/`ls`) lands in ~1.5w and already delivers reproducible team sharing; the full version (cosign signing+verification, git-scheme source, `template diff` render-diff, registry GC integration, bundled param-schema editor wiring) is ~2.5w.

## Open questions
- [Q-T](../OPEN-QUESTIONS.md) — the chosen text-template engine (stdlib vs gonja) is what third-party bundles author against; the registry must pin engine identity so a bundle authored for one engine isn't rendered by another. (See [DECISIONS D2](../DECISIONS.md).)
- **Q-REGISTRY** *(new)* — does `template push` support **multi-template bundles** (one artifact carrying a whole `php.*`/`node.*` family addressed by sub-path) or strictly **one template per tag**? Multi-template eases atomic team releases and shared `extends:` bases but complicates per-template digest pinning and partial cache GC; recommendation: one artifact per template family with sub-path refs (`oci://…/templates:1.4.0//php.laravel`), pinned by the family digest. Record before wiring `push`.
- **Q-SIGN-TRUST** *(new)* — is cosign **keyless** (Fulcio/Rekor, requires the developer's machine to reach the public transparency log) acceptable as the default for OSS teams, or must the default be **keyed** (a team-distributed public key, fully offline-verifiable)? This trades zero-key-management for an online verification dependency; recommendation: keyed by default for offline determinism, keyless opt-in. (Relates to [Q-DAEMON](../OPEN-QUESTIONS.md)'s offline-first posture.)
