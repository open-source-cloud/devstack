package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runSchema drives `config schema` through the real cobra tree.
func runSchema(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd(Options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"config", "schema"}, args...))
	err := root.Execute()
	return out.String(), err
}

// TestConfigSchemaDefaultsToProject pins the default kind and asserts the output
// is a usable JSON Schema document, not a summary.
func TestConfigSchemaDefaultsToProject(t *testing.T) {
	out, err := runSchema(t)
	if err != nil {
		t.Fatalf("config schema: %v\n%s", err, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v, want draft 2020-12", doc["$schema"])
	}
	props, _ := doc["properties"].(map[string]any)
	if _, ok := props["services"]; !ok {
		t.Errorf("project schema should describe `services`; got properties %v", props)
	}
}

// TestConfigSchemaWorkspaceKind covers the other kind and the filename alias.
func TestConfigSchemaWorkspaceKind(t *testing.T) {
	for _, kind := range []string{"workspace", "workspace.yaml"} {
		out, err := runSchema(t, "--kind", kind)
		if err != nil {
			t.Fatalf("config schema --kind %s: %v\n%s", kind, err, out)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("--kind %s: output is not JSON: %v", kind, err)
		}
		props, _ := doc["properties"].(map[string]any)
		if _, ok := props["shared"]; !ok {
			t.Errorf("--kind %s: workspace schema should describe `shared`", kind)
		}
	}
}

// TestConfigSchemaUnknownKind asserts the error names the available kinds rather
// than failing opaquely.
func TestConfigSchemaUnknownKind(t *testing.T) {
	out, err := runSchema(t, "--kind", "nope")
	if err == nil {
		t.Fatalf("expected an error for an unknown kind; got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "project") || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("error should list the available kinds, got: %v", err)
	}
}

// TestConfigSchemaNeedsNoWorkspace is the property that makes this useful to an
// agent: the schema describes the file format, so it must work in an empty dir —
// exactly when someone is about to author their first devstack.yaml.
func TestConfigSchemaNeedsNoWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVSTACK_WORKSPACE", "")
	if _, err := runSchema(t); err != nil {
		t.Fatalf("config schema must not require a workspace: %v", err)
	}
}
