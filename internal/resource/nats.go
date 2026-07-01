package resource

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// This file is the NATS (JetStream) Provisioner (spec 29 §messaging — the NATIVE
// default for queues + streams). It creates/inspects/deletes JetStream STREAMS
// (kind=stream) and durable CONSUMERS acting as work-QUEUES (kind=queue) using the
// PURE-GO nats.go client in-process (CGO-free, so it stays inside the single static
// binary — never an external `nats` tool). The client sits behind the small
// NatsAdmin seam so unit/race tests run without a live NATS: inject NATS.Factory
// with a fake. Stream/consumer names are transparently PROJECT-PREFIXED for tenant
// isolation on the shared broker; callers escape with --no-prefix. Idempotent
// (CreateOrUpdateStream/Consumer are upserts).

// StreamSpec is the engine-agnostic stream intent the seam receives (a NATS stream
// with limits/work-queue retention). It never leaks a nats type so tests are trivial.
type StreamSpec struct {
	Name     string        // the tenant-scoped stream name
	Subjects []string      // subjects the stream binds (default "<name>.>")
	WorkType bool          // work-queue retention (a queue) vs limits (a stream)
	MaxAge   time.Duration // retention window (0 → unlimited)
	Replicas int           // replica count (0/1 → single)
}

// ConsumerSpec is the durable-consumer intent (a NATS "queue" binds a durable
// consumer to a work-queue stream).
type ConsumerSpec struct {
	Durable string // the durable consumer name
}

// NatsAdmin is the subset of JetStream admin ops the provisioner uses, behind a
// mockable seam. The default impl wraps nats.go/jetstream; tests inject a fake.
type NatsAdmin interface {
	EnsureStream(ctx context.Context, s StreamSpec) error
	DeleteStream(ctx context.Context, name string) error
	ListStreams(ctx context.Context) ([]string, error)
	EnsureConsumer(ctx context.Context, stream string, c ConsumerSpec) error
	DeleteConsumer(ctx context.Context, stream, durable string) error
	Close() error
}

// NatsFactory builds a NatsAdmin for a resolved Target (the 127.0.0.1 overlay
// endpoint). Injectable so the provisioner is endpoint-free in tests; nil selects
// the real nats.go client.
type NatsFactory func(ctx context.Context, t Target) (NatsAdmin, error)

// NATS is the nats JetStream Provisioner. Factory nil → the real client.
type NATS struct {
	Factory NatsFactory
}

var _ Provisioner = NATS{}

// Engine reports the shared-template capability this provisioner serves (the nats
// template's `provides: nats`).
func (NATS) Engine() string { return "nats" }

// Kinds are the resource kinds this provisioner can create.
func (NATS) Kinds() []string { return []string{"stream", "queue", "consumer"} }

// natsURL is the loopback admin endpoint for the overlay-published instance.
func natsURL(t Target) string { return fmt.Sprintf("nats://%s:%d", t.Host, t.Port) }

func (n NATS) admin(ctx context.Context, t Target) (NatsAdmin, error) {
	if n.Factory != nil {
		return n.Factory(ctx, t)
	}
	return defaultNatsAdmin(ctx, t)
}

// natsSubjects derives the stream subjects from Params["subjects"] (comma list)
// or the default "<name>.>" wildcard.
func natsSubjects(name string, params map[string]any) []string {
	if s := paramStr(params, "subjects"); s != "" {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{name + ".>"}
}

// Ensure idempotently provisions a JetStream stream (kind=stream) or a work-queue
// stream + durable consumer (kind=queue). Returns the connection facts.
func (n NATS) Ensure(ctx context.Context, t Target, r Resource) (Attrs, error) {
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	a, err := n.admin(ctx, t)
	if err != nil {
		return nil, err
	}
	defer func() { _ = a.Close() }()

	spec := StreamSpec{
		Name:     name,
		Subjects: natsSubjects(name, r.Params),
		WorkType: r.Kind == "queue",
		MaxAge:   durationParam(r.Params, "retention"),
		Replicas: intParam(r.Params, "replicas"),
	}
	if err := a.EnsureStream(ctx, spec); err != nil {
		return nil, fmt.Errorf("ensure nats stream %q: %w", name, err)
	}
	attrs := n.attrs(t, name, spec.Subjects)
	if r.Kind == "queue" {
		if err := a.EnsureConsumer(ctx, name, ConsumerSpec{Durable: name}); err != nil {
			return nil, fmt.Errorf("ensure nats consumer %q: %w", name, err)
		}
		attrs["consumer"] = name
	}
	return attrs, nil
}

// Drop removes the stream (which also removes its consumers). Idempotent: a missing
// stream is not an error. Never touches the shared broker container.
func (n NATS) Drop(ctx context.Context, t Target, r Resource) error {
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	a, err := n.admin(ctx, t)
	if err != nil {
		return err
	}
	defer func() { _ = a.Close() }()
	if err := a.DeleteStream(ctx, name); err != nil {
		if isNatsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete nats stream %q: %w", name, err)
	}
	return nil
}

// Preflight verifies the endpoint is reachable (connect + close). Absence degrades
// only the queue/stream verbs, never `up`.
func (n NATS) Preflight(ctx context.Context, t Target) error {
	a, err := n.admin(ctx, t)
	if err != nil {
		return err
	}
	return a.Close()
}

// ListStreams returns the tenant's stream names (prefix-filtered). A lock-free read.
func (n NATS) ListStreams(ctx context.Context, t Target, prefix string) ([]string, error) {
	a, err := n.admin(ctx, t)
	if err != nil {
		return nil, err
	}
	defer func() { _ = a.Close() }()
	all, err := a.ListStreams(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range all {
		if prefix != "" && !strings.HasPrefix(s, prefix) {
			continue
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

func (NATS) attrs(t Target, name string, subjects []string) Attrs {
	return Attrs{
		"endpoint": natsURL(t),
		"host":     sharedHost(t.Instance),
		"port":     "4222",
		"stream":   name,
		"subjects": strings.Join(subjects, ","),
	}
}

// isNatsNotFound reports the "stream not found" family so Drop stays idempotent.
func isNatsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if err == jetstream.ErrStreamNotFound || err == jetstream.ErrConsumerNotFound {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// --- default nats.go-backed NatsAdmin -----------------------------------------

type natsAdmin struct {
	nc *nats.Conn
	js jetstream.JetStream
}

func defaultNatsAdmin(_ context.Context, t Target) (NatsAdmin, error) {
	nc, err := nats.Connect(natsURL(t), nats.Timeout(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("connect nats %s: %w", natsURL(t), err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("open jetstream: %w", err)
	}
	return &natsAdmin{nc: nc, js: js}, nil
}

func (a *natsAdmin) EnsureStream(ctx context.Context, s StreamSpec) error {
	cfg := jetstream.StreamConfig{
		Name:      s.Name,
		Subjects:  s.Subjects,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    s.MaxAge,
	}
	if s.WorkType {
		cfg.Retention = jetstream.WorkQueuePolicy
	}
	if s.Replicas > 1 {
		cfg.Replicas = s.Replicas
	}
	_, err := a.js.CreateOrUpdateStream(ctx, cfg)
	return err
}

func (a *natsAdmin) DeleteStream(ctx context.Context, name string) error {
	return a.js.DeleteStream(ctx, name)
}

func (a *natsAdmin) ListStreams(ctx context.Context) ([]string, error) {
	lister := a.js.StreamNames(ctx)
	var out []string
	for name := range lister.Name() {
		out = append(out, name)
	}
	if err := lister.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *natsAdmin) EnsureConsumer(ctx context.Context, stream string, c ConsumerSpec) error {
	_, err := a.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:   c.Durable,
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	return err
}

func (a *natsAdmin) DeleteConsumer(ctx context.Context, stream, durable string) error {
	return a.js.DeleteConsumer(ctx, stream, durable)
}

func (a *natsAdmin) Close() error {
	if a.nc != nil {
		a.nc.Close()
	}
	return nil
}

// durationParam reads a Go duration param (e.g. "168h"), 0 when absent/invalid.
func durationParam(p map[string]any, key string) time.Duration {
	s := paramStr(p, key)
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}
