package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// This file is the Kafka (Redpanda) Provisioner (spec 29 §messaging — the NATIVE
// default for topics + Kafka streams). It creates/inspects/deletes TOPICS
// (kind=topic and kind=stream both map to a partitioned Kafka topic) using the
// PURE-GO franz-go admin client (kadm) in-process (CGO-free, so it stays inside the
// single static binary — never an external `rpk`/`kafka-topics` tool). The client
// sits behind the small KafkaAdmin seam so unit/race tests run without a live
// broker: inject Kafka.Factory with a fake. Topic names are transparently
// PROJECT-PREFIXED for tenant isolation; callers escape with --no-prefix. Idempotent
// (an already-existing topic is a no-op, not an error).

// KafkaAdmin is the subset of Kafka admin ops the provisioner uses, behind a
// mockable seam. The default impl wraps franz-go/kadm; tests inject a fake.
type KafkaAdmin interface {
	CreateTopic(ctx context.Context, name string, partitions int, replicas int) error
	DeleteTopic(ctx context.Context, name string) error
	ListTopics(ctx context.Context) ([]string, error)
	Close() error
}

// KafkaFactory builds a KafkaAdmin for a resolved Target (the external
// 127.0.0.1 listener the kafka template advertises). Injectable so the provisioner
// is broker-free in tests; nil selects the real franz-go client.
type KafkaFactory func(ctx context.Context, t Target) (KafkaAdmin, error)

// Kafka is the kafka/Redpanda Provisioner. Factory nil → the real client.
type Kafka struct {
	Factory KafkaFactory
}

var _ Provisioner = Kafka{}

// Engine reports the shared-template capability this provisioner serves (the kafka
// template's `provides: kafka`).
func (Kafka) Engine() string { return "kafka" }

// Kinds are the resource kinds this provisioner can create.
func (Kafka) Kinds() []string { return []string{"topic", "stream", "acl"} }

func (k Kafka) admin(ctx context.Context, t Target) (KafkaAdmin, error) {
	if k.Factory != nil {
		return k.Factory(ctx, t)
	}
	return defaultKafkaAdmin(ctx, t)
}

// kafkaAddr is the host-reachable broker address (the advertised external listener).
func kafkaAddr(t Target) string { return fmt.Sprintf("%s:%d", t.Host, t.Port) }

// Ensure idempotently creates a Kafka topic (kind=topic or kind=stream). partitions
// (default 1) and replicas (default 1) come from Params. An already-existing topic
// is a no-op. Returns the connection facts.
func (k Kafka) Ensure(ctx context.Context, t Target, r Resource) (Attrs, error) {
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	a, err := k.admin(ctx, t)
	if err != nil {
		return nil, err
	}
	defer func() { _ = a.Close() }()

	partitions := intParam(r.Params, "partitions")
	if partitions <= 0 {
		partitions = 1
	}
	replicas := intParam(r.Params, "replicas")
	if replicas <= 0 {
		replicas = 1
	}
	if err := a.CreateTopic(ctx, name, partitions, replicas); err != nil {
		if !isTopicExists(err) {
			return nil, fmt.Errorf("create kafka topic %q: %w", name, err)
		}
	}
	return k.attrs(t, name, partitions), nil
}

// Drop deletes the topic. Idempotent: a missing topic is not an error. Never
// touches the shared broker container.
func (k Kafka) Drop(ctx context.Context, t Target, r Resource) error {
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	a, err := k.admin(ctx, t)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	if err := a.DeleteTopic(ctx, name); err != nil {
		if isTopicNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete kafka topic %q: %w", name, err)
	}
	return nil
}

// Preflight verifies the broker is reachable (list topics). Absence degrades only
// the topic/stream verbs, never `up`.
func (k Kafka) Preflight(ctx context.Context, t Target) error {
	a, err := k.admin(ctx, t)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	_, err = a.ListTopics(ctx)
	return err
}

// ListTopics returns the tenant's topic names (prefix-filtered). A lock-free read.
func (k Kafka) ListTopics(ctx context.Context, t Target, prefix string) ([]string, error) {
	a, err := k.admin(ctx, t)
	if err != nil {
		return nil, err
	}
	defer func() { _ = a.Close() }()
	all, err := a.ListTopics(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range all {
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, "__") {
			continue // skip internal topics
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (Kafka) attrs(t Target, name string, partitions int) Attrs {
	return Attrs{
		"endpoint":   kafkaAddr(t),
		"host":       sharedHost(t.Instance),
		"port":       "9092",
		"topic":      name,
		"partitions": fmt.Sprintf("%d", partitions),
		"broker":     kafkaAddr(t),
	}
}

// isTopicExists reports the idempotent "topic already exists" case.
func isTopicExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// isTopicNotFound reports the "unknown topic" family so Drop stays idempotent.
func isTopicNotFound(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "unknown topic") || strings.Contains(e, "does not exist")
}

// --- default franz-go-backed KafkaAdmin ---------------------------------------

type kafkaAdmin struct {
	cl  *kgo.Client
	adm *kadm.Client
}

func defaultKafkaAdmin(_ context.Context, t Target) (KafkaAdmin, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(kafkaAddr(t)))
	if err != nil {
		return nil, fmt.Errorf("connect kafka %s: %w", kafkaAddr(t), err)
	}
	return &kafkaAdmin{cl: cl, adm: kadm.NewClient(cl)}, nil
}

func (a *kafkaAdmin) CreateTopic(ctx context.Context, name string, partitions int, replicas int) error {
	_, err := a.adm.CreateTopic(ctx, int32(partitions), int16(replicas), nil, name)
	return err
}

func (a *kafkaAdmin) DeleteTopic(ctx context.Context, name string) error {
	resp, err := a.adm.DeleteTopics(ctx, name)
	if err != nil {
		return err
	}
	if r, ok := resp[name]; ok && r.Err != nil {
		return r.Err
	}
	return nil
}

func (a *kafkaAdmin) ListTopics(ctx context.Context) ([]string, error) {
	td, err := a.adm.ListTopics(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for name := range td {
		out = append(out, name)
	}
	return out, nil
}

func (a *kafkaAdmin) Close() error {
	if a.cl != nil {
		a.cl.Close()
	}
	return nil
}
