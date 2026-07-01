package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

func exposeModel() *config.Model {
	return &config.Model{
		Workspace: config.Workspace{
			Name: "w",
			Shared: map[string]config.SharedSvc{
				"postgres":   {Template: "postgres"},
				"minio":      {Template: "minio"},
				"localstack": {Template: "localstack"},
				"web":        {Template: "node.vite"}, // not an engine → not exposable
			},
		},
	}
}

func TestResolveExposeInstances_AllExposable(t *testing.T) {
	d := UpDeps{Model: exposeModel()}
	got, err := resolveExposeInstances(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"localstack", "minio", "postgres"} // sorted; "web" excluded
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("all = %v, want %v", got, want)
	}
}

func TestResolveExposeInstances_NamedAndErrors(t *testing.T) {
	d := UpDeps{Model: exposeModel()}
	got, err := resolveExposeInstances(d, []string{"minio", "postgres"})
	if err != nil || strings.Join(got, ",") != "minio,postgres" {
		t.Fatalf("named = %v err=%v", got, err)
	}
	if _, err := resolveExposeInstances(d, []string{"nope"}); err == nil {
		t.Error("unknown instance should error")
	}
	if _, err := resolveExposeInstances(d, []string{"web"}); err == nil {
		t.Error("non-engine shared service should be rejected as non-exposable")
	}
}

func TestWriteAndReadExposeOverlay(t *testing.T) {
	root := t.TempDir()
	pub := map[string][]publishedPort{
		"minio":    {{host: 59001, container: 9001}, {host: 59000, container: 9000}},
		"postgres": {{host: 55432, container: 5432}},
	}
	path, err := writeExposeOverlay(root, pub)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	// Instances sorted; minio's ports sorted by container (9000 before 9001).
	want := "services:\n" +
		"  minio:\n    ports:\n" +
		"      - \"127.0.0.1:59000:9000\"\n" +
		"      - \"127.0.0.1:59001:9001\"\n" +
		"  postgres:\n    ports:\n" +
		"      - \"127.0.0.1:55432:5432\"\n"
	if got != want {
		t.Errorf("overlay mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// exposedInstances must round-trip the service keys (and ignore the port lines).
	insts := exposedInstances(root)
	if strings.Join(insts, ",") != "minio,postgres" {
		t.Errorf("exposedInstances = %v, want [minio postgres]", insts)
	}
}

func TestExposedInstances_NoneWhenAbsent(t *testing.T) {
	if got := exposedInstances(t.TempDir()); got != nil {
		t.Errorf("no overlay → nil, got %v", got)
	}
}

func TestConnectionURL(t *testing.T) {
	pgParams := map[string]any{"rootUser": "admin", "rootPassword": "s3cret"}
	cases := []struct {
		engine string
		ep     exposePort
		params map[string]any
		port   int
		want   string
	}{
		{"postgres", exposePort{5432, "postgres", "", 0, true}, pgParams, 55432, "postgres://admin:s3cret@127.0.0.1:55432/postgres?sslmode=disable"},
		{"postgres", exposePort{5432, "postgres", "", 0, true}, nil, 55432, "postgres://devstack:devstack@127.0.0.1:55432/postgres?sslmode=disable"},
		{"redis", exposePort{6379, "redis", "", 0, true}, nil, 56379, "redis://127.0.0.1:56379"},
		{"minio", exposePort{9000, "s3", "", 0, true}, nil, 59000, "http://127.0.0.1:59000"},
		{"localstack", exposePort{4566, "aws", "", 0, true}, nil, 54566, "http://127.0.0.1:54566"},
		{"nats", exposePort{8222, "monitor", "", 0, false}, nil, 58222, "http://127.0.0.1:58222"},
		{"nats", exposePort{4222, "nats", "", 0, true}, nil, 54222, "nats://127.0.0.1:54222"},
		{"kafka", exposePort{19092, "kafka", "", 0, true}, nil, 49092, "127.0.0.1:49092"},
		{"rabbitmq", exposePort{15672, "management", "", 0, false}, nil, 55673, "http://127.0.0.1:55673"},
		{"rabbitmq", exposePort{5672, "amqp", "", 0, true}, nil, 55672, "amqp://devstack@127.0.0.1:55672"},
	}
	for _, tc := range cases {
		if got := connectionURL(tc.engine, tc.ep, tc.params, tc.port); got != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.engine, tc.ep.label, got, tc.want)
		}
	}
}

// TestExposePortsNeverCollideWithProvision is the load-bearing invariant: the
// expose overlay and the provision overlay must never publish the SAME host port
// (base) for the SAME engine, or applying both recreates the container with a
// duplicate binding. Kafka is the deliberate exception — its host clients MUST
// use the fixed advertised 49092, so it reuses the provision port.
func TestExposePortsNeverCollideWithProvision(t *testing.T) {
	provBase := map[string]int{}
	for engine, ov := range engineOverlays {
		provBase[engine] = ov.portBase
	}
	for engine, ports := range exposeEngines {
		for _, ep := range ports {
			if pb, ok := provBase[engine]; ok && ep.base == pb && engine != "kafka" {
				t.Errorf("engine %q expose base %d collides with provision base %d", engine, ep.base, pb)
			}
		}
	}
	// Every expose base must be unique across all engines/ports (no two services
	// fight for the same host port at allocation time either).
	seen := map[int]string{}
	for _, ports := range exposeEngines {
		for _, ep := range ports {
			if prev, ok := seen[ep.base]; ok && prev != ep.purpose {
				t.Errorf("expose base %d reused across purposes %q and %q", ep.base, prev, ep.purpose)
			}
			seen[ep.base] = ep.purpose
		}
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x")
	if fileExists(f) {
		t.Error("missing file → false")
	}
	_ = os.WriteFile(f, []byte("y"), 0o644)
	if !fileExists(f) {
		t.Error("present file → true")
	}
	if fileExists(dir) {
		t.Error("directory → false")
	}
}
