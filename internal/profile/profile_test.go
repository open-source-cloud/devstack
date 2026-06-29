package profile

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

// model: shared pg/redis; project app with services api (group core, uses pg),
// web (tag frontend, uses pg+redis), worker (tag frontend), cron (no profile).
func sliceModel() *config.Model {
	return &config.Model{
		Workspace: config.Workspace{
			DefaultProfile: "core",
			Groups:         map[string]config.Group{"core": {Services: []string{"api"}}},
			Shared:         map[string]config.SharedSvc{"postgres": {}, "redis": {}},
		},
		Projects: map[string]config.Project{"app": {Services: map[string]config.Service{
			"api":    {Uses: []string{"workspace.shared.postgres"}},
			"web":    {Profiles: []string{"frontend"}, Uses: []string{"workspace.shared.postgres", "workspace.shared.redis"}},
			"worker": {Profiles: []string{"frontend"}},
			"cron":   {},
		}}},
	}
}

func active(t *testing.T, a Active) string {
	t.Helper()
	var parts []string
	for _, s := range a.Services["app"] {
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

func TestResolveDefaultProfile(t *testing.T) {
	// No --profile → defaultProfile "core" → only the core group (api).
	a := Resolve(sliceModel(), nil)
	if got := active(t, a); got != "api" {
		t.Errorf("default(core) active = %q, want api", got)
	}
	if strings.Join(a.Shared, ",") != "postgres" {
		t.Errorf("shared = %v, want [postgres] (api uses pg)", a.Shared)
	}
}

func TestResolveExplicitProfileTag(t *testing.T) {
	// --profile frontend → services tagged frontend (web, worker), not api/cron.
	a := Resolve(sliceModel(), []string{"frontend"})
	if got := active(t, a); got != "web,worker" {
		t.Errorf("frontend active = %q, want web,worker", got)
	}
	// web uses pg+redis; worker uses none → shared = postgres,redis.
	if strings.Join(a.Shared, ",") != "postgres,redis" {
		t.Errorf("shared = %v, want [postgres redis]", a.Shared)
	}
}

func TestResolveUnionCommaAndRepeat(t *testing.T) {
	// core ∪ frontend via comma and via repeat → identical (api,web,worker).
	want := "api,web,worker"
	if got := active(t, Resolve(sliceModel(), []string{"core,frontend"})); got != want {
		t.Errorf("comma union = %q, want %q", got, want)
	}
	if got := active(t, Resolve(sliceModel(), []string{"core", "frontend"})); got != want {
		t.Errorf("repeat union = %q, want %q", got, want)
	}
}

func TestResolveAllReserved(t *testing.T) {
	// --profile all → every service regardless of defaultProfile.
	if got := active(t, Resolve(sliceModel(), []string{"all"})); got != "api,cron,web,worker" {
		t.Errorf("all = %q, want every service", got)
	}
}

func TestResolveNoConfigDefaultIsAll(t *testing.T) {
	// No defaultProfile + no --profile → reserved `all` → every service.
	m := sliceModel()
	m.Workspace.DefaultProfile = ""
	if got := active(t, Resolve(m, nil)); got != "api,cron,web,worker" {
		t.Errorf("no-config default = %q, want every service (all)", got)
	}
}

func TestResolveHasAndShared(t *testing.T) {
	a := Resolve(sliceModel(), []string{"core"})
	if !a.Has("app", "api") || a.Has("app", "web") {
		t.Error("Has wrong for the core slice")
	}
}
