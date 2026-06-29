package docker

import (
	"context"
	"strings"
	"testing"
)

// fakeRunner records invocations so command construction can be asserted without
// a real docker binary.
type fakeRunner struct {
	calls [][]string
}

func (f *fakeRunner) Run(_ context.Context, _ []string, _, name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil
}

func (f *fakeRunner) Output(_ context.Context, _ []string, _, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil, nil
}

func (f *fakeRunner) last() string { return strings.Join(f.calls[len(f.calls)-1], " ") }

func newTestCompose() (*Compose, *fakeRunner) {
	r := &fakeRunner{}
	c := &Compose{Project: "devstack-api", File: "/ws/.devstack/docker-compose.yaml", Dir: "/ws/.devstack", Runner: r}
	return c, r
}

func TestComposeUp(t *testing.T) {
	c, r := newTestCompose()
	if err := c.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "docker compose -p devstack-api -f /ws/.devstack/docker-compose.yaml up -d"
	if got := r.last(); got != want {
		t.Errorf("up = %q, want %q", got, want)
	}
}

func TestComposeUpSubset(t *testing.T) {
	c, r := newTestCompose()
	c.Project = "devstack-shared"
	_ = c.Up(context.Background(), "postgres", "redis")
	if got := r.last(); !strings.HasSuffix(got, "up -d postgres redis") {
		t.Errorf("subset up = %q", got)
	}
}

func TestComposeDownAndStop(t *testing.T) {
	c, r := newTestCompose()
	_ = c.Down(context.Background(), false)
	if got := r.last(); !strings.HasSuffix(got, "down") {
		t.Errorf("down = %q", got)
	}
	_ = c.Down(context.Background(), true)
	if got := r.last(); !strings.HasSuffix(got, "down --volumes") {
		t.Errorf("down --volumes = %q", got)
	}
	_ = c.Stop(context.Background(), "postgres")
	if got := r.last(); !strings.HasSuffix(got, "stop postgres") {
		t.Errorf("stop = %q", got)
	}
}

func TestComposeBuildNoCache(t *testing.T) {
	c, r := newTestCompose()
	_ = c.Build(context.Background(), true, "api")
	if got := r.last(); !strings.HasSuffix(got, "build --no-cache api") {
		t.Errorf("build = %q", got)
	}
}

func TestCmdErrorMessage(t *testing.T) {
	e := &CmdError{Cmd: "docker compose up", Stderr: "network not found", Err: context.Canceled}
	if !strings.Contains(e.Error(), "network not found") || !strings.Contains(e.Error(), "docker compose up") {
		t.Errorf("error message = %q", e.Error())
	}
}
