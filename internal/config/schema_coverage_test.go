package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// schemaBinding ties one Go struct to the object in a published schema that is
// supposed to describe it. `at` is a slash path through the decoded schema
// document (properties/… , $defs/… , items, additionalProperties).
type schemaBinding struct {
	at  string
	typ reflect.Type
}

func t7[T any]() reflect.Type { var z T; return reflect.TypeOf(z) }

// workspaceBindings covers every struct reachable from config.Workspace.
var workspaceBindings = []schemaBinding{
	{"", t7[Workspace]()},
	{"properties/profiles", t7[Profiles]()},
	{"properties/groups/additionalProperties", t7[Group]()},
	{"properties/secrets", t7[Secrets]()},
	{"properties/secrets/properties/providers/items", t7[SecretProvider]()},
	{"properties/network", t7[Network]()},
	{"properties/network/properties/proxy", t7[Proxy]()},
	{"properties/network/properties/tunnel", t7[Tunnel]()},
	{"properties/backend", t7[BackendConfig]()},
	{"properties/shared/additionalProperties", t7[SharedSvc]()},
	{"properties/projects/items", t7[ProjectRef]()},
	{"$defs/resources", t7[Resources]()},
	{"$defs/hooks", t7[Hooks]()},
	{"$defs/hook", t7[Hook]()},
}

// projectBindings covers every struct reachable from config.Project.
var projectBindings = []schemaBinding{
	{"", t7[Project]()},
	{"$defs/service", t7[Service]()},
	{"$defs/env", t7[Env]()},
	{"$defs/env/properties/import/items", t7[Import]()},
	{"$defs/healthcheck", t7[Healthcheck]()},
	{"$defs/dependsOn", t7[DependsOn]()},
	{"$defs/resourceDecl", t7[ResourceDecl]()},
	{"$defs/task", t7[Task]()},
	{"$defs/resources", t7[Resources]()},
	{"$defs/hooks", t7[Hooks]()},
	{"$defs/hook", t7[Hook]()},
}

// TestSchemaCoversEveryField is the structural half of the D16 guard. The
// round-trip test only exercises fields the fixtures happen to use; this walks
// the structs themselves, so adding a `yaml:` field without adding it to the
// schema fails here — in the same PR that changed the struct.
func TestSchemaCoversEveryField(t *testing.T) {
	for _, tc := range []struct {
		kind     SchemaKind
		bindings []schemaBinding
	}{
		{SchemaWorkspace, workspaceBindings},
		{SchemaProject, projectBindings},
	} {
		raw, err := Schema(tc.kind)
		if err != nil {
			t.Fatalf("Schema(%s): %v", tc.kind, err)
		}
		var doc map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("decode %s: %v", tc.kind, err)
		}
		for _, b := range tc.bindings {
			node, ok := walkSchema(doc, b.at)
			if !ok {
				t.Errorf("%s: no schema object at %q (for %s)", tc.kind, b.at, b.typ)
				continue
			}
			assertFieldsCovered(t, string(tc.kind), b, node)
		}
	}
}

// assertFieldsCovered checks the binding in both directions: every yaml field is
// a schema property, and every schema property is a yaml field. The second half
// catches a field that was renamed or deleted in Go but left behind in the schema.
func assertFieldsCovered(t *testing.T, kind string, b schemaBinding, node map[string]any) {
	t.Helper()
	props, _ := node["properties"].(map[string]any)
	if props == nil {
		t.Errorf("%s at %q: object has no properties block (for %s)", kind, b.at, b.typ)
		return
	}
	// additionalProperties:false is what gives the schema its teeth.
	if ap, ok := node["additionalProperties"]; !ok || ap != false {
		t.Errorf("%s at %q: expected additionalProperties:false (for %s)", kind, b.at, b.typ)
	}

	want := yamlFields(b.typ)
	for _, f := range want {
		if _, ok := props[f]; !ok {
			t.Errorf("%s at %q: field %q of %s is missing from the schema", kind, b.at, f, b.typ)
		}
	}
	have := make(map[string]bool, len(want))
	for _, f := range want {
		have[f] = true
	}
	extra := make([]string, 0)
	for p := range props {
		if !have[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(extra)
	for _, p := range extra {
		t.Errorf("%s at %q: schema property %q has no matching field on %s", kind, b.at, p, b.typ)
	}
}

// yamlFields returns the yaml key of every exported field on a struct.
func yamlFields(typ reflect.Type) []string {
	out := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// walkSchema resolves a slash path through a decoded schema document. An empty
// path returns the root.
func walkSchema(doc map[string]any, path string) (map[string]any, bool) {
	node := doc
	if path == "" {
		return node, true
	}
	for _, seg := range strings.Split(path, "/") {
		next, ok := node[seg].(map[string]any)
		if !ok {
			return nil, false
		}
		node = next
	}
	return node, true
}
