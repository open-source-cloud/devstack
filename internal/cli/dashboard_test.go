package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsDashboardRegistered(t *testing.T) {
	for _, name := range []string{"logs", "dashboard"} {
		if !findCmd(t, name) {
			t.Errorf("command %q is not registered as a real RunE command", name)
		}
	}
}

// TestDashboardInteractiveDecision covers the non-TTY / --json / --quiet gate.
func TestDashboardInteractiveDecision(t *testing.T) {
	root := NewRootCmd(Options{})
	root.SetOut(&bytes.Buffer{}) // a buffer is never a TTY
	tests := []struct {
		name string
		g    *GlobalOpts
		want bool
	}{
		{"buffer-out", &GlobalOpts{}, false},
		{"json", &GlobalOpts{JSON: true}, false},
		{"quiet", &GlobalOpts{Quiet: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dashboardInteractive(root, tt.g); got != tt.want {
				t.Fatalf("dashboardInteractive = %v, want %v", got, tt.want)
			}
		})
	}
}

// seedDashWorkspace seeds a minimal workspace + isolated ledger under a temp dir,
// chdir'ing into it so config discovery + the ledger both resolve locally.
func seedDashWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "workspace.yaml"),
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nshared:\n  postgres: { template: postgres }\nprojects:\n  - { name: app, path: app }\n")
	mustWrite(t, filepath.Join(dir, "app", "devstack.yaml"),
		"apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web: { template: node.vite, uses: [workspace.shared.postgres] }\n")
	t.Chdir(dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))
	return dir
}

// TestDashboardSnapshotNonTTY drives `dashboard` with a buffered (non-TTY) stdout;
// it must print the one-shot snapshot + the redirect note, never the TUI.
func TestDashboardSnapshotNonTTY(t *testing.T) {
	seedDashWorkspace(t)
	var out bytes.Buffer
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"dashboard"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("dashboard: %v\n%s", err, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "interactive terminal") || !strings.Contains(s, "devstack logs") {
		t.Fatalf("snapshot missing redirect note:\n%s", s)
	}
	if !strings.Contains(s, "PROJECTS") {
		t.Fatalf("snapshot missing status projection:\n%s", s)
	}
}

// TestDashboardJSONSnapshot asserts --json emits a machine-readable snapshot.
func TestDashboardJSONSnapshot(t *testing.T) {
	seedDashWorkspace(t)
	var out bytes.Buffer
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"dashboard", "--json"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("dashboard --json: %v\n%s", err, out.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out.String())
	}
	if _, ok := got["projects"]; !ok {
		t.Errorf("json snapshot missing 'projects' key: %v", got)
	}
	if _, ok := got["shared"]; !ok {
		t.Errorf("json snapshot missing 'shared' key: %v", got)
	}
}

// TestLogsNoTargetsNotice covers the zero-state message contract directly (the
// end-to-end path is daemon-dependent, so the message is unit-tested instead).
func TestLogsNoTargetsNotice(t *testing.T) {
	root := NewRootCmd(Options{})
	var out bytes.Buffer
	root.SetOut(&out)

	if err := logsNoTargets(root, &GlobalOpts{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no managed services") {
		t.Fatalf("expected zero-state notice, got %q", out.String())
	}

	out.Reset()
	if err := logsNoTargets(root, &GlobalOpts{}, []string{"api"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no running services match") {
		t.Fatalf("expected filtered zero-state notice, got %q", out.String())
	}

	// --json and --quiet stay silent (empty stream == zero objects).
	out.Reset()
	if err := logsNoTargets(root, &GlobalOpts{JSON: true}, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("--json zero-state should be silent, got %q", out.String())
	}
}
