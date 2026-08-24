package ide

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/schemas"
)

// TestSchemaURLResolvesToARealFile is the anti-404 guard. Every URL the editor
// configs emit must end in a schema document that actually exists in this repo —
// the previous implementation pointed at schemas/devstack.schema.json at a time
// when no schemas/ directory existed at any tag.
func TestSchemaURLResolvesToARealFile(t *testing.T) {
	g := &Generator{schemaVersion: "1.2.3"}
	for _, kind := range config.SchemaKinds() {
		url := g.schemaURL(kind)
		idx := strings.LastIndex(url, "/")
		if idx < 0 {
			t.Fatalf("malformed schema URL %q", url)
		}
		name := url[idx+1:]
		if _, err := schemas.FS.ReadFile(name); err != nil {
			t.Errorf("schema URL for %s points at %q, which is not in schemas/: %v", kind, name, err)
		}
	}
}

// TestSchemaRefPinsReleasesAndFallsBackOtherwise: a release build pins its own
// tag so the schema can never drift from the binary that wrote the modeline; any
// other stamp (the "dev" default, a snapshot) has no tag on GitHub and must fall
// back to main instead of emitting a URL that 404s.
func TestSchemaRefPinsReleasesAndFallsBackOtherwise(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"1.2.3", "v1.2.3"},
		{"v1.2.3", "v1.2.3"},
		{"0.26.0", "v0.26.0"},
		{"1.2.3-rc.1", "v1.2.3-rc.1"},
		{"dev", "main"},
		{"", "main"},
		{"snapshot", "main"},
	} {
		g := &Generator{schemaVersion: tc.version}
		if got := g.schemaRef(); got != tc.want {
			t.Errorf("schemaRef(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
}

// TestSchemaURLPerKind pins that workspace.yaml and devstack.yaml get DIFFERENT
// schemas — they did not before, so workspace.yaml was validated against the
// project schema and every field would have reported as unknown.
func TestSchemaURLPerKind(t *testing.T) {
	g := &Generator{schemaVersion: "1.2.3"}
	ws, proj := g.schemaURL(config.SchemaWorkspace), g.schemaURL(config.SchemaProject)
	if ws == proj {
		t.Fatalf("workspace and project must not share a schema URL (both %q)", ws)
	}
	if !strings.HasSuffix(ws, "/workspace.schema.json") {
		t.Errorf("workspace URL = %q, want it to end in workspace.schema.json", ws)
	}
	if !strings.HasSuffix(proj, "/devstack.schema.json") {
		t.Errorf("project URL = %q, want it to end in devstack.schema.json", proj)
	}
}
