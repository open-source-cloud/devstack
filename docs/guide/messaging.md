# Messaging: queues, topics & streams

[devstack](../../README.md) · [Guide](./README.md) › Messaging

Provision tenant-scoped **queues**, pub/sub **topics**, and durable **streams**
on the messaging engines you already run as [shared services](shared-services.md)
— NATS, Kafka (Redpanda), RabbitMQ, or LocalStack (SQS/SNS). Every object is
created idempotently, named per project, and tracked in the ledger under the lock.

```bash
devstack queue create jobs                  # a work queue on the inferred engine
devstack topic create orders --engine kafka # a pub/sub topic on Kafka
devstack stream create events --partitions 6 --engine kafka
```

## Before you start: declare and start an engine

These commands operate on a **running shared engine**. Declare one in
`workspace.yaml` and bring it up first:

```yaml
# workspace.yaml
shared:
  nats:  { template: nats }
  kafka: { template: kafka }        # Redpanda, Kafka-compatible
  localstack: { template: localstack }   # SQS/SNS/etc.
```

```bash
devstack up            # network + shared engines online
```

If no matching engine is declared/up, the create call fails with a remediation
pointing you back here. See [shared services](shared-services.md) for the engine
catalog and lifecycle.

## Naming & the `<project>-` prefix

Like buckets, messaging objects are prefixed with the owning project name and a
hyphen (`my-api` → `my-api-jobs`). Pass `--no-prefix` to use the literal name.
The owning project defaults to the workspace's single project, else the first
alphabetically; override with `--project`.

---

## `queue` — work queues

Tenant-scoped queues on the shared **NATS / Redis / SQS** engines.

```bash
devstack queue create jobs
devstack queue create emails.fifo --engine sqs --fifo
devstack queue create tasks --engine sqs --dlq tasks-dead --max-receive 5
devstack queue list
devstack queue rm jobs --yes
```

### `queue create <name>`

| Flag | Default | Description |
|---|---|---|
| `--engine` | inferred | `nats` \| `sqs` \| `redis` |
| `--fifo` | false | SQS FIFO queue (appends `.fifo` to the name) |
| `--dlq` | "" | dead-letter queue name (SQS) |
| `--max-receive` | 0 | redrive `maxReceiveCount` before a message goes to the DLQ (SQS) |
| `--project` | "" | owning project |
| `--no-prefix` | false | use the literal name (skip the `<project>-` prefix) |

**Engine inference order:** `nats → redis → localstack`. The first of those
engines that is declared and up wins, unless you pass `--engine`. `--fifo`,
`--dlq`, and `--max-receive` are SQS features (via LocalStack).

### `queue list` / `queue rm`

| Command | Flags |
|---|---|
| `queue list` | `--project`, `--all` (every queue, not just this project's) |
| `queue rm <name>` | `--project`, `--engine`, `--yes` (required for `--json`), `--no-prefix` |

`queue list` is lock-free (a read-only snapshot). `queue rm` is destructive and
prompts for confirmation interactively.

---

## `topic` — pub/sub topics

Tenant-scoped topics on the shared **Kafka / NATS / SNS** engines.

```bash
devstack topic create orders
devstack topic create notifications --engine sns --subscribe email-inbox
devstack topic list
devstack topic rm orders --yes
```

### `topic create <name>`

| Flag | Default | Description |
|---|---|---|
| `--engine` | inferred | `sns` \| `kafka` \| `nats` |
| `--subscribe` | "" | an SQS queue to subscribe to the topic (SNS → SQS fan-out) |
| `--project` | "" | owning project |
| `--no-prefix` | false | use the literal name |

**Engine inference order:** `kafka → nats → localstack`. `--subscribe` wires an
SNS→SQS fan-out and applies to the LocalStack (SNS) engine.

### `topic list` / `topic rm`

| Command | Flags |
|---|---|
| `topic list` | `--project`, `--all` |
| `topic rm <name>` | `--project`, `--engine`, `--yes`, `--no-prefix` |

---

## `stream` — durable streams

Tenant-scoped durable streams on the shared **NATS / Kafka** engines.

```bash
devstack stream create events --engine nats --retention 168h
devstack stream create clickstream --engine kafka --partitions 12 --replicas 3
devstack stream list
devstack stream rm events --yes
```

### `stream create <name>`

| Flag | Default | Description |
|---|---|---|
| `--engine` | inferred | `nats` \| `kafka` |
| `--partitions` | 0 | partition count — **Kafka only** |
| `--replicas` | 0 | replication factor — **Kafka only** |
| `--retention` | "" | retention as a Go duration (e.g. `168h`) |
| `--project` | "" | owning project |
| `--no-prefix` | false | use the literal name |

**Engine inference order:** `nats → kafka`. `--partitions` and `--replicas` are
Kafka concepts and are **rejected for NATS** — a NATS stream that receives either
flag errors out. Use `--retention` for either engine.

### `stream list` / `stream rm`

| Command | Flags |
|---|---|
| `stream list` | `--project`, `--all` |
| `stream rm <name>` | `--project`, `--engine`, `--yes`, `--no-prefix` |

---

## Declarative alternative

You rarely need to run these by hand. The same objects can be declared in a
project's `resources:` block and provisioned automatically at `up`:

```yaml
# devstack.yaml
resources:
  - { uses: workspace.shared.nats,  kind: queue,  name: jobs }
  - { uses: workspace.shared.kafka, kind: topic,  name: orders }
  - { uses: workspace.shared.kafka, kind: stream, name: events, params: { partitions: 6 } }
```

Both paths call the same provisioners and the same ledger under the same lock.
See [the resource layer](resources.md) for the declarative model.

---

## Recipes

**A NATS work queue for a background worker**

```bash
devstack up                      # nats declared + online
devstack queue create jobs       # → my-api-jobs on NATS
```

**SQS FIFO with a dead-letter queue (via LocalStack)**

```bash
devstack queue create tasks-dead --engine sqs
devstack queue create tasks --engine sqs --fifo \
  --dlq tasks-dead --max-receive 5
devstack aws -- sqs list-queues   # inspect with the host aws CLI
```

**Kafka topic with an explicit partition count on the stream**

```bash
devstack topic  create orders     --engine kafka
devstack stream create clickstream --engine kafka --partitions 12 --replicas 3
```

**Fan out an SNS topic to an SQS queue**

```bash
devstack queue create email-inbox --engine sqs
devstack topic create notifications --engine sns --subscribe email-inbox
```

## See also

- [Shared services](shared-services.md) — declare and run the messaging engines
- [The resource layer](resources.md) — declare queues/topics/streams in `resources:`
- [Object storage](object-storage.md) — the sibling `s3` group (also `<project>-` prefixed)
- [Command reference](command-reference.md) — every command, terse

---

◀ [Object storage](object-storage.md) · [Guide index](./README.md) · [The resource layer](resources.md) ▶
