package resource

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestRedisEngineAndKinds(t *testing.T) {
	r := Redis{}
	if r.Engine() != "redis" {
		t.Errorf("Engine() = %q, want redis", r.Engine())
	}
	if !slices.Contains(r.Kinds(), "queue") || !slices.Contains(r.Kinds(), "topic") {
		t.Errorf("Kinds() = %v", r.Kinds())
	}
}

func TestRedisEnsureIsHintOnly(t *testing.T) {
	tgt := Target{Instance: "redis", Host: "127.0.0.1", Port: 46379}
	tests := []struct {
		kind     string
		wantHint string
	}{
		{"queue", "LPUSH"},
		{"topic", "PUBLISH"},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			attrs, err := Redis{}.Ensure(context.Background(), tgt,
				Resource{Engine: "redis", Kind: tc.kind, Name: "web-jobs", Owner: "web"})
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if attrs["key"] != "web-jobs" {
				t.Errorf("attrs[key] = %q, want web-jobs (tenant-prefixed)", attrs["key"])
			}
			if attrs["host"] != "shared-redis" {
				t.Errorf("attrs[host] = %q, want shared-redis", attrs["host"])
			}
			if !strings.Contains(attrs["hint"], tc.wantHint) {
				t.Errorf("hint = %q, want it to mention %q", attrs["hint"], tc.wantHint)
			}
		})
	}
}

func TestRedisDropAndPreflightNoop(t *testing.T) {
	tgt := Target{Instance: "redis", Host: "127.0.0.1", Port: 46379}
	if err := (Redis{}).Drop(context.Background(), tgt, Resource{Engine: "redis", Kind: "queue", Name: "web-jobs"}); err != nil {
		t.Errorf("Drop must be a no-op: %v", err)
	}
	if err := (Redis{}).Preflight(context.Background(), tgt); err != nil {
		t.Errorf("Preflight must succeed without a client: %v", err)
	}
}
