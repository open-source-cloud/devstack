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
		{"postgres", exposePort{5432, "postgres", "", 0, true}, pgParams, 5432, "postgres://admin:s3cret@127.0.0.1:5432/postgres?sslmode=disable"},
		{"postgres", exposePort{5432, "postgres", "", 0, true}, nil, 5432, "postgres://devstack:devstack@127.0.0.1:5432/postgres?sslmode=disable"},
		{"mysql", exposePort{3306, "mysql", "", 0, true}, nil, 3306, "mysql://devstack:devstack@127.0.0.1:3306/devstack"},
		{"mariadb", exposePort{3306, "mariadb", "", 0, true}, nil, 3306, "mysql://devstack:devstack@127.0.0.1:3306/devstack"},
		{"mongodb", exposePort{27017, "mongodb", "", 0, true}, nil, 27017, "mongodb://devstack:devstack@127.0.0.1:27017/?authSource=admin"},
		{"cassandra", exposePort{9042, "cassandra", "", 0, true}, nil, 9042, "127.0.0.1:9042"},
		{"arangodb", exposePort{8529, "arangodb", "", 0, true}, nil, 8529, "http://127.0.0.1:8529"},
		{"redis", exposePort{6379, "redis", "", 0, true}, nil, 6379, "redis://127.0.0.1:6379"},
		{"minio", exposePort{9000, "s3", "", 0, true}, nil, 9000, "http://127.0.0.1:9000"},
		{"localstack", exposePort{4566, "aws", "", 0, true}, nil, 4566, "http://127.0.0.1:4566"},
		{"nats", exposePort{8222, "monitor", "", 0, false}, nil, 8222, "http://127.0.0.1:8222"},
		{"nats", exposePort{4222, "nats", "", 0, true}, nil, 4222, "nats://127.0.0.1:4222"},
		{"kafka", exposePort{19092, "kafka", "", 0, true}, nil, 49092, "127.0.0.1:49092"},
		{"rabbitmq", exposePort{15672, "management", "", 0, false}, nil, 15672, "http://127.0.0.1:15672"},
		{"rabbitmq", exposePort{5672, "amqp", "", 0, true}, nil, 5672, "amqp://devstack@127.0.0.1:5672"},
	}
	for _, tc := range cases {
		if got := connectionURL(tc.engine, tc.ep, tc.params, tc.port); got != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.engine, tc.ep.label, got, tc.want)
		}
	}
}

// TestExposeUsesStandardPorts locks in the unification: there is exactly ONE host
// port per engine — the well-known one — and it equals the in-container port. That
// is what lets provisioning/reset/snapshot and `expose` share a single overlay
// instead of two fighting bands. Kafka is the deliberate exception: its broker
// advertises a fixed 127.0.0.1:49092 external listener, so its host base is 49092
// while the container port is 19092.
func TestExposeUsesStandardPorts(t *testing.T) {
	for engine, ports := range exposeEngines {
		for _, ep := range ports {
			if engine == "kafka" {
				continue
			}
			if ep.base != ep.container {
				t.Errorf("engine %q port %q: host base %d must equal container port %d (standard-port unification)",
					engine, ep.label, ep.base, ep.container)
			}
		}
	}
	// Every provisionable engine must have a PRIMARY expose port, since provisioning
	// now resolves its host-reachable admin endpoint from that single overlay.
	for _, engine := range []string{"postgres", "redis", "minio", "nats", "kafka", "localstack"} {
		if _, ok := primaryExposePort(engine); !ok {
			t.Errorf("engine %q has no primary expose port — provisioning cannot reach it", engine)
		}
	}
	// Within a SINGLE engine, its ports must not share a base (else a two-port
	// engine like minio/nats/rabbitmq would self-collide on the same host port).
	for engine, ports := range exposeEngines {
		seen := map[int]string{}
		for _, ep := range ports {
			if prev, ok := seen[ep.base]; ok {
				t.Errorf("engine %q reuses expose base %d across purposes %q and %q", engine, ep.base, prev, ep.purpose)
			}
			seen[ep.base] = ep.purpose
		}
	}
	// Across DIFFERENT engines the base MAY repeat on purpose: two engines that
	// speak the same wire protocol want the same well-known port (mysql/mariadb on
	// 3306, localstack/ministack on 4566). That is safe because the ledger's
	// AllocatePort skips every already-allocated port (AllocatedPorts spans all
	// owners), so a lone engine lands on the standard port and, when both are
	// exposed, the second transparently deconflicts to base+1.
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
