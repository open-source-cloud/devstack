package resource

import (
	"context"
	"slices"
	"testing"
	"time"
)

// fakeNats is an in-memory NatsAdmin: it records streams + consumers so tests
// assert create/list/delete + idempotency without a live NATS.
type fakeNats struct {
	streams   map[string]StreamSpec
	consumers map[string][]string // stream → durable names
	closed    bool
}

func newFakeNats() *fakeNats {
	return &fakeNats{streams: map[string]StreamSpec{}, consumers: map[string][]string{}}
}

func (f *fakeNats) EnsureStream(_ context.Context, s StreamSpec) error {
	f.streams[s.Name] = s // upsert → idempotent
	return nil
}
func (f *fakeNats) DeleteStream(_ context.Context, name string) error {
	delete(f.streams, name)
	delete(f.consumers, name)
	return nil
}
func (f *fakeNats) ListStreams(_ context.Context) ([]string, error) {
	var out []string
	for n := range f.streams {
		out = append(out, n)
	}
	return out, nil
}
func (f *fakeNats) EnsureConsumer(_ context.Context, stream string, c ConsumerSpec) error {
	for _, d := range f.consumers[stream] {
		if d == c.Durable {
			return nil // idempotent
		}
	}
	f.consumers[stream] = append(f.consumers[stream], c.Durable)
	return nil
}
func (f *fakeNats) DeleteConsumer(_ context.Context, stream, durable string) error { return nil }
func (f *fakeNats) Close() error                                                   { f.closed = true; return nil }

func natsTarget() Target {
	return Target{Instance: "nats", Host: "127.0.0.1", Port: 44222}
}

func natsWith(f *fakeNats) NATS {
	return NATS{Factory: func(context.Context, Target) (NatsAdmin, error) { return f, nil }}
}

func TestNatsEngineAndKinds(t *testing.T) {
	n := NATS{}
	if n.Engine() != "nats" {
		t.Errorf("Engine() = %q, want nats", n.Engine())
	}
	if !slices.Contains(n.Kinds(), "stream") || !slices.Contains(n.Kinds(), "queue") {
		t.Errorf("Kinds() = %v, want stream+queue", n.Kinds())
	}
}

func TestNatsEnsureStreamAndQueue(t *testing.T) {
	tests := []struct {
		name         string
		res          Resource
		wantWorkType bool
		wantConsumer bool
		wantMaxAge   time.Duration
	}{
		{
			name:       "stream limits retention with max-age",
			res:        Resource{Engine: "nats", Kind: "stream", Name: "web-orders", Owner: "web", Params: map[string]any{"retention": "168h"}},
			wantMaxAge: 168 * time.Hour,
		},
		{
			name:         "queue is a work-queue stream + durable consumer",
			res:          Resource{Engine: "nats", Kind: "queue", Name: "web-jobs", Owner: "web"},
			wantWorkType: true,
			wantConsumer: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNats()
			attrs, err := natsWith(f).Ensure(context.Background(), natsTarget(), tc.res)
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			s, ok := f.streams[tc.res.Name]
			if !ok {
				t.Fatalf("stream %q not created: %v", tc.res.Name, f.streams)
			}
			if s.WorkType != tc.wantWorkType {
				t.Errorf("WorkType = %v, want %v", s.WorkType, tc.wantWorkType)
			}
			if s.MaxAge != tc.wantMaxAge {
				t.Errorf("MaxAge = %v, want %v", s.MaxAge, tc.wantMaxAge)
			}
			if def := s.Subjects; len(def) == 0 || def[0] != tc.res.Name+".>" {
				t.Errorf("subjects = %v, want default %q", def, tc.res.Name+".>")
			}
			if tc.wantConsumer {
				if !slices.Contains(f.consumers[tc.res.Name], tc.res.Name) {
					t.Errorf("durable consumer not created: %v", f.consumers)
				}
				if attrs["consumer"] != tc.res.Name {
					t.Errorf("attrs[consumer] = %q, want %q", attrs["consumer"], tc.res.Name)
				}
			}
			if attrs["host"] != "shared-nats" {
				t.Errorf("host attr = %q, want shared-nats", attrs["host"])
			}
		})
	}
}

func TestNatsIdempotentAndPrefixIsolation(t *testing.T) {
	f := newFakeNats()
	n := natsWith(f)
	ctx := context.Background()
	// Two projects create a same-logical "events" stream; the tenant prefix keeps
	// them distinct so project A can never clobber B.
	for _, name := range []string{"web-events", "api-events", "web-events"} { // web-events twice → idempotent
		if _, err := n.Ensure(ctx, natsTarget(), Resource{Engine: "nats", Kind: "stream", Name: name, Owner: "x"}); err != nil {
			t.Fatalf("Ensure %q: %v", name, err)
		}
	}
	if len(f.streams) != 2 {
		t.Errorf("want 2 distinct streams (web-events, api-events), got %v", f.streams)
	}
}

func TestNatsDropIdempotent(t *testing.T) {
	f := newFakeNats()
	n := natsWith(f)
	ctx := context.Background()
	_, _ = n.Ensure(ctx, natsTarget(), Resource{Engine: "nats", Kind: "stream", Name: "web-orders", Owner: "web"})
	if err := n.Drop(ctx, natsTarget(), Resource{Engine: "nats", Kind: "stream", Name: "web-orders", Owner: "web"}); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, ok := f.streams["web-orders"]; ok {
		t.Error("stream not deleted")
	}
	// Dropping a missing stream is not an error.
	if err := n.Drop(ctx, natsTarget(), Resource{Engine: "nats", Kind: "stream", Name: "gone", Owner: "web"}); err != nil {
		t.Errorf("Drop of missing stream must be idempotent: %v", err)
	}
}

func TestNatsListStreamsPrefixFiltered(t *testing.T) {
	f := newFakeNats()
	n := natsWith(f)
	ctx := context.Background()
	for _, name := range []string{"web-a", "web-b", "api-c"} {
		_, _ = n.Ensure(ctx, natsTarget(), Resource{Engine: "nats", Kind: "stream", Name: name, Owner: "x"})
	}
	got, err := n.ListStreams(ctx, natsTarget(), "web-")
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if !slices.Equal(got, []string{"web-a", "web-b"}) {
		t.Errorf("ListStreams(web-) = %v, want [web-a web-b]", got)
	}
}
