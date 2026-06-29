package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/trust"
)

// uninstallTrustRunner makes trust.Uninstall find mkcert and succeed.
type uninstallTrustRunner struct{ ran bool }

func (r *uninstallTrustRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return []byte("/ca/root"), nil
}
func (r *uninstallTrustRunner) Run(_ context.Context, _ string, args ...string) error {
	if len(args) > 0 && args[0] == "-uninstall" {
		r.ran = true
	}
	return nil
}
func (r *uninstallTrustRunner) LookPath(string) (string, error) { return "/usr/bin/mkcert", nil }

func TestUninstallRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"uninstall"})
	if err != nil || c.Name() != "uninstall" || c.RunE == nil {
		t.Fatalf("uninstall not registered as a real command: %v", err)
	}
}

func TestUninstallJSONRequiresYes(t *testing.T) {
	t.Chdir(t.TempDir())
	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"uninstall", "--json"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err == nil {
		t.Fatal("uninstall --json without --yes must error")
	}
}

func TestRunUninstallSequence(t *testing.T) {
	// Devstack-namespaced XDG dirs to remove + an /etc/hosts stand-in.
	data := filepath.Join(t.TempDir(), "data")
	cache := filepath.Join(t.TempDir(), "cache")
	config := filepath.Join(t.TempDir(), "config")
	for _, d := range []string{data, cache, config} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hosts := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hosts, []byte(
		"127.0.0.1 localhost\n# >>> devstack >>> (managed — do not edit)\n127.0.0.1 app.localhost\n# <<< devstack <<<\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mc := &docker.MockClient{
		Context: "ctx",
		Containers: []docker.Container{
			{ID: "a", Name: "devstack-app-web-1", State: "running", Labels: map[string]string{
				generate.LabelManaged: "true", "com.docker.compose.project": "devstack-app"}},
			{ID: "p", Name: "devstack-shared-postgres-1", State: "running", Labels: map[string]string{
				generate.LabelManaged: "true", "com.docker.compose.project": generate.SharedStackName}},
		},
	}
	fr := &destroyFakeRunner{}
	tr := &trust.Trust{Runner: &uninstallTrustRunner{}}

	res := runUninstall(context.Background(), uninstallEnv{
		Client: mc, Runner: fr, Trust: tr, HostsPath: hosts,
		Dirs: []string{data, cache, config},
	})

	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
	// Every managed stack composed down WITH volumes.
	for _, p := range []string{"devstack-app", generate.SharedStackName} {
		if !fr.saw("-p "+p, "down", "--volumes") {
			t.Errorf("stack %s not composed down -v: %v", p, fr.cmds)
		}
	}
	if len(res.ProjectsDown) != 2 {
		t.Errorf("projects down = %v, want 2", res.ProjectsDown)
	}
	// External network removed.
	if !fr.saw("network", "rm", generate.SharedNetwork) || !res.NetworkRemoved {
		t.Errorf("network not removed: %v", fr.cmds)
	}
	// CA cleared via mkcert -uninstall.
	if !res.CACleared {
		t.Error("CA was not cleared")
	}
	// /etc/hosts marker block removed.
	if !res.HostsCleared {
		t.Error("hosts block not cleared")
	}
	if b, _ := os.ReadFile(hosts); strings.Contains(string(b), "app.localhost") {
		t.Errorf("devstack hosts entries survived:\n%s", b)
	}
	// XDG dirs removed.
	if len(res.DirsRemoved) != 3 {
		t.Errorf("dirs removed = %v, want 3", res.DirsRemoved)
	}
	for _, d := range []string{data, cache, config} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("dir %s should be removed", d)
		}
	}
}

func TestRunUninstallNetworkAlreadyGone(t *testing.T) {
	// A "network not found" is the desired end state, not a warning.
	fr := &notFoundRunner{}
	res := runUninstall(context.Background(), uninstallEnv{
		Client: &docker.MockClient{Context: "ctx"}, Runner: fr,
		Trust:     &trust.Trust{Runner: &uninstallTrustRunner{}},
		HostsPath: filepath.Join(t.TempDir(), "nohosts"), Dirs: nil,
	})
	if !res.NetworkRemoved {
		t.Error("a not-found network should count as removed (desired end state)")
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "network rm") {
			t.Errorf("not-found network should not warn: %v", res.Warnings)
		}
	}
}

// notFoundRunner fails network rm with a not-found error, succeeds otherwise.
type notFoundRunner struct{ destroyFakeRunner }

func (r *notFoundRunner) Run(ctx context.Context, env []string, dir, name string, args ...string) error {
	_ = r.destroyFakeRunner.Run(ctx, env, dir, name, args...)
	if len(args) >= 2 && args[0] == "network" && args[1] == "rm" {
		return &docker.CmdError{Cmd: "docker network rm", Stderr: "Error: No such network: devstack_shared"}
	}
	return nil
}
