//go:build integration

package docker

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestContainerInspectLogs_RealDaemon validates the moby field mapping against a
// real Engine: a container with a healthcheck must report .State.Health.Status
// through ContainerInspect, and its stdout must demux through ContainerLogs.
// Tagged `integration` so the daemon-free unit lane skips it (run via the
// integration CI lane / `go test -tags=integration ./internal/docker`).
func TestContainerInspectLogs_RealDaemon(t *testing.T) {
	ctx := context.Background()
	cli, err := NewClient(ctx)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = cli.Close() }()
	if err := cli.Ping(ctx); err != nil {
		t.Skipf("no reachable Docker daemon: %v", err)
	}

	// Pre-pull so `docker run -d` emits only the container ID on stdout (pull
	// progress would otherwise go to stderr and, worse, racily delay the run).
	if out, err := exec.CommandContext(ctx, "docker", "pull", "busybox").CombinedOutput(); err != nil {
		t.Fatalf("docker pull busybox: %v\n%s", err, out)
	}

	name := "devstack-it-c2-" + strconv.Itoa(os.Getpid())
	// A container that prints a known line and stays up, with a trivially-passing
	// healthcheck so .State.Health.Status reaches `healthy` quickly.
	run := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name,
		"--health-cmd", "true", "--health-interval", "1s", "--health-retries", "1",
		"--health-start-period", "0s",
		"busybox", "sh", "-c", "echo hello-from-c2; sleep 60")
	out, err := run.Output() // stdout only → just the container ID
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	// Poll inspect until the healthcheck flips to healthy (bounded).
	deadline := time.Now().Add(30 * time.Second)
	var d ContainerDetails
	for {
		d, err = cli.ContainerInspect(ctx, id)
		if err != nil {
			t.Fatalf("ContainerInspect: %v", err)
		}
		if d.Healthy() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("container never became healthy: state=%q health=%q", d.State, d.Health)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !d.Running {
		t.Errorf("Running = false, want true (state=%q)", d.State)
	}
	if !d.HasHealthcheck() {
		t.Errorf("HasHealthcheck = false, want true (health=%q)", d.Health)
	}

	logs, err := cli.ContainerLogs(ctx, id, 10)
	if err != nil {
		t.Fatalf("ContainerLogs: %v", err)
	}
	if !strings.Contains(logs, "hello-from-c2") {
		t.Errorf("logs = %q, want to contain the printed line", logs)
	}

	// A container with no healthcheck reports empty Health, not "starting".
	name2 := name + "-nohc"
	out2, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name2,
		"busybox", "sh", "-c", "sleep 60").Output()
	if err != nil {
		t.Fatalf("docker run (no hc): %v", err)
	}
	id2 := strings.TrimSpace(string(out2))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name2).Run() })
	d2, err := cli.ContainerInspect(ctx, id2)
	if err != nil {
		t.Fatalf("ContainerInspect (no hc): %v", err)
	}
	if d2.HasHealthcheck() {
		t.Errorf("HasHealthcheck = true for a container with no healthcheck (health=%q)", d2.Health)
	}
}
