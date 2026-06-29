//go:build integration

package hooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

// TestOSExecer_RealCompose brings up a one-service busybox stack and runs a
// `run: exec` hook into it via the real `docker compose exec` transport,
// asserting combined output and inline `-e` env injection. Tagged `integration`.
func TestOSExecer_RealCompose(t *testing.T) {
	ctx := context.Background()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skipf("no reachable Docker daemon: %v", err)
	}

	dir := t.TempDir()
	proj := "devstack-it-hooks-" + strconv.Itoa(os.Getpid())
	file := filepath.Join(dir, "docker-compose.yaml")
	compose := "services:\n  app:\n    image: busybox\n    command: [\"sh\",\"-c\",\"sleep 300\"]\n"
	if err := os.WriteFile(file, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	up := exec.CommandContext(ctx, "docker", "compose", "-p", proj, "-f", file, "up", "-d")
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", proj, "-f", file, "down", "-v").Run()
	})

	e := OSExecer{BaseDir: dir, Project: proj, File: file}
	r := &Runner{Execer: e}
	h := config.Hook{
		Name: "echo-env", Run: "exec", Service: "app",
		Command: []string{"sh", "-c", "echo container-says:$INJECTED"},
		Env:     map[string]string{"INJECTED": "hello"},
	}
	out, attempts, err := r.Run(ctx, h, nil)
	if err != nil {
		t.Fatalf("Run exec: %v\n%s", err, out)
	}
	if attempts != 1 || !strings.Contains(out, "container-says:hello") {
		t.Errorf("exec hook out=%q attempts=%d, want injected env echoed", out, attempts)
	}
}
