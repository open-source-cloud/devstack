# Spec 28 — Local-cloud shared-engine templates (LocalStack, NATS, Kafka, RabbitMQ, ministack)

**Module:** `templates/*` (+ `internal/scaffold`, `internal/generate`, `internal/workspace`) · **Milestone:** v2 · **Effort:** ~2.5w (new feature #18 — local-cloud engines; needs a FEATURES.md entry)

> Numbering note: specs currently stop at 25 and features at 17. This spec claims **28** and forward-references its imperative sibling [spec 29](29-resource-commands.md); specs **26/27** are unallocated gaps that must be reserved (or this pair renumbered to 26/27) before merge. FEATURES.md has no #18/#19 — this spec adds **#18** and the sibling adds **#19**.

## Purpose
Extend the shared-engine set beyond `postgres`/`redis`/`minio` so a workspace can stand up a **local cloud** — AWS emulation (LocalStack: S3/SQS/SNS/Kinesis/DynamoDB/Lambda), event streaming (NATS JetStream, Kafka), and AMQP (RabbitMQ) — all as warm, ref-counted, tenant-isolated instances on the one `devstack_shared` network, instead of a duplicate broker per repo. Each engine is **net-new** (zero existing references anywhere in the tree) and ships as exactly one `templates/<name>/template.yaml` declaring `provides:`/`exports:`/`defaultPort:`/`params:`/`service:` — the same shared-engine contract the three built-ins already satisfy. This spec defines **only the engine templates and their capability/version/host-port/healthcheck/tenant-scoping model**; the imperative `aws`/`nats`/`kafka`/`amqp` resource commands that create buckets/queues/streams *inside* these engines are the sibling [spec 29](29-resource-commands.md), and the data lifecycle (`db snapshot/restore`) is [spec 15](15-db-snapshot-restore.md). Templates land first because [spec 29](29-resource-commands.md) has nothing to provision against until the engines exist.

## Decisions
- **One template per engine, capability-keyed.** `provides:` is the ledger `engine` column ([spec 08](08-state-locking-and-lifecycle.md)); instances are keyed `(engine, major)` exactly like `shared-postgres-18` ([spec 03](03-workspaces-and-shared-services.md) §version-conflict). New capabilities: `aws` (LocalStack), `nats`, `kafka`, `amqp` (RabbitMQ). The DNS alias is `shared-<name>` (`shared-localstack`, `shared-nats`, `shared-kafka`, `shared-rabbitmq`) via the existing `sharedAlias`/`sharedAttr` resolver (`internal/generate/resolver.go`).
- **`exports:` carries only non-secret attrs** (`host`, `port`, plus engine extras like `endpoint`, `region`, `monitorPort`/`adminPort`/`mgmtPort`). Anything secret (`password`, `secretkey`, `secret`, `token`) is **rejected as an inline `${ref}`** by `secretAttrs` (`resolver.go:16`) and must flow through `env.import` as valueless per-service env keys filled at compose-up ([spec 04](04-secrets.md) §7.5 coupling) — no plaintext on disk, asserted by the existing determinism/secret CI test.
- **Resolver extension is part of this spec.** `host`/`port`/`endpoint`/`region` resolve from data the resolver already has (the DNS alias and the instance's single in-network `defaultPort` in `sharedPort map[string]int`, plus the `region` param). But `monitorPort`/`adminPort`/`mgmtPort` are **secondary** container ports (8222/9644/15672), which the resolver does **not** track today — it carries only one port per shared service. Exporting them requires a small per-template **export-attr → port lookup** (a static map from the template's declared secondary ports), not a literal hardcode in `sharedAttr`. This is the one non-trivial `internal/generate` change; see Open questions.
- **Kafka = Redpanda, not Apache Kafka.** Redpanda is a single C++ static-binary, Kafka-API-compatible broker with built-in schema registry and no JVM/ZooKeeper/KRaft-quorum ceremony — dramatically lighter and faster to a healthy state for local dev. We still `provides: kafka` (the capability is the wire protocol, not the implementation) so `${ref:workspace.shared.kafka}` and the [spec 29](29-resource-commands.md) `rpk`-driven commands stay implementation-agnostic. (See gotchas for why plain `apache/kafka` KRaft is the worse local default.)
- **Host-port policy per engine follows [spec 03](03-workspaces-and-shared-services.md): default no published port** (DNS over `devstack_shared`). A `127.0.0.1`-only host port is published **only** when a host-side tool must reach the engine — and that publish is an **up-time compose overlay** (`writeProvisionOverlay` → `.devstack/shared/compose.provision.yaml`, the pattern provisioning already uses for `pg-provision` port 45432), never the deterministic `generate` output. Each engine that needs host reachability gets its own `FreeHostPort` *purpose* + base (see table). Note the live signature is `FreeHostPort(ctx, owner, purpose, base)` (owner = the shared alias, e.g. `shared-localstack`), self-locking inside the flock — same call shape as `internal/orchestrate/provision.go`.
- **LocalStack edition = community** (`localstack/localstack`, Apache-2.0), services selected via the `SERVICES` env param. Pro/`localstack-pro` is owner-opt-in via the `image`/`authToken` params, never default (it needs a paid token through `env.import`).
- **Tenant scoping is per-engine and prefix-based** where the engine namespace is global. Bucket names (LocalStack S3 / MinIO), Kinesis stream names, DynamoDB table names, SQS/SNS names, and Kafka topics are **flat global namespaces inside one instance** — every resource [spec 29](29-resource-commands.md) creates is **prefixed by the consumer project** (`<proj>-<name>`) and recorded in the `provisioned(ctx, project, kind, name)` ledger via `RecordProvisioned` (`kind` is free-text — `bucket`/`queue`/`stream`/`topic`/`table`/`vhost` need **no migration**, joining the existing `db`/`role`/`bucket`/`redis_index` kinds). Project A can never name-collide with or reap project B's resources.
- **`ministack` is a second AWS-emulation engine (owner-decided).** It is the [ministack.org](https://ministack.org/) container image — a lighter LocalStack alternative — authored as a normal `templates/ministack/template.yaml` declaring `provides: aws` (same capability as LocalStack, so the [spec 29](29-resource-commands.md) `aws`/`s3`/`queue` verbs are backend-agnostic). A workspace picks one AWS-emulation engine by template name; both key the ledger as `engine=aws`. Its exact image tag / edge port / `SERVICES` knob / health endpoint must be confirmed against ministack.org before authoring (zero references in the tree today).
- **No new pure-Go runtime SDK.** `pgx/v5` stays the only in-process runtime SDK. Every new engine tool (`awslocal`/`aws`, `nats`, `rpk`, `rabbitmqadmin`) is an **external binary shelled out behind an `internal/` interface** with a mock and a `CmdError(cmd+code+stderr)` — the `docker`/`git`/[spec 15](15-db-snapshot-restore.md) `Dumper` discipline — and is an `info`-level `doctor` probe (absence degrades only the matching [spec 29](29-resource-commands.md) verbs, never blocks `up`). The probes themselves are introduced by [spec 29](29-resource-commands.md) alongside the verbs that use them; the templates here add **zero** Go deps and leave the `CGO_ENABLED=0` static binary untouched.

### Capability / version / host-port matrix

| Template | `provides` | DNS alias | `defaultPort` (in-network) | Host port? (purpose / base) | Stateful volume | Healthcheck |
|---|---|---|---|---|---|---|
| `localstack` | `aws` | `shared-localstack` | 4566 (edge) | **yes** — host AWS SDK/`aws` CLI (`localstack-host` / 44566) | `localstackdata:/var/lib/localstack` | `curl -sf localhost:4566/_localstack/health \| grep running` |
| `nats` | `nats` | `shared-nats` | 4222 (client); 8222 monitor | **yes** for host `nats` CLI (`nats-host` / 44222); monitor 8222 host-opt | `natsdata:/data` (JetStream) | `wget -qO- localhost:8222/healthz` |
| `kafka` (Redpanda) | `kafka` | `shared-kafka` | 9092 (Kafka API); 9644 admin | **yes** for host `rpk`/clients (`kafka-host` / 49092) | `kafkadata:/var/lib/redpanda/data` | `rpk cluster health -x admin.hosts=localhost:9644` |
| `rabbitmq` | `amqp` | `shared-rabbitmq` | 5672 (AMQP); 15672 mgmt UI | mgmt UI host-opt (`rabbitmq-mgmt` / 45672) | `rabbitmqdata:/var/lib/rabbitmq` | `rabbitmq-diagnostics -q ping` |

> The advertised-listener subtlety (Kafka/Redpanda, NATS routes) is why these need **two listeners** — see gotchas. Host ports are `127.0.0.1`-bound and ledger-allocated **inside the flock** ([spec 03](03-workspaces-and-shared-services.md) §port-allocation) via `FreeHostPort(ctx, owner, purpose, base)`, unioned with Docker-published bindings.

## LocalStack — `templates/localstack/template.yaml`
```yaml
schemaVersion: 1
description: "Shared LocalStack AWS-emulation engine, reached over the shared network at shared-localstack:4566."
provides: aws
exports: [host, port, endpoint, region]
defaultPort: 4566
params:
  image:
    type: string
    default: "localstack/localstack:3"
    description: "LocalStack community image; pin a major for (engine,major) ledger keying."
  services:
    type: string
    default: "s3,sqs,sns,kinesis,dynamodb"
    description: "Comma list for the SERVICES env (lazy-loads only these for fast, light startup)."
  region:
    type: string
    default: "us-east-1"
service:
  image: "[[ .params.image ]]"
  restart: unless-stopped
  environment:
    SERVICES: "[[ .params.services ]]"
    DEBUG: "0"
    AWS_DEFAULT_REGION: "[[ .params.region ]]"
    # Persist across restarts (community persistence is best-effort; see gotchas).
    PERSISTENCE: "1"
  volumes:
    - "localstackdata:/var/lib/localstack"
  healthcheck:
    # /_localstack/health returns per-service readiness; gate on the edge being up.
    test: ["CMD-SHELL", "curl -sf http://localhost:4566/_localstack/health | grep -q running"]
    interval: 10s
    timeout: 5s
    retries: 8
    start_period: 20s
volumes:
  localstackdata: {}
```
- **In-network:** a consumer sets `AWS_ENDPOINT_URL=http://shared-localstack:4566` (resolved via `${ref:workspace.shared.localstack.endpoint}` → `http://shared-localstack:4566`; `region` → the param). **Host-side** ([spec 29](29-resource-commands.md) `aws`/`awslocal`): the `localstack-host`/44566 overlay publishes `127.0.0.1:44566:4566`, and the resource command targets `--endpoint-url http://127.0.0.1:44566`. Credentials for emulation are the canonical dummy pair (`test`/`test`) — **not secret**, so they may be plain `environment:` on the consumer, but a generated-secret policy can still route them via `env.import`.
- **`endpoint` and `region` are new export attrs**, both non-secret and resolvable from data the resolver already has: `sharedAttr` gains `case "endpoint"` → `"http://" + sharedAlias(name) + ":" + sharedPort[name]` and `case "region"` → the instance's `region` param.

## NATS (JetStream) — `templates/nats/template.yaml`
```yaml
schemaVersion: 1
description: "Shared NATS engine with JetStream, reached over the shared network at shared-nats:4222."
provides: nats
exports: [host, port, monitorPort]
defaultPort: 4222
params:
  version:
    type: string
    default: "2"
    description: "NATS major version image tag (the (engine,major) key)."
service:
  image: "nats:[[ .params.version ]]"
  restart: unless-stopped
  # -js enables JetStream (streams/queues/KV); -m 8222 enables the monitoring port.
  command: ["-js", "-sd", "/data", "-m", "8222"]
  volumes:
    - "natsdata:/data"
  healthcheck:
    # The container ships no curl; use the bundled wget against the /healthz monitor route.
    test: ["CMD-SHELL", "wget -qO- http://localhost:8222/healthz || exit 1"]
    interval: 10s
    timeout: 5s
    retries: 5
volumes:
  natsdata: {}
```
- JetStream gives durable streams (event sourcing) and queue groups (work queues) — the two patterns [spec 29](29-resource-commands.md) `nats stream/consumer` provisions, prefixed `<proj>-<name>`. Monitoring on 8222 is in-network by default (`monitorPort` export, which needs the secondary-port resolver lookup) and host-published only on request.

## Kafka (Redpanda) — `templates/kafka/template.yaml`
```yaml
schemaVersion: 1
description: "Shared Kafka-compatible broker (Redpanda), reached over the shared network at shared-kafka:9092."
provides: kafka
exports: [host, port, adminPort]
defaultPort: 9092
params:
  image:
    type: string
    default: "redpandadata/redpanda:v24.2.7"
    description: "Single-binary Kafka-API broker; pin a version for (engine,major) keying."
service:
  image: "[[ .params.image ]]"
  restart: unless-stopped
  command:
    - "redpanda"
    - "start"
    - "--mode=dev-container"           # single-node, no quorum ceremony
    - "--smp=1"
    - "--default-log-level=warn"
    # TWO advertised listeners: in-network clients use the DNS alias; host tools
    # use 127.0.0.1. A single advertised listener is the #1 Kafka-local footgun.
    - "--kafka-addr=internal://0.0.0.0:9092,external://0.0.0.0:19092"
    - "--advertise-kafka-addr=internal://shared-kafka:9092,external://127.0.0.1:49092"
  volumes:
    - "kafkadata:/var/lib/redpanda/data"
  healthcheck:
    test: ["CMD-SHELL", "rpk cluster health -x admin.hosts=localhost:9644 | grep -q 'Healthy:.*true'"]
    interval: 10s
    timeout: 5s
    retries: 8
    start_period: 15s
volumes:
  kafkadata: {}
```
- **In-network** clients connect to `shared-kafka:9092` (the `internal` advertised listener). **Host** tools/`rpk` reach `127.0.0.1:49092` via the `external` listener — the `kafka-host`/49092 overlay publishes `127.0.0.1:49092:19092`. The two-listener split is mandatory: a broker advertises the address it tells clients to reconnect on after bootstrap, so one listener can satisfy either in-network DNS **or** host loopback, never both.
- `rpk` is the [spec 29](29-resource-commands.md) host tool for `kafka topic/acl`; topics are prefixed `<proj>-<name>` and ledger-recorded `kind=topic`.

## RabbitMQ — `templates/rabbitmq/template.yaml`
```yaml
schemaVersion: 1
description: "Shared RabbitMQ (AMQP) engine with the management UI, reached at shared-rabbitmq:5672."
provides: amqp
exports: [host, port, mgmtPort]
defaultPort: 5672
params:
  version:
    type: string
    default: "3"
    description: "RabbitMQ major (the (engine,major) key); the -management tag adds the UI."
  user:
    type: string
    default: devstack
service:
  image: "rabbitmq:[[ .params.version ]]-management"
  restart: unless-stopped
  environment:
    RABBITMQ_DEFAULT_USER: "[[ .params.user ]]"
    # Password is secret → injected via env.import at compose-up, never inline.
    RABBITMQ_DEFAULT_PASS:
  volumes:
    - "rabbitmqdata:/var/lib/rabbitmq"
  healthcheck:
    test: ["CMD-SHELL", "rabbitmq-diagnostics -q ping"]
    interval: 15s
    timeout: 10s
    retries: 5
    start_period: 30s
volumes:
  rabbitmqdata: {}
```
- `RABBITMQ_DEFAULT_PASS` is emitted as a **valueless per-service env key** (the `environment: [NAME]` form) and filled from the secrets provider at compose-up — the [spec 04](04-secrets.md) coupling. The management UI (15672) is in-network by default (`mgmtPort` export), host-published only on request (`rabbitmq-mgmt`/45672). Per-project isolation = a project-prefixed vhost (`<proj>`) + scoped user, provisioned by [spec 29](29-resource-commands.md) `amqp vhost`, `kind=vhost`.

## `ministack` — a second AWS-emulation engine (owner decision: it's an image, like LocalStack)
**Decided:** `ministack` is the [ministack.org](https://ministack.org/) container image — a real AWS-local-emulation runtime, a lighter sibling/alternative to LocalStack. So it is a **normal shared engine template**, NOT a preset: `templates/ministack/template.yaml` declaring `provides: aws` (the *same* capability as LocalStack, so a project's `uses: workspace.shared.<name>` and the [spec 29](29-resource-commands.md) `aws`/`s3`/`queue` verbs are backend-agnostic — they target whichever AWS-emulation engine the workspace ran). A workspace picks **one** AWS-emulation engine (LocalStack *or* ministack) by template name; both key the ledger as `engine=aws` so only one `(aws, major)` instance is warm.
```yaml
# templates/ministack/template.yaml  (illustrative — confirm image tag/edge port/SERVICES knob against ministack.org docs)
schemaVersion: 1
description: "ministack — local AWS emulation (S3/SQS/SNS/…), a lighter LocalStack alternative."
provides: aws
exports: [endpoint, region]          # same export contract as localstack (host-side --endpoint-url + region)
defaultPort: 4566                    # CONFIRM: ministack's edge/gateway port
params:
  image:    { type: string, default: "ministack/ministack:latest" }   # CONFIRM exact repo/tag
  services: { type: string, default: "s3,sqs,sns" }                    # CONFIRM the service-selection env/knob
service:
  image: "[[ .params.image ]]"
  restart: unless-stopped
  environment: { SERVICES: "[[ .params.services ]]" }                  # CONFIRM env var name
  healthcheck: { test: ["CMD","curl","-fsS","http://localhost:4566/_localstack/health"], interval: 10s, timeout: 5s, retries: 10 }  # CONFIRM health endpoint
```
Because the AWS API surface is what matters, `ministack` reuses LocalStack's exact `endpoint`/`region` export attrs, the same `127.0.0.1` host-port overlay (own `FreeHostPort` purpose `aws-host`/base shared with localstack since only one is active), and the same project-prefixed bucket/queue naming for tenant isolation. **The exact image tag, edge port, service-selection knob, and health endpoint must be confirmed against ministack.org before this template is authored** (the recon found zero references anywhere; these are placeholders).

## Behavior
1. **Author/scaffold.** New built-in cloud engines are added in-tree under `templates/<name>/template.yaml` (the `go:embed` set). The user-facing authoring path is `template new --kind engine --name <name> --provides <cap> --port <n> --exports host,port,...` ([spec 23](23-template-authoring.md)); by default `template new` writes into the `$DEVSTACK_HOME/templates` **store** (pass `--dir templates` to seed an in-tree built-in). `internal/scaffold` rejects an empty `provides:`, so every shared engine here declares `provides:`+`defaultPort:`. Custom store copies under `$DEVSTACK_HOME/templates/<name>/` override the embedded built-in by name (the store chain ahead of `go:embed`).
2. **Reference resolution.** A consumer's `uses:`/`${ref:workspace.shared.<name>.<attr>}` resolves through `graphResolver.sharedAttr` (`resolver.go:82`): `host`→`shared-<name>`, `port`→`defaultPort`, plus the new non-secret `endpoint`/`region` (from alias+defaultPort+param) and `monitorPort`/`adminPort`/`mgmtPort` (from the new export-attr→secondary-port lookup). Any secret attr is rejected → must go through `env.import`.
3. **Generate (deterministic).** The compose model is built programmatically and validated by `compose-go/v2`; the engine joins `devstack_shared` with its `shared-<name>` alias; **no host port** appears in generated output. `writeIfChanged` + SHA-256 rebuild hash, byte-identical (CI determinism lane).
4. **Ledger keying.** On `up`, the engine registers as `shared-<engine>-<major>` keyed `(engine, major)` — `shared-postgres-18` style. A workspace pinning two majors of one engine gets two instances, ref-counted independently.
5. **Up saga (unchanged order).** `preflight → network → generate → secrets → trust → shared(health-gated) → provision(if targets>0) → hooks` ([spec 09](09-orchestration-and-onboarding.md)). The new engines are ordinary shared services in the `shared` phase; their healthchecks gate readiness ([spec 10](10-health-readiness-and-ordering.md)). **No new saga phase** is added by this spec.
6. **Host-reachability overlay (only when a host tool needs it).** When a host tool ([spec 29](29-resource-commands.md) resource verb, or an explicit host-port request) needs the engine reachable from the host, `FreeHostPort(ctx, sharedAlias(inst), "<engine>-host", base)` allocates **inside the flock**, and an up-time overlay `.devstack/shared/compose.provision.yaml` (written by `writeProvisionOverlay`) publishes `127.0.0.1:<port>:<container-port>` — leaving deterministic `generate` output untouched, exactly like `pg-provision` (45432). Because the resource commands are **standalone cobra commands** that mirror the provisionPhase body (lock → host-port overlay → engine tool → ledger → `LogEvent`), not new saga phases, they own writing/refreshing this overlay outside `up` too.
7. **Tenant-scoped provisioning** is [spec 29](29-resource-commands.md): every resource is created `<proj>-<name>`-prefixed via the engine's external tool behind an `internal/` interface, recorded `RecordProvisioned(ctx, project, kind, name)` under the flock, and the *ledger rows* are reaped by `db gc`'s `OrphanedProvisioned(active)` ([spec 13](13-doctor-diagnostics-and-teardown.md)) when the project is removed. (Reaping the row does not by itself delete the bucket/topic in the engine — the actual engine-side teardown is the [spec 29](29-resource-commands.md) tool's job, driven off the same rows.)

## Verified constraints / gotchas
- **Kafka: do NOT default to `apache/kafka` KRaft.** Plain Apache Kafka needs explicit `KAFKA_NODE_ID`/`CONTROLLER_QUORUM_VOTERS`/`PROCESS_ROLES` plus a `cluster-id` and a `kafka-storage format` init step before it will start as a single node — a long, fragile, JVM-heavy boot. **Redpanda `--mode=dev-container`** is a single static binary that is healthy in seconds with no quorum config. We keep `provides: kafka` (capability = wire protocol) so consumers/[spec 29](29-resource-commands.md) are implementation-agnostic.
- **Advertised listeners must be split (Kafka & NATS routes).** A broker hands clients the *advertised* address to reconnect on after bootstrap. One advertised address can serve in-network DNS (`shared-kafka:9092`) **or** host loopback (`127.0.0.1:49092`), never both — so the template declares **two** listeners (`internal`/`external`). The naive single-listener config works from inside the network and silently fails the host `rpk` (and vice-versa).
- **Host-side tools can NOT reach an in-network-only engine.** With the default no-published-port policy a shared engine is only on `devstack_shared`; any tool running **from the host** (the AWS SDK, `aws`/`awslocal`, `nats`, `rpk`, `rabbitmqadmin`, like `pgx` for Postgres) needs the `127.0.0.1` up-time overlay first. This is the same constraint that forces `pg-provision` to publish 45432 — the host pgx connection cannot resolve `shared-postgres`.
- **LocalStack health is per-service, not a flat 200.** `GET /_localstack/health` returns each service's state (`available`/`running`); a service in `SERVICES` is **lazy** and only flips to `running` on first use. Gate the healthcheck on the **edge** being up (`grep running`), not on every service, or the container never reports healthy until traffic arrives.
- **LocalStack persistence is best-effort in community.** `PERSISTENCE=1` + the `/var/lib/localstack` volume survives restarts on a best-effort basis; deep state durability is a Pro feature. Document it: treat LocalStack data as **recreatable**, and let [spec 29](29-resource-commands.md) re-create resources idempotently from the `provisioned` ledger rather than relying on the volume.
- **Bucket / stream / table / topic namespaces are FLAT and GLOBAL inside one instance** — identical to the MinIO bucket caveat ([spec 15](15-db-snapshot-restore.md)). Two projects both asking for `uploads` would collide. **Prefix every resource by project** (`<proj>-uploads`) and key the `provisioned` ledger by `(ctx, project, kind, name)` so tenant isolation holds and `db gc` reaps the right rows.
- **NATS image has no `curl`.** Healthcheck must use the bundled `wget` (or the `nats` binary if present) against `/healthz` on the `-m 8222` monitor port — a `CMD curl` test fails as "executable not found" and the engine never goes healthy.
- **RabbitMQ first boot is slow** (Erlang VM + Mnesia schema) — set `start_period: 30s` and use `rabbitmq-diagnostics -q ping` (not a TCP probe), or the saga's health gate flaps and aborts a healthy bring-up.
- **Secrets never inline.** `RABBITMQ_DEFAULT_PASS`, LocalStack Pro `authToken`, and any generated broker credential are `secretAttrs` (`resolver.go:16`) — emit valueless `environment: [NAME]` and pass the value via `exec.Cmd.Env` at compose-up (or push to a provider via the [spec 04](04-secrets.md) Pusher). The existing CI test asserting no secret value lands in a generated file must pass for these templates.
- **External binaries are `info`-level only.** `aws`/`awslocal`, `nats`, `rpk`, `rabbitmqadmin` are host tools behind `internal/` seams ([spec 29](29-resource-commands.md)); their absence degrades only the matching resource verbs, never `up` — the `mkcert`/`cloudflared`/`pg_dump` posture ([DECISIONS D11/D12](../DECISIONS.md), [spec 15](15-db-snapshot-restore.md)). The templates add **zero** Go deps; `CGO_ENABLED=0` static binary is untouched.
- **Never recreate a stateful shared engine.** A param change that would force-recreate `shared-kafka`/`shared-rabbitmq` (e.g. a volume-path or listener change) is gated by the never-recreate guard ([spec 03](03-workspaces-and-shared-services.md)) — it would drop every tenant's connections. Bumping the **major** spins a *new* `(engine, major)` instance instead of mutating the running one.
- **Determinism vs runtime.** Only **generated artifacts** are determinism-gated; the host-port overlay, ledger rows, and [spec 29](29-resource-commands.md) resource creation are runtime mutations and are explicitly **not** byte-compared — same split as `pg-provision`.

## Acceptance criteria
- [ ] `templates/localstack`, `templates/nats`, `templates/kafka`, `templates/rabbitmq` each pass `template lint` (non-empty `provides:`+`defaultPort:`, valid `service:`/`healthcheck:`) and `template test` golden render.
- [ ] A workspace with `uses: workspace.shared.localstack` brings up exactly one `shared-localstack` on `devstack_shared`, reachable in-network at `shared-localstack:4566`, with **no** host port in deterministic `generate` output.
- [ ] `${ref:workspace.shared.localstack.endpoint}` resolves to `http://shared-localstack:4566` and `.region` to the param; `${ref:...localstack.monitorPort}`-style secondary-port attrs resolve via the new export-attr→port lookup; a `${ref:...localstack.secret}` (or any `secretAttrs` attr) is **rejected** at generate time.
- [ ] Two projects pinning `kafka` major `24` vs `25` produce two ledger-keyed instances `shared-kafka-24`/`-25`, ref-counted independently ([spec 03](03-workspaces-and-shared-services.md) parity test).
- [ ] Each engine reports **healthy** under its declared healthcheck within `start_period`; the up saga's health gate passes (Redpanda via `rpk cluster health`, NATS via `/healthz`, RabbitMQ via `rabbitmq-diagnostics ping`, LocalStack via edge `running`).
- [ ] A host-reachability request publishes a `127.0.0.1`-only overlay port via `FreeHostPort(ctx, sharedAlias(inst), "<engine>-host", base)` allocated inside the flock; `generate` output is byte-identical with and without the overlay.
- [ ] Kafka template advertises two listeners; an in-network client reaches `shared-kafka:9092` and host `rpk` reaches `127.0.0.1:49092` in the same run.
- [ ] `RABBITMQ_DEFAULT_PASS` appears as a valueless per-service env key in generated compose; the secret value appears in **no** generated file (CI assertion).
- [ ] `ministack` is authored as an AWS-emulation engine template (`provides: aws`, the ministack.org image) once its image tag/edge port/`SERVICES` knob/health endpoint are confirmed against ministack.org; it is interchangeable with LocalStack by template name (both `engine=aws`).

## Dependencies / consumers
Consumes `internal/scaffold` (the `provides:`-required builder), `internal/template` (the `[[ ]]` engine + `go:embed`/store chain), `internal/generate` (`sharedAttr`/`sharedAlias` + the new non-secret export cases and the secondary-port lookup), `internal/workspace` (`(engine, major)` ledger keying, ref-counting/reconcile, `FreeHostPort(ctx, owner, purpose, base)`), `internal/state`+`internal/lock` (the `provisioned` free-text-`kind` ledger + flock), `internal/secrets` (valueless env injection for broker creds, [spec 04](04-secrets.md)). **Consumed by** [spec 29](29-resource-commands.md) (the `aws`/`nats`/`kafka`/`amqp` resource commands that provision buckets/queues/streams/topics inside these engines — the imperative sibling, with [spec 15](15-db-snapshot-restore.md) as the data-command precedent) and `internal/doctor` (the new `info`-level `bin.aws`/`bin.nats`/`bin.rpk`/`bin.rabbitmqadmin` probes, introduced with spec 29's verbs). **Thin (~1w):** LocalStack + NATS templates with health + host overlay + the new `endpoint`/`region` export attrs. **Full (~2.5w):** adds Kafka (Redpanda) + RabbitMQ, the secondary-port resolver lookup, the `ministack` AWS-emulation engine template (once ministack.org config is confirmed), and golden tests.

## Open questions
**Q-MINISTACK — RESOLVED (owner):** `ministack` is the [ministack.org](https://ministack.org/) container image — a real AWS-local-emulation runtime, a lighter alternative to LocalStack. It is authored as a normal AWS-emulation **engine template** (`provides: aws`), interchangeable with LocalStack by template name (both key the ledger `engine=aws`). **Remaining sub-task before authoring:** confirm the exact image repo/tag, edge/gateway port, service-selection env knob, and health endpoint against the ministack.org docs (the recon found zero references; spec uses placeholders). **Implementation note (not yet authored):** the first cloud-engine batch ships `localstack`/`nats`/`kafka`/`rabbitmq` only; `templates/ministack/` is deliberately **deferred** because its image tag/edge port/`SERVICES` knob/health endpoint are unconfirmed offline — authoring it with a guessed image would key the `aws` engine to a non-existent container. It lands once a maintainer confirms the ministack.org config; LocalStack already satisfies the `aws` capability in the meantime.
**Q-SECONDARY-PORTS** — how do `monitorPort`/`adminPort`/`mgmtPort` resolve, given the resolver tracks only one in-network port per shared service (`sharedPort map[string]int`)? **Recommendation:** add a small static per-template export-attr→port map in `internal/generate` (driven off the template's declared secondary ports), not a hardcode in `sharedAttr`. **Decision:** owner to confirm whether secondary-port exports ship in v2 or are deferred (host-published mgmt/monitor UIs work without a `${ref}` export).
**Q-KAFKA-RUNTIME** — Redpanda vs Apache Kafka KRaft as the `kafka` engine. **Recommendation:** Redpanda (single static binary, no JVM/quorum, healthy in seconds; `provides: kafka` keeps consumers implementation-agnostic). **Decision:** owner to confirm Redpanda as the default `kafka` image, with an `image:` param escape hatch to `apache/kafka` for parity testing.
**Q-LOCALSTACK-EDITION** — community default vs Pro opt-in. **Recommendation:** community (`localstack/localstack`, Apache-2.0) default; Pro only via owner-set `image`+`authToken` (token through `env.import`). **Decision:** owner to confirm community-only ships v2; Pro support is param-gated, not bundled.
**Q-SPEC-NUMBERING** — specs 26/27 are unallocated and this pair claims 28/29. **Decision:** owner to reserve 26/27 (or renumber this pair to 26/27) and add the FEATURES.md #18/#19 rows before merge.
**Q-DAEMON** ([../OPEN-QUESTIONS.md](../OPEN-QUESTIONS.md)) — no daemon → these engines stay warm with no autostop; `shared gc` reclaims zero-ref instances on demand (inherited, not re-argued here).