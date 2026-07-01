package resource

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// fakeKafka is an in-memory KafkaAdmin recording topics + their partition counts.
type fakeKafka struct {
	topics     map[string]int // name → partitions
	createErr  error
	closed     bool
	createCall int
}

func newFakeKafka() *fakeKafka { return &fakeKafka{topics: map[string]int{}} }

func (f *fakeKafka) CreateTopic(_ context.Context, name string, partitions, _ int) error {
	f.createCall++
	if f.createErr != nil {
		return f.createErr
	}
	if _, ok := f.topics[name]; ok {
		return errors.New("TOPIC_ALREADY_EXISTS: topic already exists")
	}
	f.topics[name] = partitions
	return nil
}
func (f *fakeKafka) DeleteTopic(_ context.Context, name string) error {
	if _, ok := f.topics[name]; !ok {
		return errors.New("UNKNOWN_TOPIC_OR_PARTITION: unknown topic")
	}
	delete(f.topics, name)
	return nil
}
func (f *fakeKafka) ListTopics(_ context.Context) ([]string, error) {
	out := []string{"__consumer_offsets"} // internal topic must be filtered out
	for n := range f.topics {
		out = append(out, n)
	}
	return out, nil
}
func (f *fakeKafka) Close() error { f.closed = true; return nil }

func kafkaTarget() Target { return Target{Instance: "kafka", Host: "127.0.0.1", Port: 49092} }

func kafkaWith(f *fakeKafka) Kafka {
	return Kafka{Factory: func(context.Context, Target) (KafkaAdmin, error) { return f, nil }}
}

func TestKafkaEngineAndKinds(t *testing.T) {
	k := Kafka{}
	if k.Engine() != "kafka" {
		t.Errorf("Engine() = %q, want kafka", k.Engine())
	}
	if !slices.Contains(k.Kinds(), "topic") || !slices.Contains(k.Kinds(), "stream") {
		t.Errorf("Kinds() = %v", k.Kinds())
	}
}

func TestKafkaEnsureTopicPartitions(t *testing.T) {
	tests := []struct {
		name     string
		res      Resource
		wantPart int
	}{
		{"default single partition", Resource{Engine: "kafka", Kind: "topic", Name: "web-events", Owner: "web"}, 1},
		{"explicit partitions via stream", Resource{Engine: "kafka", Kind: "stream", Name: "web-orders", Owner: "web", Params: map[string]any{"partitions": 6}}, 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeKafka()
			attrs, err := kafkaWith(f).Ensure(context.Background(), kafkaTarget(), tc.res)
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if f.topics[tc.res.Name] != tc.wantPart {
				t.Errorf("partitions = %d, want %d", f.topics[tc.res.Name], tc.wantPart)
			}
			if attrs["topic"] != tc.res.Name {
				t.Errorf("attrs[topic] = %q", attrs["topic"])
			}
		})
	}
}

func TestKafkaEnsureIdempotent(t *testing.T) {
	f := newFakeKafka()
	k := kafkaWith(f)
	ctx := context.Background()
	r := Resource{Engine: "kafka", Kind: "topic", Name: "web-events", Owner: "web"}
	for i := 0; i < 3; i++ {
		if _, err := k.Ensure(ctx, kafkaTarget(), r); err != nil {
			t.Fatalf("Ensure #%d: an existing topic must be a no-op, got %v", i, err)
		}
	}
	if len(f.topics) != 1 {
		t.Errorf("want a single topic after 3 ensures, got %v", f.topics)
	}
}

func TestKafkaDropIdempotentAndListFilters(t *testing.T) {
	f := newFakeKafka()
	k := kafkaWith(f)
	ctx := context.Background()
	for _, name := range []string{"web-a", "api-b"} {
		_, _ = k.Ensure(ctx, kafkaTarget(), Resource{Engine: "kafka", Kind: "topic", Name: name, Owner: "x"})
	}
	// List filters internal topics + applies the tenant prefix.
	got, err := k.ListTopics(ctx, kafkaTarget(), "web-")
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	if !slices.Equal(got, []string{"web-a"}) {
		t.Errorf("ListTopics(web-) = %v, want [web-a]", got)
	}
	// Drop is idempotent.
	if err := k.Drop(ctx, kafkaTarget(), Resource{Engine: "kafka", Kind: "topic", Name: "web-a", Owner: "web"}); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if err := k.Drop(ctx, kafkaTarget(), Resource{Engine: "kafka", Kind: "topic", Name: "web-a", Owner: "web"}); err != nil {
		t.Errorf("Drop of missing topic must be idempotent: %v", err)
	}
}
