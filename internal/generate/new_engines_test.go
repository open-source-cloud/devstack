package generate

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/templates"
)

// TestNewEngineTemplatesLint covers the engines added alongside the spec-28 set:
// stores, search, object storage and the developer-facing services.
//
// Each `wantIn` is deliberately the detail that would silently break the engine
// if it were dropped — an image that no longer exists, a healthcheck using a
// binary the image does not ship, or an env var without which the server refuses
// connections. Losing one of these produces a template that lints fine and fails
// on first run, which is exactly what this test exists to prevent.
func TestNewEngineTemplatesLint(t *testing.T) {
	src := template.NewFSSource(templates.FS)
	cases := []struct {
		name     string
		provides string
		port     int
		wantIn   string
		why      string
	}{
		{"valkey", "valkey", 6379, "valkey-cli",
			"valkey-cli is the only in-image probe; there is no curl"},
		{"mailpit", "mailpit", 1025, "MP_DATABASE",
			"without MP_DATABASE the volume silently persists nothing"},
		{"meilisearch", "meilisearch", 7700, "/meili_data",
			"the volume must be the directory, not the .ms file inside it"},
		{"etcd", "etcd", 2379, "ETCD_ADVERTISE_CLIENT_URLS",
			"setting listen URLs without advertise URLs is a fatal startup error"},
		{"clickhouse", "clickhouse", 8123, "CLICKHOUSE_PASSWORD",
			"with no user/password the entrypoint disables network access entirely"},
		{"neo4j", "neo4j", 7687, "NEO4J_AUTH",
			"auth seeding only happens on first boot"},
		{"timescaledb", "timescaledb", 5432, "/home/postgres/pgdata",
			"this image is NOT docker-library/postgres; PGDATA lives elsewhere"},
		{"mosquitto", "mosquitto", 1883, "mosquitto_pub",
			"the image has no curl; mosquitto_pub speaks the real protocol"},
		{"keycloak", "keycloak", 8080, "KC_HEALTH_ENABLED",
			"port 9000 stays closed without it and the healthcheck never passes"},
		{"jaeger", "jaeger", 4317, "13133",
			"v2 moved the health port; v1's 14269 does not exist here"},
		{"consul", "consul", 8500, "agent",
			"the -dev agent is what enables the UI and default-allow ACLs"},
		{"opensearch", "opensearch", 9200, "discovery.type",
			"without single-node discovery the node waits forever for peers"},
		{"rustfs", "rustfs", 9000, "RUSTFS_ACCESS_KEY",
			"credentials are how the S3 API is reachable at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := template.Resolve(src, tc.name, nil)
			if err != nil {
				t.Fatalf("resolve %s: %v", tc.name, err)
			}
			if res.Provides != tc.provides {
				t.Errorf("provides = %q, want %q", res.Provides, tc.provides)
			}
			if res.DefaultPort != tc.port {
				t.Errorf("defaultPort = %d, want %d", res.DefaultPort, tc.port)
			}
			if len(res.Exports) == 0 {
				t.Errorf("%s declares no exports, so no project can import from it", tc.name)
			}
			compose, err := LintResolved(tc.name, res)
			if err != nil {
				t.Fatalf("lint %s: %v", tc.name, err)
			}
			if !strings.Contains(string(compose), tc.wantIn) {
				t.Errorf("compose is missing %q (%s):\n%s", tc.wantIn, tc.why, compose)
			}
		})
	}
}

// TestEngineTemplatesDeclareNoBuild pins the engine/app split: generation rejects
// a shared service with a build context, so an engine template that grows one
// would fail at `up` rather than here.
func TestEngineTemplatesDeclareNoBuild(t *testing.T) {
	src := template.NewFSSource(templates.FS)
	for _, name := range []string{
		"valkey", "mailpit", "meilisearch", "etcd", "clickhouse", "neo4j",
		"timescaledb", "mosquitto", "keycloak", "jaeger", "consul",
		"opensearch", "rustfs",
	} {
		res, err := template.Resolve(src, name, nil)
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		if _, ok := res.Service["build"]; ok {
			t.Errorf("%s declares build:, but a shared engine must be image-based", name)
		}
		if _, ok := res.Service["image"]; !ok {
			t.Errorf("%s declares no image:", name)
		}
	}
}
