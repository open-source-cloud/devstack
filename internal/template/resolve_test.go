package template

import (
	"strings"
	"testing"
	"testing/fstest"
)

// These tests use synthetic MapFS templates; the embedded built-ins are
// exercised by the generate package's acceptance suite.
func mapSource(files map[string]string) TemplateSource {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return NewFSSource(m)
}

// TestExtendsMergesServiceAndBuildTree is the core of spec 02 acceptance #1:
// a child template that extends a base renders a correctly merged service plus
// the inherited + child build files.
func TestExtendsMergesServiceAndBuildTree(t *testing.T) {
	src := mapSource(map[string]string{
		"base/template.yaml":    "params:\n  v: { default: \"1\" }\nservice:\n  image: \"base:[[ .params.v ]]\"\n  environment:\n    A: \"1\"\n",
		"base/build/Dockerfile": "FROM base\n",
		"leaf/template.yaml":    "extends: base\nservice:\n  environment:\n    B: \"2\"\n  command: [\"run\"]\n",
		"leaf/build/extra.sh":   "echo hi\n",
	})
	r, err := Resolve(src, "leaf", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Chain; len(got) != 2 || got[0] != "base" || got[1] != "leaf" {
		t.Errorf("chain = %v, want [base leaf]", got)
	}
	if r.Service["image"] != "base:1" {
		t.Errorf("image = %v (base value should render)", r.Service["image"])
	}
	env := r.Service["environment"].(map[string]any)
	if env["A"] != "1" || env["B"] != "2" {
		t.Errorf("environment not merged: %v", env)
	}
	// Build tree merges: base Dockerfile + leaf extra.sh.
	if _, ok := r.BuildFiles["Dockerfile"]; !ok {
		t.Error("inherited Dockerfile missing")
	}
	if _, ok := r.BuildFiles["extra.sh"]; !ok {
		t.Error("child build file missing")
	}
}

// TestChildOverridesParentBuildFile proves a child's same-path build file wins.
func TestChildOverridesParentBuildFile(t *testing.T) {
	src := mapSource(map[string]string{
		"base/template.yaml":    "service:\n  image: x\n",
		"base/build/Dockerfile": "FROM base\n",
		"leaf/template.yaml":    "extends: base\nservice:\n  image: y\n",
		"leaf/build/Dockerfile": "FROM leaf\n",
	})
	r, err := Resolve(src, "leaf", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(r.BuildFiles["Dockerfile"]); !strings.Contains(got, "FROM leaf") {
		t.Errorf("child Dockerfile should win, got %q", got)
	}
}

// TestRequiredParam is spec 02 acceptance #5 at the resolve layer.
func TestRequiredParam(t *testing.T) {
	src := mapSource(map[string]string{
		"t/template.yaml": "params:\n  name: { required: true }\nservice:\n  image: \"x:[[ .params.name ]]\"\n",
	})
	if _, err := Resolve(src, "t", nil); err == nil {
		t.Fatal("want a missing-required-param error")
	}
	if _, err := Resolve(src, "t", map[string]any{"name": "ok"}); err != nil {
		t.Fatalf("supplying the param should succeed: %v", err)
	}
}

func TestExtendsCycleDetected(t *testing.T) {
	src := mapSource(map[string]string{
		"a/template.yaml": "extends: b\nservice: { image: a }\n",
		"b/template.yaml": "extends: a\nservice: { image: b }\n",
	})
	if _, err := Resolve(src, "a", nil); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want a cycle error, got %v", err)
	}
}

func TestExportsAndPortInheritLeafWins(t *testing.T) {
	src := mapSource(map[string]string{
		"base/template.yaml": "provides: db\nexports: [host]\ndefaultPort: 1111\nservice: { image: x }\n",
		"leaf/template.yaml": "extends: base\ndefaultPort: 2222\nservice: { image: y }\n",
	})
	r, err := Resolve(src, "leaf", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Provides != "db" {
		t.Errorf("provides = %q, want db (inherited)", r.Provides)
	}
	if r.DefaultPort != 2222 {
		t.Errorf("defaultPort = %d, want 2222 (leaf wins)", r.DefaultPort)
	}
	if len(r.Exports) != 1 || r.Exports[0] != "host" {
		t.Errorf("exports = %v, want [host]", r.Exports)
	}
}

func TestUnknownTemplate(t *testing.T) {
	src := mapSource(map[string]string{"a/template.yaml": "service: { image: x }\n"})
	if _, err := Resolve(src, "missing", nil); err == nil {
		t.Fatal("want error for unknown template")
	}
}

// TestUnknownTemplateSuggests checks the actionable error (ARCHITECTURE §7.6):
// a typo names the absent template and lists the available ones.
func TestUnknownTemplateSuggests(t *testing.T) {
	src := mapSource(map[string]string{"postgres/template.yaml": "service: { image: x }\n"})
	_, err := src.Resolve("postgrez")
	if err == nil {
		t.Fatal("want error for typo'd template")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error %q should say not found and list available templates", err.Error())
	}
}

// TestPostInitMerge verifies the documented post-chain merge layer (spec 02):
// extends base → leaf template.yaml → post_init.yaml.
func TestPostInitMerge(t *testing.T) {
	src := mapSource(map[string]string{
		"t/template.yaml":  "service:\n  image: x\n  environment:\n    A: \"1\"\n",
		"t/post_init.yaml": "service:\n  environment:\n    B: \"2\"\n  command: [run]\nvolumes:\n  data: {}\n",
	})
	r, err := Resolve(src, "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := r.Service["environment"].(map[string]any)
	if env["A"] != "1" || env["B"] != "2" {
		t.Errorf("post_init env not merged: %v", env)
	}
	if _, ok := r.Volumes["data"]; !ok {
		t.Errorf("post_init volumes not merged: %v", r.Volumes)
	}
}

// TestJoinOnListParam covers the join helper accepting a YAML list param ([]any).
func TestJoinOnListParam(t *testing.T) {
	out, err := RenderText("t", []byte(`[[ join "," .params.items ]]`),
		map[string]any{"params": map[string]any{"items": []any{"a", "b", "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "a,b,c" {
		t.Errorf("join on []any = %q, want a,b,c", out)
	}
}
