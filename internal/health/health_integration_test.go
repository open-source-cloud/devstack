//go:build integration

package health

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-source-cloud/devstack/internal/docker"
)

// TestPoll_RealDaemon polls real containers: one whose healthcheck passes (→
// healthy) and one whose healthcheck fails (→ unhealthy ProbeError with logs).
// Tagged `integration`; run via `go test -tags=integration ./internal/health`.
func TestPoll_RealDaemon(t *testing.T) {
	ctx := context.Background()
	cli, err := docker.NewClient(ctx)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = cli.Close() }()
	if err := cli.Ping(ctx); err != nil {
		t.Skipf("no reachable Docker daemon: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "pull", "busybox").CombinedOutput(); err != nil {
		t.Fatalf("docker pull busybox: %v\n%s", err, out)
	}
	pid := strconv.Itoa(os.Getpid())

	run := func(name string, args ...string) string {
		full := append([]string{"run", "-d", "--name", name}, args...)
		out, err := exec.CommandContext(ctx, "docker", full...).Output()
		if err != nil {
			t.Fatalf("docker run %s: %v", name, err)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
		return strings.TrimSpace(string(out))
	}

	tm := Timing{Interval: 500 * time.Millisecond, Timeout: time.Second, Retries: 30, StartPeriod: 0}

	t.Run("healthy", func(t *testing.T) {
		id := run("devstack-it-h-ok-"+pid,
			"--health-cmd", "true", "--health-interval", "1s", "--health-retries", "1",
			"--health-start-period", "0s",
			"busybox", "sh", "-c", "echo ready; sleep 120")
		rec, err := Poll(ctx, cli, Target{ContainerID: id, Service: "ok", Kind: "exec"}, tm)
		if err != nil {
			t.Fatalf("Poll healthy: %v", err)
		}
		if rec.Status != statusHealthy {
			t.Errorf("status = %q, want healthy", rec.Status)
		}
	})

	t.Run("unhealthy", func(t *testing.T) {
		id := run("devstack-it-h-bad-"+pid,
			"--health-cmd", "false", "--health-interval", "1s", "--health-retries", "1",
			"--health-start-period", "0s",
			"busybox", "sh", "-c", "echo about-to-be-unhealthy; sleep 120")
		rec, err := Poll(ctx, cli, Target{ContainerID: id, Service: "bad", Kind: "exec"}, tm)
		var pe *ProbeError
		if !errors.As(err, &pe) {
			t.Fatalf("want ProbeError, got %v", err)
		}
		if rec.Status != statusUnhealthy {
			t.Errorf("status = %q, want unhealthy", rec.Status)
		}
		if !strings.Contains(pe.Logs, "about-to-be-unhealthy") {
			t.Errorf("diagnostic logs = %q, want the printed line", pe.Logs)
		}
	})
}
