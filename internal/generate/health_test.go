package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/templates"
)

func TestHealthcheckTestKinds(t *testing.T) {
	cases := []struct {
		name string
		hc   config.Healthcheck
		want []any
	}{
		{"tcp", config.Healthcheck{Kind: "tcp", Port: 5432},
			[]any{"CMD-SHELL", "nc -z localhost 5432"}},
		{"http", config.Healthcheck{Kind: "http", Port: 8080, Path: "/healthz"},
			[]any{"CMD", "curl", "-fsS", "-o", "/dev/null", "http://localhost:8080/healthz"}},
		{"https-skipverify", config.Healthcheck{Kind: "https", Port: 443, Path: "/"},
			[]any{"CMD", "curl", "-fsS", "-k", "-o", "/dev/null", "https://localhost:443/"}},
		{"exec", config.Healthcheck{Kind: "exec", Command: []string{"mysqladmin", "ping"}},
			[]any{"CMD", "mysqladmin", "ping"}},
		{"pg_isready", config.Healthcheck{Kind: "pg_isready", User: "app", DB: "appdb"},
			[]any{"CMD-SHELL", "pg_isready -p 5432 -U app -d appdb"}},
		{"redis-default", config.Healthcheck{Kind: "redis"},
			[]any{"CMD-SHELL", "redis-cli -p 6379 PING"}},
		{"redis-literal-auth", config.Healthcheck{Kind: "redis", Auth: "devpass"},
			[]any{"CMD-SHELL", "redis-cli -p 6379 -a devpass PING"}},
		{"redis-secret-auth-omitted", config.Healthcheck{Kind: "redis", Auth: "secret://vault/redis#pw"},
			[]any{"CMD-SHELL", "redis-cli -p 6379 PING"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := healthcheckTest(&c.hc)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("test = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHealthcheckTestErrors(t *testing.T) {
	for _, hc := range []config.Healthcheck{
		{Kind: "tcp"},  // no port
		{Kind: "exec"}, // no command
	} {
		if _, err := healthcheckTest(&hc); err == nil {
			t.Errorf("kind %q with missing params should error", hc.Kind)
		}
	}
}

func TestHealthcheckBlockTimingOnlyWhenSet(t *testing.T) {
	// Only the test is present when timing is unset.
	bare, _ := healthcheckBlock(&config.Healthcheck{Kind: "tcp", Port: 1})
	if len(bare) != 1 {
		t.Errorf("bare block = %v, want only test", bare)
	}
	full, _ := healthcheckBlock(&config.Healthcheck{
		Kind: "tcp", Port: 1, Interval: "5s", Timeout: "3s", Retries: 7, StartPeriod: "20s",
	})
	for _, k := range []string{"test", "interval", "timeout", "retries", "start_period"} {
		if _, ok := full[k]; !ok {
			t.Errorf("full block missing %q: %v", k, full)
		}
	}
}

func TestDependsOnBlockClassification(t *testing.T) {
	m := &config.Model{Projects: map[string]config.Project{
		"api": {Services: map[string]config.Service{
			"web":   {Template: "t"},
			"cache": {Template: "t"},
		}},
	}}
	deps := []config.DependsOn{
		{Service: "cache", Condition: "healthy"},                     // intra (bare)
		{Service: "workspace.api.web", Condition: "started"},         // intra (qualified)
		{Service: "workspace.shared.postgres", Condition: "healthy"}, // shared → skip
		{Service: "workspace.other.svc", Condition: "healthy"},       // other project → skip
	}
	got, err := dependsOnBlock(m, "api", deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("depends_on = %v, want 2 intra-project edges", got)
	}
	if got["cache"].(map[string]any)["condition"] != "service_healthy" {
		t.Errorf("cache condition = %v, want service_healthy", got["cache"])
	}
	if got["web"].(map[string]any)["condition"] != "service_started" {
		t.Errorf("web condition = %v, want service_started", got["web"])
	}
}

func TestDependsOnBlockMissingTarget(t *testing.T) {
	m := &config.Model{Projects: map[string]config.Project{
		"api": {Services: map[string]config.Service{"web": {Template: "t"}}},
	}}
	_, err := dependsOnBlock(m, "api", []config.DependsOn{{Service: "ghost"}})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want a missing-target error naming ghost, got %v", err)
	}
}

// TestIntraProjectDependsOn_EndToEnd generates a real project where one service
// depends on a sibling (condition healthy) and the sibling declares a
// healthcheck, asserting the lowering reaches compose AND compose-go accepts the
// service_healthy edge.
func TestIntraProjectDependsOn_EndToEnd(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("workspace.yaml", "apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n  - { name: app, path: app }\n")
	write("app/devstack.yaml", `apiVersion: devstack/v1
kind: Project
name: app
services:
  cache:
    template: node.vite
    healthcheck: { kind: tcp, port: 6379, interval: 2s }
  web:
    template: node.vite
    dependsOn:
      - { service: cache, condition: healthy }
`)
	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	g, err := New(m, template.NewFSSource(templates.FS), WithEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st, err := g.GenerateProject("app")
	if err != nil {
		t.Fatalf("GenerateProject: %v", err)
	}
	compose := string(st.Compose)
	if !strings.Contains(compose, "depends_on:") || !strings.Contains(compose, "condition: service_healthy") {
		t.Errorf("compose missing lowered depends_on:\n%s", compose)
	}
	if !strings.Contains(compose, "cache") {
		t.Errorf("compose should reference the cache dependency:\n%s", compose)
	}
}
