package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
)

// managedContainers is a small fixture: two project services + two shared engines.
func managedContainers() []docker.Container {
	return []docker.Container{
		{ID: "api1", Name: "devstack-shop-api-1", State: "running", Labels: map[string]string{
			generate.LabelManaged: "true", generate.LabelProject: "shop", generate.LabelService: "api"}},
		{ID: "web1", Name: "devstack-shop-web-1", State: "running", Labels: map[string]string{
			generate.LabelManaged: "true", generate.LabelProject: "shop", generate.LabelService: "web"}},
		{ID: "pg1", Name: "devstack-shared-postgres-1", State: "running", Labels: map[string]string{
			generate.LabelManaged: "true", generate.LabelShared: "postgres"}},
		{ID: "rd1", Name: "devstack-shared-redis-1", State: "running", Labels: map[string]string{
			generate.LabelManaged: "true", generate.LabelShared: "redis"}},
	}
}

func TestResolveLogTargets(t *testing.T) {
	client := &docker.MockClient{Containers: managedContainers()}
	tests := []struct {
		name     string
		filter   []string
		wantSvcs []string
	}{
		{"all", nil, []string{"api", "shared-postgres", "shared-redis", "web"}},
		{"by-service", []string{"api"}, []string{"api"}},
		{"by-shared-alias", []string{"shared-postgres"}, []string{"shared-postgres"}},
		{"by-bare-engine", []string{"redis"}, []string{"shared-redis"}},
		{"by-project", []string{"shop"}, []string{"api", "web"}},
		{"no-match", []string{"nope"}, nil},
		{"mixed", []string{"api", "shared-redis"}, []string{"api", "shared-redis"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets, err := resolveLogTargets(context.Background(), client, tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, tg := range targets {
				got = append(got, tg.Service)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantSvcs, ",") {
				t.Fatalf("targets = %v, want %v", got, tt.wantSvcs)
			}
		})
	}
}

// TestResolveLogTargetsSorted asserts deterministic ordering (sorted by service).
func TestResolveLogTargetsSorted(t *testing.T) {
	client := &docker.MockClient{Containers: managedContainers()}
	targets, err := resolveLogTargets(context.Background(), client, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(targets); i++ {
		if targets[i-1].Service > targets[i].Service {
			t.Fatalf("targets not sorted: %q before %q", targets[i-1].Service, targets[i].Service)
		}
	}
}

func TestLogRendererJSONShape(t *testing.T) {
	var buf bytes.Buffer
	r := &logRenderer{out: &buf, json: true}
	r.write(
		logTarget{Service: "api", Project: "shop", Container: "devstack-shop-api-1"},
		docker.LogLine{Stream: "stdout", TS: "2026-06-14T12:03:01.412Z", Text: "GET /healthz 200"},
	)
	var got logJSONLine
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not one JSON object per line: %v\n%s", err, buf.String())
	}
	want := logJSONLine{
		TS: "2026-06-14T12:03:01.412Z", Service: "api", Project: "shop",
		Stream: "stdout", Container: "devstack-shop-api-1", Line: "GET /healthz 200",
	}
	if got != want {
		t.Fatalf("json line = %+v, want %+v", got, want)
	}
	// The raw output must carry every contract key.
	for _, k := range []string{`"ts"`, `"service"`, `"project"`, `"stream"`, `"container"`, `"line"`} {
		if !strings.Contains(buf.String(), k) {
			t.Errorf("json output missing key %s: %s", k, buf.String())
		}
	}
}

// TestLogRendererPlainGutter checks the non-JSON, no-color gutter format.
func TestLogRendererPlainGutter(t *testing.T) {
	var buf bytes.Buffer
	r := &logRenderer{out: &buf, json: false, color: false}
	r.write(logTarget{Service: "api"}, docker.LogLine{Stream: "stdout", Text: "hello"})
	if got := buf.String(); got != "api │ hello\n" {
		t.Fatalf("plain gutter = %q", got)
	}
}

// TestStreamLogsJSON drives the full multiplex path over the mock: two services,
// each with seeded lines, into the JSON contract. Non-follow exits at EOF.
func TestStreamLogsJSON(t *testing.T) {
	client := &docker.MockClient{
		Containers: managedContainers(),
		Streams: map[string][]docker.LogLine{
			"api1": {{Stream: "stdout", Text: "api-a"}, {Stream: "stderr", Text: "api-b"}},
			"pg1":  {{Stream: "stdout", Text: "pg-a"}},
		},
	}
	targets, err := resolveLogTargets(context.Background(), client, []string{"api", "shared-postgres"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r := &logRenderer{out: &buf, json: true}
	if err := streamLogs(context.Background(), client, targets, docker.LogOptions{}, r); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 json lines, got %d:\n%s", len(lines), buf.String())
	}
	svcCount := map[string]int{}
	for _, l := range lines {
		var obj logJSONLine
		if err := json.Unmarshal([]byte(l), &obj); err != nil {
			t.Fatalf("line not valid json: %q", l)
		}
		svcCount[obj.Service]++
	}
	if svcCount["api"] != 2 || svcCount["shared-postgres"] != 1 {
		t.Fatalf("unexpected per-service counts: %v", svcCount)
	}
}

// TestStreamLogsUnreadableService verifies a per-service stream error is a
// one-line notice, not fatal, and the rest still stream.
func TestStreamLogsUnreadableService(t *testing.T) {
	client := &docker.MockClient{
		Containers: managedContainers(),
		StreamErr:  errNoneDriver,
	}
	targets, _ := resolveLogTargets(context.Background(), client, []string{"api"})
	var buf bytes.Buffer
	r := &logRenderer{out: &buf}
	if err := streamLogs(context.Background(), client, targets, docker.LogOptions{}, r); err != nil {
		t.Fatalf("streamLogs should not fail fatally: %v", err)
	}
	if !strings.Contains(buf.String(), "api:") {
		t.Fatalf("expected a per-service notice, got %q", buf.String())
	}
}

var errNoneDriver = errStub("configured logging driver does not support reading")

type errStub string

func (e errStub) Error() string { return string(e) }
