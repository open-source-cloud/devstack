package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/state"
)

func TestStatusRegistered(t *testing.T) {
	if !findCmd(t, "status") {
		t.Error("status is not registered as a real RunE command")
	}
}

func TestStatusRunsInWorkspace(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "workspace.yaml"),
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  postgres: { template: postgres }\nprojects:\n  - { name: app, path: app }\n")
	mustWrite(t, filepath.Join(dir, "app", "devstack.yaml"),
		"apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web: { template: node.vite, uses: [workspace.shared.postgres] }\n")
	t.Chdir(dir)
	// Isolate the ledger so the test never touches the real ~/.local/share.
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))

	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"status"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("status: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "app") {
		t.Errorf("status output should name project app:\n%s", out.String())
	}
}

func TestLastSaga(t *testing.T) {
	phases := []state.SagaPhase{
		{Scope: "app", Phase: "compose-up", Status: state.PhaseSatisfied},
		{Scope: "other", Phase: "compose-up", Status: state.PhaseFailed},
	}
	if s := lastSaga(phases, "app"); s == nil || s.Status != state.PhaseSatisfied {
		t.Errorf("app lastSaga = %+v, want satisfied compose-up", s)
	}
	if s := lastSaga(phases, "other"); s == nil || s.Status != state.PhaseFailed {
		t.Errorf("other lastSaga = %+v, want failed", s)
	}
	if s := lastSaga(phases, "ghost"); s != nil {
		t.Errorf("unknown project lastSaga = %+v, want nil", s)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
