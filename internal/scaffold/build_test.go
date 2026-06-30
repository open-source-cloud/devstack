package scaffold_test

import (
	"testing"
	"testing/fstest"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/scaffold"
	"github.com/open-source-cloud/devstack/internal/template"
)

// testSrc is a synthetic template source: two shared engines (one with a
// defaulted param), a non-shared app template, and an engine with a required
// default-less param.
func testSrc() template.TemplateSource {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	return template.NewFSSource(fstest.MapFS{
		"pg/template.yaml":    f("provides: postgres\nparams:\n  version:\n    type: string\n    default: \"16\"\nservice:\n  image: postgres\n"),
		"redis/template.yaml": f("provides: redis\nservice:\n  image: redis\n"),
		"app/template.yaml":   f("description: a buildable app\nservice:\n  image: app\n"),
		"need/template.yaml":  f("provides: thing\nparams:\n  token:\n    type: string\n    required: true\nservice:\n  image: thing\n"),
	})
}

func TestBuildWorkspace_ProvidesFilter(t *testing.T) {
	src := testSrc()
	if _, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w", Services: []scaffold.ServiceInput{{Engine: "pg"}}}); err != nil {
		t.Errorf("shared engine pg should be accepted: %v", err)
	}
	if _, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w", Services: []scaffold.ServiceInput{{Engine: "app"}}}); err == nil {
		t.Error("non-shared template app should be rejected")
	}
	if _, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w", Services: []scaffold.ServiceInput{{Engine: "nope"}}}); err == nil {
		t.Error("unknown engine should error")
	}
}

func TestBuildWorkspace_DropAtDefault(t *testing.T) {
	src := testSrc()
	cases := []struct {
		name       string
		params     map[string]string
		wantParams map[string]any // nil = no params section
	}{
		{"no params", nil, nil},
		{"version at default dropped", map[string]string{"version": "16"}, nil},
		{"version overridden kept", map[string]string{"version": "18"}, map[string]any{"version": "18"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w", Services: []scaffold.ServiceInput{{Engine: "pg", Params: tc.params}}})
			if err != nil {
				t.Fatal(err)
			}
			got := ws.Shared["pg"].Params
			if len(got) != len(tc.wantParams) {
				t.Fatalf("params = %v, want %v", got, tc.wantParams)
			}
			for k, v := range tc.wantParams {
				if got[k] != v {
					t.Errorf("param %q = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestBuildWorkspace_UnknownParam(t *testing.T) {
	_, err := scaffold.BuildWorkspace(testSrc(), scaffold.Inputs{Name: "w",
		Services: []scaffold.ServiceInput{{Engine: "pg", Params: map[string]string{"bogus": "x"}}}})
	if err == nil {
		t.Fatal("unknown param should error")
	}
}

func TestBuildWorkspace_RequiredFailFast(t *testing.T) {
	src := testSrc()
	if _, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w", Services: []scaffold.ServiceInput{{Engine: "need"}}}); err == nil {
		t.Error("missing required param should fail fast")
	}
	if _, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w",
		Services: []scaffold.ServiceInput{{Engine: "need", Params: map[string]string{"token": "abc"}}}}); err != nil {
		t.Errorf("required param supplied should succeed: %v", err)
	}
}

func TestBuildWorkspace_FromStoreSeedAndOverride(t *testing.T) {
	src := testSrc()
	seed := map[string]config.SharedSvc{
		"pg":    {Template: "pg", Params: map[string]any{"version": "16"}},
		"redis": {Template: "redis"},
	}
	// No explicit services: the seed is carried through verbatim.
	ws, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w", FromStore: seed})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ws.Shared["pg"]; !ok {
		t.Error("store seed pg missing")
	}
	if _, ok := ws.Shared["redis"]; !ok {
		t.Error("store seed redis missing")
	}
	// An explicit --service overrides the same-named seed (re-resolved params).
	ws2, err := scaffold.BuildWorkspace(src, scaffold.Inputs{Name: "w", FromStore: seed,
		Services: []scaffold.ServiceInput{{Engine: "pg", Params: map[string]string{"version": "18"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if v := ws2.Shared["pg"].Params["version"]; v != "18" {
		t.Errorf("explicit service should override seed: version = %v, want 18", v)
	}
}
