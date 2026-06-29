//go:build e2e

// Package e2e drives the built devstack binary end to end — functional CLI flows
// (no daemon) and full daemon e2e (up/status/down). Tagged `e2e` so it builds
// the binary and runs only in the dedicated CI lane (and locally via
// `go test -tags=e2e ./tests/e2e`). Daemon tests additionally gate on
// DEVSTACK_E2E=1 because they mutate Docker (the shared stack + network).
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bin is the path to the devstack binary built once for the whole package.
var bin string

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "getwd:", err)
		os.Exit(1)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))

	dir, err := os.MkdirTemp("", "devstack-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		os.Exit(1)
	}
	bin = filepath.Join(dir, "devstack")
	build := exec.Command("go", "build", "-o", bin, "./cmd/devstack")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build devstack: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// sandbox is an isolated workspace + XDG home for one test.
type sandbox struct {
	ws  string
	env []string
}

// newSandbox writes workspace.yaml (+ each project's devstack.yaml) into a temp
// dir and returns it with an isolated XDG environment so the ledger/lock never
// touch the developer's real state.
func newSandbox(t *testing.T, files map[string]string) *sandbox {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	env := append(os.Environ(),
		"DEVSTACK_WORKSPACE="+root,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_RUNTIME_DIR="+filepath.Join(home, "run"),
		"XDG_BIN_HOME="+filepath.Join(home, "bin"),
	)
	return &sandbox{ws: root, env: env}
}

// run executes the binary, fails the test on a non-zero exit, and returns the
// combined output.
func (s *sandbox) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := s.tryRun(args...)
	if err != nil {
		t.Fatalf("`devstack %s` failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// tryRun executes the binary and returns its combined output + error.
func (s *sandbox) tryRun(args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = s.env
	cmd.Dir = s.ws
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// requireDaemon skips the test unless DEVSTACK_E2E=1 AND a Docker daemon is
// reachable. The env gate prevents a local run from clobbering a real shared
// stack; CI (ephemeral runner) sets it.
func requireDaemon(t *testing.T) {
	t.Helper()
	if os.Getenv("DEVSTACK_E2E") != "1" {
		t.Skip("daemon e2e mutates Docker (devstack_shared); set DEVSTACK_E2E=1 to run")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no reachable Docker daemon: %v", err)
	}
}

// dockerComposeDown tears down a compose project by name (best-effort cleanup).
func dockerComposeDown(project string) {
	_ = exec.Command("docker", "compose", "-p", project, "down", "-v").Run()
}
