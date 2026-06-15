# Spec 12 — Service profiles & selective up

**Module:** `internal/workspace`, `internal/config` (+ `internal/generate`, `internal/orchestrate`) · **Milestone:** M6 · **Effort:** ~1.5w (feature #4)

## Purpose
Let a developer start a **named slice** of the workspace — `up --profile frontend` — instead of all twelve microservices, and have the tool transitively pull in *only* the shared infrastructure that slice actually `uses`. On a 16 GB laptop nobody runs the whole stack at once; profiles turn the shared-services reference graph ([spec 03](03-workspaces-and-shared-services.md)) into a first-class selection mechanism, and a `minimal` profile keeps low-RAM machines alive. This is the cheapest large-impact DX win because the graph that powers it already exists.

## The two-tier mapping (the key call)
Profiles select across **two different planes**, and the implementation must keep them separate:

| Plane | Selected by | Mechanism |
|---|---|---|
| **Project-internal services** (in each `devstack.yaml`) | Docker Compose **`profiles:`** keys on the service | `docker compose --profile NAME up -d` per project stack |
| **Shared infrastructure** (`devstack-shared` compose project) | the **reference graph** — what active project services declare in `uses:` | ref-count + start exactly those shared services ([spec 03](03-workspaces-and-shared-services.md)) |

The shared stack is a **separate compose project** with its own lifecycle and ref-counting; it is **not** driven by Compose profiles. Mixing the two is the central design error to avoid: if you put shared Postgres behind a compose `profiles:` key, the ref-count and the `(engine, majorVersion)` keying ([spec 03](03-workspaces-and-shared-services.md) §version-conflict policy) both break. Instead, the orchestrator computes the active project-service set, walks their `uses:` edges across the workspace graph, and starts/ref-counts the **union** of reached shared services — nothing more.

```
up --profile frontend
        │
        ▼  resolve active services = union of services tagged `frontend` across all projects
   [web, storefront, bff]
        │
        ▼  walk `uses:` edges of the active set against the workspace graph
   uses → { shared.postgres-16, shared.redis }     (NOT shared.minio — no active svc uses it)
        │
        ▼  ref-count + `compose -p devstack-shared up -d shared-postgres-16 shared-redis`  (inside lock)
        │
        ▼  per project: `compose -p devstack-<proj> --profile frontend up -d`
```

## Schema additions (cross-link [spec 01](01-config-schema.md))
> **Two senses of "profile" — keep them apart.** spec 01's `profiles: { default: dev }` is the **env-overlay** selector (which `*.<env>.yaml` layer is merged — a *config-layering* concept). This spec's **service slices** are a *selective-up* concept: per-service Compose `profiles:` membership tags plus workspace-level **`groups:`**, both activated by `--profile`. To avoid overloading the `profiles:` key, the workspace-level slice definitions (`groups`, `defaultProfile`) live at the **top level**, *not* under `profiles:`.

Slices are declarable at **both** tiers; the resolver unions them by name (see [Q-PROFILE](../OPEN-QUESTIONS.md) for the policy fork).

```yaml
# workspace.yaml — workspace-level slice *definitions* (named groups → project services)
profiles: { default: dev }              # env OVERLAY selector (spec 01) — NOT service slicing; unchanged
defaultProfile: core                    # NEW (top-level): which slice `up` starts with no --profile
groups:                                 # NEW (top-level): workspace-authored named service slices
  core:          { services: [api, db-migrate] }
  frontend:      { services: [web, storefront, bff] }
  payments:      { services: [payments-api, payments-worker] }
  observability: { services: [grafana, loki, tempo] }
  minimal:       { services: [api], memoryHintMB: 2048 }   # low-RAM laptops
```

```yaml
# services/web/devstack.yaml — repo-level membership (a service opts into profiles)
services:
  web:
    template: node.vite
    profiles: [frontend, core]          # NEW: Compose profile keys this service carries
    memoryMB: 768                        # NEW (reserved): per-service budget hint, feature #10
    uses: [workspace.shared.postgres, workspace.shared.redis]
```

Resolution rule: an active **profile name** selects a service if (a) the workspace `groups.<name>.services` lists it, **or** (b) the service's own `profiles:` list contains the name. `internal/config` builds a `profile → {project, service}` index at load time and validates that every name referenced in a group exists as a real service; a dangling group member is a `file:line:col` error ([spec 01](01-config-schema.md) §validate, Layer B).

## Default behaviour (no `--profile`)
1. If `defaultProfile` is set in `workspace.yaml`, `up` activates that profile.
2. Else `up` activates **all enabled services** — i.e. every service with no `profiles:` key plus every service whose profile is explicitly listed. This matches Compose semantics: a profile-less service always starts; a profiled service starts only when its profile is active.
3. `up --profile all` (reserved name) forces the whole workspace regardless of `defaultProfile`.

## CLI surface (consistent with [spec 07](07-cli-and-aliasing.md))
`--profile` is **repeatable and comma-separated**; the active set is the union:

```
devstack up --profile frontend
devstack up --profile frontend --profile payments
devstack up --profile frontend,observability
devstack up --profile minimal
```

The flag already exists in the [spec 07](07-cli-and-aliasing.md) surface (`devstack up [--profile P]`); this spec gives it semantics. Internally each active name is passed to `compose --profile` per project stack **and** drives the shared-graph walk. We set the profile set explicitly on `exec.Cmd` args rather than relying on inheriting `COMPOSE_PROFILES` from the user's shell — but we **must also export the same set as `COMPOSE_PROFILES`** in the child env so any `compose exec` used by hooks ([spec 11](11-lifecycle-hooks.md)) sees a consistent active set.

## Memory-budget hook (reserve, don't build — feature #10)
`up` sums `memoryMB` over the **active** services (+ the shared services the graph pulled in) and, when the total exceeds a host budget, prints a warning — not a hard fail. v1 only warns when the **declared** sum exceeds a *configured* `memoryBudgetMB` (absent → no check). The host-RAM probe and Docker-Desktop VM-size check are feature #10 / [spec 18](18-resource-limits-and-multi-arch.md) (the probe row is added to the [spec 13](13-doctor-diagnostics-and-teardown.md) matrix); this spec only wires the summation point and the `memoryMB` / `groups.<>.memoryHintMB` schema keys so the budget feature drops in without a schema bump. `minimal` exists precisely so a developer under a budget warning has a one-flag escape hatch.

## Interactions with sibling subsystems
- **Health/ordering ([spec 10](10-health-readiness-and-ordering.md)):** the readiness graph gates **only services in the active profile**. Inactive services are excluded from the `dependsOn: healthy` wait set, or `up --profile minimal` would block forever on a service it never started.
- **Hooks ([spec 11](11-lifecycle-hooks.md)):** `postUp`/`preDown` fire **only for active services**; a `firstRun` hook attached to an inactive service does **not** run (and its idempotency row is not written), so it still fires correctly the first time that service is later brought up.
- **`down` stops the WHOLE project, not just the active profile (recommended).** Rationale: `down` is a teardown verb; selectively downing one profile while leaving siblings up creates exactly the orphan-ref-row drift the reconcile loop ([spec 03](03-workspaces-and-shared-services.md), [spec 08](08-state-locking-and-lifecycle.md)) exists to fix, and Compose's own `down` already ignores profile scoping. `down` therefore enumerates **by the tool-owned label** (not by `--profile`) and removes the whole project stack, then drops all of that project's ref rows. (A future `stop --profile` for pause-without-teardown is out of scope; noted in [Q-PROFILE](../OPEN-QUESTIONS.md).)
- **Saga ([spec 08](08-state-locking-and-lifecycle.md)):** the active-profile set is computed in an early, pure phase and threaded through the remaining phases as immutable input, so a resumed `up` re-derives the identical set. All ref-row + network mutations stay inside the `gofrs/flock` lock.

## Verified constraints / gotchas
- **Compose profile activation is opt-in per service:** a service that declares **any** `profiles:` is **not started** unless one of those profiles is active; a service with **no** `profiles:` **always** starts. So tagging *some* services with `core` silently leaves untagged services running everywhere — make membership explicit and lint for "service in zero groups but expected in a slice".
- **`depends_on` across an inactive profile is dropped, not auto-activated.** Compose v2 silently ignores a `depends_on` edge whose target is in an inactive profile (older/edge cases error). We must **validate cross-profile dependencies at generate time** ([spec 02](02-templating-and-generation.md)): if active service `A depends_on B` and `B` is not in the active set, fail fast with a remediation ("add `B` to profile `X` or remove the dependency"), rather than shipping a compose file that quietly omits the edge.
- **`--profile` (CLI) vs `COMPOSE_PROFILES` (env):** the CLI flag is authoritative per-invocation but does **not** persist; `COMPOSE_PROFILES` leaks from the user's shell and would otherwise silently widen the active set. We pass `--profile` explicitly **and** set `COMPOSE_PROFILES` in the child env to the exact resolved set, so the two never disagree (and `compose exec` in hooks agrees).
- **`compose down` does not stop services in non-active profiles** — if a profile was active on a prior `up` and is inactive now, a plain `down` leaves those containers running. This is the same enumeration footgun as [spec 03](03-workspaces-and-shared-services.md): `down`/`gc` must enumerate containers by the **tool-owned label** with `All=true` and `oneoff=true` excluded, never trust the active `--profile` set, or stale containers and ref rows accumulate.
- **Shared services must never carry compose `profiles:`** — they live in the separate `devstack-shared` project and are selected only by the reference-graph walk + ref-count. A generate-time lint rejects any `profiles:` key on a shared-stack service.
- **Profile names share Compose's namespace constraints** (lowercase alnum + `-`/`_`); validate names at load so a name valid in YAML but rejected by `docker compose` doesn't fail deep in the saga.

## Acceptance criteria
- [ ] `up --profile frontend` starts only the services tagged `frontend` (across all projects) and only the shared services those services transitively `uses:` — `shared status` shows ref rows for exactly that set.
- [ ] `up` with no flag and `defaultProfile: core` set starts the `core` slice; with no `defaultProfile`, it starts all profile-less services + all explicitly-profiled-and-active services.
- [ ] `--profile a,b` and `--profile a --profile b` produce the identical active set (union, order-independent).
- [ ] An active service `depends_on` a service outside the active profile → generate-time error with a one-line remediation (not a silently dropped edge, not a runtime hang).
- [ ] Health gating ([spec 10](10-health-readiness-and-ordering.md)) waits only on active-profile services; `up --profile minimal` does not block on an unstarted service.
- [ ] `firstRun` ([spec 11](11-lifecycle-hooks.md)) for a service not in the active profile does **not** run and leaves no idempotency row; it runs on the first `up` that activates that service.
- [ ] `down` (no `--profile`) removes the whole project stack by tool-owned-label enumeration and drops all that project's ref rows, even for services started under a previously-active profile.
- [ ] A workspace `groups.<name>.services` entry naming a non-existent service → `file:line:col` config error ([spec 01](01-config-schema.md)).
- [ ] A `profiles:` key on a shared-stack service → generate-time lint error.
- [ ] With `memoryBudgetMB` set and active-profile declared `memoryMB` exceeding it, `up` prints a warning naming the offending services and suggesting `--profile minimal`; with no budget configured, no check runs.

## Dependencies / consumers
Consumes `internal/config` (the `profile → {project, service}` index + schema), the workspace **reference graph** and ref-counting from `internal/workspace` ([spec 03](03-workspaces-and-shared-services.md)), `internal/generate` (cross-profile `depends_on` lint, compose `profiles:` emission — [spec 02](02-templating-and-generation.md)), and the `up`/`down` saga + lock + ledger in `internal/orchestrate` / `internal/lock` / `internal/state` ([spec 08](08-state-locking-and-lifecycle.md)). Feeds `internal/health` ([spec 10](10-health-readiness-and-ordering.md)) and `internal/hooks` ([spec 11](11-lifecycle-hooks.md)) the active-service set. Reserves the schema hook for the memory budget in feature #10 / [spec 18](18-resource-limits-and-multi-arch.md). A **thinner v1** (~3–4 days): repo-level `profiles:` + the shared-graph walk + the cross-profile `depends_on` lint, with `defaultProfile = all` and no memory hook. **Full ~1.5w** adds workspace-level `groups`, the `minimal`/budget warning wiring, and the `COMPOSE_PROFILES`/hook-scoping consistency tests.

## Open questions
[Q-DAEMON](../OPEN-QUESTIONS.md) (no daemon → a stopped-but-not-downed profile cannot be auto-reaped; reconcile stays lazy). **New:** [Q-PROFILE](../OPEN-QUESTIONS.md) — **should profiles be defined per-repo, at the workspace level, or both, and what is the default-profile policy?** Per-repo membership maximizes repo portability (a repo's `devstack.yaml` carries its own slice tags) but no single repo can express a *cross-repo* slice like `frontend`; workspace `groups` express cross-repo slices but couple the workspace to service names inside repos it doesn't own. Recommendation: **both**, unioned by name (as specified above), with `defaultProfile` opt-in and `all` as the no-config default. Sub-question: should `down --profile`/`stop --profile` exist for pause-without-teardown, or does keeping `down` whole-project (recommended here) suffice for v1?
