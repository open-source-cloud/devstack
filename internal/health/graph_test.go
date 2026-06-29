package health

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

func depModel() *config.Model {
	return &config.Model{
		Workspace: config.Workspace{Shared: map[string]config.SharedSvc{
			"postgres": {Template: "postgres"},
			"redis":    {Template: "redis"},
		}},
		Projects: map[string]config.Project{"app": {Services: map[string]config.Service{
			"web": {DependsOn: []config.DependsOn{
				{Service: "workspace.shared.postgres", Condition: "healthy"},
				{Service: "cache", Condition: "started"},
			}},
			"cache": {DependsOn: []config.DependsOn{
				{Service: "workspace.shared.postgres", Condition: "healthy"},
			}},
		}}},
	}
}

func TestBuildGraphNodesAndEdges(t *testing.T) {
	g, err := BuildGraph(depModel())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes()) != 4 { // app/web, app/cache, shared/postgres, shared/redis
		t.Errorf("nodes = %d, want 4", len(g.Nodes()))
	}
	if cyc := g.Cycle(); cyc != nil {
		t.Errorf("acyclic graph reported a cycle: %v", cyc)
	}
}

func TestWavesTopoOrder(t *testing.T) {
	g, _ := BuildGraph(depModel())
	waves, err := g.Waves()
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 3 {
		t.Fatalf("waves = %d, want 3", len(waves))
	}
	ids := func(w []Node) []string {
		var out []string
		for _, n := range w {
			out = append(out, n.ID())
		}
		return out
	}
	// wave0: the two shared services (nothing depends backward onto them).
	if got := strings.Join(ids(waves[0]), ","); got != "shared/postgres,shared/redis" {
		t.Errorf("wave0 = %q, want shared/postgres,shared/redis", got)
	}
	// wave1: cache (depends only on postgres). wave2: web (depends on cache).
	if got := strings.Join(ids(waves[1]), ","); got != "app/cache" {
		t.Errorf("wave1 = %q, want app/cache", got)
	}
	if got := strings.Join(ids(waves[2]), ","); got != "app/web" {
		t.Errorf("wave2 = %q, want app/web", got)
	}
}

func TestCycleDetectedWithPath(t *testing.T) {
	m := &config.Model{Projects: map[string]config.Project{"app": {Services: map[string]config.Service{
		"a": {DependsOn: []config.DependsOn{{Service: "b", Condition: "started"}}},
		"b": {DependsOn: []config.DependsOn{{Service: "a", Condition: "started"}}},
	}}}}
	g, err := BuildGraph(m)
	if err != nil {
		t.Fatal(err)
	}
	cyc := g.Cycle()
	if cyc == nil {
		t.Fatal("expected a cycle")
	}
	joined := strings.Join(cyc, " → ")
	if !strings.Contains(joined, "app.a") || !strings.Contains(joined, "app.b") {
		t.Errorf("cycle path = %q, want it to name app.a and app.b", joined)
	}
	if _, err := g.Waves(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("Waves on a cyclic graph should error with the path, got %v", err)
	}
}

func TestBuildGraphUnknownTarget(t *testing.T) {
	m := &config.Model{Projects: map[string]config.Project{"app": {Services: map[string]config.Service{
		"web": {DependsOn: []config.DependsOn{{Service: "ghost"}}},
	}}}}
	if _, err := BuildGraph(m); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want an unknown-target error naming ghost, got %v", err)
	}
}

func TestRequireHealthchecks(t *testing.T) {
	g, _ := BuildGraph(depModel())

	// postgres has no healthcheck → the web→postgres (healthy) edge fails.
	err := g.RequireHealthchecks(func(Node) bool { return false })
	if err == nil || !strings.Contains(err.Error(), "healthcheck") {
		t.Fatalf("want a missing-healthcheck error, got %v", err)
	}
	// All targets have healthchecks → ok.
	if err := g.RequireHealthchecks(func(Node) bool { return true }); err != nil {
		t.Errorf("all-healthchecks should pass, got %v", err)
	}
	// Only the `started` edge (web→cache) is unchecked; postgres has a check →
	// no healthy edge violates.
	hasHC := func(n Node) bool { return n.Shared } // shared (postgres) has one; cache doesn't
	if err := g.RequireHealthchecks(hasHC); err != nil {
		t.Errorf("started-condition edge to a no-healthcheck service must be allowed, got %v", err)
	}
}
