package generate

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/template"
)

func modelWith(projects map[string]map[string]config.Service) *config.Model {
	m := &config.Model{Workspace: config.Workspace{Name: "w"}, Projects: map[string]config.Project{}}
	for pname, svcs := range projects {
		m.Projects[pname] = config.Project{Name: pname, Services: svcs}
	}
	return m
}

// TestCollisionLint_DuplicateName — the same bare service name in two projects
// would collide on the shared network (ARCHITECTURE §4 guardrail).
func TestCollisionLint_DuplicateName(t *testing.T) {
	m := modelWith(map[string]map[string]config.Service{
		"a": {"web": {Template: "t"}},
		"b": {"web": {Template: "t"}},
	})
	err := lintWorkspace(m)
	if err == nil || !strings.Contains(err.Error(), "collide") {
		t.Fatalf("want a collision error, got %v", err)
	}
}

// TestCollisionLint_ReservedPrefix — a project service may not usurp shared-*.
func TestCollisionLint_ReservedPrefix(t *testing.T) {
	m := modelWith(map[string]map[string]config.Service{
		"a": {"shared-postgres": {Template: "t"}},
	})
	err := lintWorkspace(m)
	if err == nil || !strings.Contains(err.Error(), "shared-") {
		t.Fatalf("want a reserved-prefix error, got %v", err)
	}
}

func TestCollisionLint_OK(t *testing.T) {
	m := modelWith(map[string]map[string]config.Service{
		"a": {"api": {Template: "t"}},
		"b": {"web": {Template: "t"}},
	})
	if err := lintWorkspace(m); err != nil {
		t.Fatalf("distinct names should pass: %v", err)
	}
}

// TestEnvFromFragment — list-form environment must not be dropped.
func TestEnvFromFragment(t *testing.T) {
	list := []any{"FOO=bar", "BARE"}
	got := envFromFragment(list)
	if got["FOO"] != "bar" {
		t.Errorf("FOO = %v, want bar", got["FOO"])
	}
	if v, ok := got["BARE"]; !ok || v != nil {
		t.Errorf("BARE should be a valueless (nil) key, got %v ok=%v", v, ok)
	}
	m := map[string]any{"A": "1", "B": true}
	g2 := envFromFragment(m)
	if g2["A"] != "1" || g2["B"] != "true" {
		t.Errorf("map form: %v", g2)
	}
}

// TestSharedBuildRejected — a shared template with a build context is rejected.
func TestSharedBuildRejected(t *testing.T) {
	m := modelWith(nil)
	res := &template.Resolved{
		Name:    "custom",
		Service: map[string]any{"build": map[string]any{"context": "build"}},
	}
	_, err := buildSharedService(m, "x", res)
	if err == nil || !strings.Contains(err.Error(), "image-based") {
		t.Fatalf("want an image-based rejection, got %v", err)
	}
}
