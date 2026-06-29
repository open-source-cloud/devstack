package migrate

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

const devdockSample = `
name: acme
services:
  postgres:
    template: postgres
    params: { version: "16" }
  redis:
    image: redis:7
  api:
    template: php.laravel.nginx
    repo: acme/api
    uses: [postgres, redis]
    env:
      DATABASE_URL: "postgres://${postgres.host}:5432/api"
      CACHE: "${redis.host}"
      EXTERNAL: "${billing.url}"
  web:
    template: node.vite
    repo: "git@github.com:acme/web.git"
`

func TestConvertSplitsSharedAndProjects(t *testing.T) {
	res, err := Convert([]byte(devdockSample), "fallback")
	if err != nil {
		t.Fatal(err)
	}

	// Workspace: name from the source, shared postgres+redis, two project refs.
	var ws map[string]any
	if err := yaml.Unmarshal(res.WorkspaceYAML, &ws); err != nil {
		t.Fatalf("workspace not valid yaml: %v\n%s", err, res.WorkspaceYAML)
	}
	if ws["name"] != "acme" {
		t.Errorf("workspace name = %v, want acme", ws["name"])
	}
	shared, _ := ws["shared"].(map[string]any)
	if _, ok := shared["postgres"]; !ok {
		t.Errorf("postgres not under shared: %v", shared)
	}
	if _, ok := shared["redis"]; !ok {
		t.Errorf("redis (from image) not under shared: %v", shared)
	}
	// api + web are projects (have repos), not shared.
	if _, ok := shared["api"]; ok {
		t.Error("api should be a project, not shared")
	}
	if res.Projects["api"] == nil || res.Projects["web"] == nil {
		t.Fatalf("expected api+web project files, got %v", keys(res.Projects))
	}

	// api's devstack.yaml: uses rewritten to workspace.shared.*, env ref rewritten.
	apiBody := string(res.Projects["api"])
	if !strings.Contains(apiBody, "workspace.shared.postgres") || !strings.Contains(apiBody, "workspace.shared.redis") {
		t.Errorf("api uses not rewritten:\n%s", apiBody)
	}
	if !strings.Contains(apiBody, "${ref:workspace.shared.postgres.host}") {
		t.Errorf("api env interpolation not rewritten to typed ref:\n%s", apiBody)
	}

	// web's git shorthand expanded — lives in the workspace project ref.
	if !strings.Contains(string(res.WorkspaceYAML), "github.com") {
		t.Errorf("web git not expanded in workspace:\n%s", res.WorkspaceYAML)
	}

	// Lossless-or-loud: the non-shared ${billing.url} ref is reported, not silently kept.
	if !hasReport(res.Report, "EXTERNAL", "billing") {
		t.Errorf("unconvertible ${billing.url} not reported: %+v", res.Report)
	}
}

func TestConvertParsesValidProjectYAML(t *testing.T) {
	res, err := Convert([]byte(devdockSample), "x")
	if err != nil {
		t.Fatal(err)
	}
	// Each emitted project file must itself be valid YAML with kind: Project.
	for name, body := range res.Projects {
		var p map[string]any
		if err := yaml.Unmarshal(body, &p); err != nil {
			t.Errorf("project %s not valid yaml: %v", name, err)
			continue
		}
		if p["kind"] != "Project" || p["name"] != name {
			t.Errorf("project %s header wrong: kind=%v name=%v", name, p["kind"], p["name"])
		}
	}
}

func TestConvertNoServicesIsLoud(t *testing.T) {
	res, err := Convert([]byte("name: empty\n"), "empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Report) == 0 {
		t.Error("a project.yaml with no services should produce a report entry, not silence")
	}
}

func hasReport(entries []ReportEntry, pathSub, valueSub string) bool {
	for _, e := range entries {
		if strings.Contains(e.Path, pathSub) && strings.Contains(e.Value, valueSub) {
			return true
		}
	}
	return false
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
