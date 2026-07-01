package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

// writeWorkspace writes a workspace.yaml + one or more project devstack.yaml files
// under a fresh temp dir and returns the root. projects maps project name → body.
func writeWS(t *testing.T, workspaceYAML string, projects map[string]string) string {
	t.Helper()
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
	write("workspace.yaml", workspaceYAML)
	for name, body := range projects {
		write(filepath.Join(name, "devstack.yaml"), body)
	}
	return root
}

func TestShellRegistered(t *testing.T) {
	if !findCmd(t, "shell") {
		t.Fatal("shell must be a real RunE command (graduated from stubs)")
	}
	// And it must NOT appear as a stub anymore.
	root := NewRootCmd(Options{})
	for _, c := range root.Commands() {
		if c.Name() == "shell" && c.RunE == nil {
			t.Fatal("shell is still a stub group")
		}
	}
}

func TestResolveShellTarget(t *testing.T) {
	oneSvc := "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web: { template: node.vite }\n"
	twoSvc := "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web: { template: node.vite }\n  api: { template: node.vite }\n"

	t.Run("single project single service defaults", func(t *testing.T) {
		root := writeWS(t,
			"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n  - { name: app, path: app }\n",
			map[string]string{"app": oneSvc})
		m := mustLoad(t, root)
		proj, svc, err := resolveShellTarget(m, "", "")
		if err != nil || proj != "app" || svc != "web" {
			t.Fatalf("resolve = (%q,%q,%v), want app/web", proj, svc, err)
		}
	})

	t.Run("multi service requires explicit name", func(t *testing.T) {
		root := writeWS(t,
			"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n  - { name: app, path: app }\n",
			map[string]string{"app": twoSvc})
		m := mustLoad(t, root)
		if _, _, err := resolveShellTarget(m, "", ""); err == nil {
			t.Fatal("multi-service project must require an explicit service")
		}
		proj, svc, err := resolveShellTarget(m, "", "api")
		if err != nil || proj != "app" || svc != "api" {
			t.Fatalf("resolve with name = (%q,%q,%v)", proj, svc, err)
		}
	})

	t.Run("unknown service errors", func(t *testing.T) {
		root := writeWS(t,
			"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n  - { name: app, path: app }\n",
			map[string]string{"app": oneSvc})
		m := mustLoad(t, root)
		if _, _, err := resolveShellTarget(m, "", "nope"); err == nil {
			t.Fatal("unknown service must error")
		}
	})

	t.Run("multi project requires --project", func(t *testing.T) {
		root := writeWS(t,
			"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n  - { name: app, path: app }\n  - { name: svc, path: svc }\n",
			map[string]string{
				"app": oneSvc,
				"svc": "apiVersion: devstack/v1\nkind: Project\nname: svc\nservices:\n  worker: { template: node.vite }\n",
			})
		m := mustLoad(t, root)
		if _, _, err := resolveShellTarget(m, "", ""); err == nil {
			t.Fatal("multi-project workspace must require --project")
		}
		proj, svc, err := resolveShellTarget(m, "svc", "")
		if err != nil || proj != "svc" || svc != "worker" {
			t.Fatalf("resolve with --project = (%q,%q,%v)", proj, svc, err)
		}
	})
}

func TestDefaultShellCmd(t *testing.T) {
	cmd := defaultShellCmd()
	if len(cmd) < 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("defaultShellCmd = %v, want an sh -c probe", cmd)
	}
	if !strings.Contains(cmd[2], "bash") || !strings.Contains(cmd[2], "sh") {
		t.Errorf("default shell probe should prefer bash, fall back to sh: %q", cmd[2])
	}
}

func TestShellNonTTYErrorsClearly(t *testing.T) {
	root := writeWS(t,
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\nprojects:\n  - { name: app, path: app }\n",
		map[string]string{"app": "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n  web: { template: node.vite }\n"})
	t.Chdir(root)

	// Force a non-TTY stdin (a pipe is not a character device) so the check is
	// deterministic regardless of how the tests are launched.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(); _ = r.Close() }()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	rootCmd := NewRootCmd(Options{})
	var out strings.Builder
	rootCmd.SetArgs([]string{"shell", "web"})
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("shell in a non-TTY must error, not hang")
	}
	if !strings.Contains(err.Error(), "TTY") && !strings.Contains(err.Error(), "terminal") {
		t.Errorf("non-TTY error should mention the terminal requirement: %v", err)
	}
}

func mustLoad(t *testing.T, root string) *config.Model {
	t.Helper()
	m, err := config.LoadAt(root)
	if err != nil {
		t.Fatalf("load %s: %v", root, err)
	}
	return m
}
