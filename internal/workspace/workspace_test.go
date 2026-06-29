package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/templates"
)

// TestMain points XDG_RUNTIME_DIR at a throwaway dir so the state package's
// internal lockfile is hermetic.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "devstack-ws-rt-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_RUNTIME_DIR", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func newManager(t *testing.T, mock *docker.MockClient) *Manager {
	t.Helper()
	model, err := config.LoadAt(filepath.Join("..", "config", "testdata", "valid"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	db, err := state.Open(context.Background(), t.TempDir(), "ctx")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if mock == nil {
		mock = &docker.MockClient{}
	}
	return &Manager{
		Model:    model,
		DB:       db,
		Docker:   mock,
		Source:   template.NewFSSource(templates.FS),
		LockPath: filepath.Join(t.TempDir(), "devstack.lock"),
	}
}

func TestSharedInstances(t *testing.T) {
	m := newManager(t, nil)
	inst, err := m.SharedInstances()
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := inst["postgres"]
	if !ok {
		t.Fatal("postgres instance missing")
	}
	if pg.Alias != "shared-postgres" || pg.Engine != "postgres" || pg.Major != "16" || pg.Port != 5432 {
		t.Errorf("postgres instance = %+v", pg)
	}
}

func TestRefCountLifecycle(t *testing.T) {
	m := newManager(t, nil)
	ctx := context.Background()

	// up api (uses postgres + redis); up web (uses postgres).
	if err := m.RegisterUp(ctx, "api"); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterUp(ctx, "web"); err != nil {
		t.Fatal(err)
	}

	status := byAlias(t, m)
	if status["shared-postgres"].RefCount != 2 {
		t.Errorf("shared-postgres refs = %d, want 2", status["shared-postgres"].RefCount)
	}
	if status["shared-redis"].RefCount != 1 {
		t.Errorf("shared-redis refs = %d, want 1", status["shared-redis"].RefCount)
	}
	if len(status["shared-postgres"].Projects) != 2 {
		t.Errorf("postgres projects = %v, want [api web]", status["shared-postgres"].Projects)
	}

	// down web → postgres still referenced by api (not zeroed).
	zeroed, err := m.RegisterDown(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(zeroed) != 0 {
		t.Errorf("zeroed after down web = %v, want none", zeroed)
	}
	if byAlias(t, m)["shared-postgres"].RefCount != 1 {
		t.Error("postgres should have 1 ref after down web")
	}

	// down api → both postgres and redis hit zero.
	zeroed, _ = m.RegisterDown(ctx, "api")
	if len(zeroed) != 2 {
		t.Errorf("zeroed after down api = %v, want both shared services", zeroed)
	}
}

func TestRegisterUpIdempotent(t *testing.T) {
	m := newManager(t, nil)
	ctx := context.Background()
	_ = m.RegisterUp(ctx, "api")
	_ = m.RegisterUp(ctx, "api") // re-up must not double-count
	if byAlias(t, m)["shared-postgres"].RefCount != 1 {
		t.Error("re-up double-counted")
	}
}

func TestReconcilePrunesDeadProjects(t *testing.T) {
	// Seed a live container for api but NOT web; both have refs registered.
	mock := &docker.MockClient{Containers: []docker.Container{
		{ID: "1", Name: "devstack-api-api", State: "running", Labels: map[string]string{
			generate.LabelManaged: "true", generate.LabelProject: "api"}},
	}}
	m := newManager(t, mock)
	ctx := context.Background()
	_ = m.RegisterUp(ctx, "api")
	_ = m.RegisterUp(ctx, "web")

	pruned, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// web is not live → its ref(s) pruned; api stays.
	if len(pruned) != 1 || pruned[0].Project != "web" {
		t.Fatalf("pruned = %+v, want [web]", pruned)
	}
	if byAlias(t, m)["shared-postgres"].RefCount != 1 {
		t.Error("api ref should survive reconcile")
	}
}

func TestFreeHostPortSkipsPublished(t *testing.T) {
	// A live container already publishes PortBaseApp on the host.
	mock := &docker.MockClient{Containers: []docker.Container{
		{ID: "1", Name: "c", State: "running",
			Labels: map[string]string{generate.LabelManaged: "true"},
			Ports:  []docker.PortBinding{{HostPort: PortBaseApp, ContainerPort: 8080, Protocol: "tcp"}}},
	}}
	m := newManager(t, mock)
	port, err := m.FreeHostPort(context.Background(), "api", "app", PortBaseApp)
	if err != nil {
		t.Fatal(err)
	}
	if port == PortBaseApp {
		t.Errorf("allocated the Docker-published port %d (the VM-proxy union failed)", port)
	}
	// Stable for the same owner/purpose.
	again, _ := m.FreeHostPort(context.Background(), "api", "app", PortBaseApp)
	if again != port {
		t.Errorf("port not stable: %d vs %d", again, port)
	}
}

// byAlias indexes Status() by alias for assertions.
func byAlias(t *testing.T, m *Manager) map[string]SharedStatus {
	t.Helper()
	st, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]SharedStatus{}
	for _, s := range st {
		out[s.Alias] = s
	}
	return out
}
