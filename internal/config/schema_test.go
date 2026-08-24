package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// compileSchema loads one embedded schema through jsonschema/v6, which also
// proves the committed document is itself valid draft-2020-12.
func compileSchema(t *testing.T, kind SchemaKind) *jsonschema.Schema {
	t.Helper()
	raw, err := Schema(kind)
	if err != nil {
		t.Fatalf("Schema(%s): %v", kind, err)
	}
	name, _ := SchemaFilename(kind)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s is not valid JSON: %v", name, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, doc); err != nil {
		t.Fatalf("AddResource(%s): %v", name, err)
	}
	s, err := c.Compile(name)
	if err != nil {
		t.Fatalf("%s is not a valid JSON Schema: %v", name, err)
	}
	return s
}

// yamlToJSONValue reads a YAML file and re-encodes it through encoding/json so
// the value carries the exact Go types jsonschema/v6 expects (float64 numbers,
// map[string]any objects) rather than goccy's decode types.
func yamlToJSONValue(t *testing.T, path string) any {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var loose any
	if err := yaml.Unmarshal(src, &loose); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	encoded, err := json.Marshal(loose)
	if err != nil {
		t.Fatalf("re-encode %s: %v", path, err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return v
}

// TestSchemaRoundTrip is the D16 guard: every fixture the Go validator accepts
// must also validate against the published JSON Schema. If a struct gains a field
// and the schema does not, additionalProperties:false fails here — in the same PR
// that changed the struct.
func TestSchemaRoundTrip(t *testing.T) {
	// The Go path: this must load cleanly, or the fixture itself is broken.
	if _, err := LoadAt(filepath.Join("testdata", "valid")); err != nil {
		t.Fatalf("LoadAt(testdata/valid): %v", err)
	}

	wsSchema := compileSchema(t, SchemaWorkspace)
	projSchema := compileSchema(t, SchemaProject)

	var checked int
	root := filepath.Join("testdata", "valid")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		var schema *jsonschema.Schema
		switch filepath.Base(path) {
		case "workspace.yaml":
			schema = wsSchema
		case "devstack.yaml":
			schema = projSchema
		default:
			return nil
		}
		checked++
		if err := schema.Validate(yamlToJSONValue(t, path)); err != nil {
			t.Errorf("%s does not validate against the published schema:\n%v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if checked < 3 {
		t.Fatalf("expected at least 3 fixtures (1 workspace + 2 projects), checked %d", checked)
	}
}

// TestSchemaRejectsUnknownKeys pins additionalProperties:false, which is what
// makes the round-trip test above able to detect a struct that grew a field.
func TestSchemaRejectsUnknownKeys(t *testing.T) {
	projSchema := compileSchema(t, SchemaProject)
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(`{
		"apiVersion": "devstack/v1",
		"kind": "Project",
		"name": "api",
		"services": {"api": {"template": "node.next"}},
		"totallyNotAField": true
	}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := projSchema.Validate(doc); err == nil {
		t.Fatal("expected an unknown top-level key to be rejected")
	}
}

// TestSchemaKindsResolve asserts every published kind has a readable document and
// that ParseSchemaKind accepts both the kind name and the file name.
func TestSchemaKindsResolve(t *testing.T) {
	for _, kind := range SchemaKinds() {
		if _, err := Schema(kind); err != nil {
			t.Errorf("Schema(%s): %v", kind, err)
		}
		got, err := ParseSchemaKind(string(kind))
		if err != nil || got != kind {
			t.Errorf("ParseSchemaKind(%q) = %q, %v; want %q", kind, got, err, kind)
		}
	}
	if _, err := ParseSchemaKind("nope"); err == nil {
		t.Error("expected an unknown kind to error")
	}
	if got, err := ParseSchemaKind("devstack.yaml"); err != nil || got != SchemaProject {
		t.Errorf("ParseSchemaKind(devstack.yaml) = %q, %v; want project", got, err)
	}
}
