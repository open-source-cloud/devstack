package health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
)

// seqClient returns a scripted sequence of ContainerInspect results (the last
// element repeats once exhausted) and a canned log body, so the poll loop is
// exercised with no daemon. inspectErrs[i] (if set) forces the i-th call to err.
type seqClient struct {
	*docker.MockClient
	states     []docker.ContainerDetails
	inspectErr []error
	i          int
	logs       string
}

func (s *seqClient) ContainerInspect(_ context.Context, _ string) (docker.ContainerDetails, error) {
	idx := s.i
	if idx >= len(s.states) {
		idx = len(s.states) - 1
	}
	var err error
	if s.i < len(s.inspectErr) {
		err = s.inspectErr[s.i]
	}
	s.i++
	if err != nil {
		return docker.ContainerDetails{}, err
	}
	if idx < 0 {
		return docker.ContainerDetails{}, errors.New("no states")
	}
	return s.states[idx], nil
}

func (s *seqClient) ContainerLogs(_ context.Context, _ string, _ int) (string, error) {
	return s.logs, nil
}

func det(state string, running bool, h docker.HealthStatus) docker.ContainerDetails {
	return docker.ContainerDetails{State: state, Running: running, Health: h}
}

// fastTiming polls effectively instantly so tests don't sleep for real.
func fastTiming(retries int) Timing {
	return Timing{Interval: time.Millisecond, Timeout: time.Second, Retries: retries, StartPeriod: 0}
}

func newClient(logs string, states ...docker.ContainerDetails) *seqClient {
	return &seqClient{MockClient: &docker.MockClient{}, states: states, logs: logs}
}

func TestPollHealthyAfterStarting(t *testing.T) {
	cli := newClient("", det("running", true, docker.HealthStarting),
		det("running", true, docker.HealthStarting),
		det("running", true, docker.HealthHealthy))
	rec, err := Poll(context.Background(), cli, Target{Service: "pg", Project: "devstack-shared", Kind: "pg_isready"}, fastTiming(10))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if rec.Status != statusHealthy {
		t.Errorf("status = %q, want healthy", rec.Status)
	}
	if rec.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", rec.Attempts)
	}
	if rec.LastError != nil {
		t.Errorf("lastError = %v, want nil", *rec.LastError)
	}
}

func TestPollUnhealthyFailsFast(t *testing.T) {
	cli := newClient("boom: connection refused\n",
		det("running", true, docker.HealthStarting),
		det("running", true, docker.HealthUnhealthy))
	rec, err := Poll(context.Background(), cli, Target{Service: "api"}, fastTiming(10))
	if rec.Status != statusUnhealthy {
		t.Errorf("status = %q, want unhealthy", rec.Status)
	}
	var pe *ProbeError
	if !errors.As(err, &pe) {
		t.Fatalf("want *ProbeError, got %T: %v", err, err)
	}
	if pe.Logs == "" || pe.Record.Status != statusUnhealthy {
		t.Errorf("ProbeError missing logs/status: %+v", pe)
	}
	// It must fail on the 2nd inspect, not poll to exhaustion.
	if rec.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (fail-fast)", rec.Attempts)
	}
}

func TestPollExitedFailsFast(t *testing.T) {
	cli := newClient("crash\n",
		det("running", true, docker.HealthStarting),
		det("exited", false, ""))
	_, err := Poll(context.Background(), cli, Target{Service: "api"}, fastTiming(10))
	var pe *ProbeError
	if !errors.As(err, &pe) || pe.Record.Status != statusExited {
		t.Fatalf("want exited ProbeError, got %v", err)
	}
}

func TestPollStartedCondition(t *testing.T) {
	cli := newClient("", det("created", false, ""), det("running", true, ""))
	rec, err := Poll(context.Background(), cli, Target{Service: "cache", Condition: Started}, fastTiming(10))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if rec.Status != statusStarted {
		t.Errorf("status = %q, want started", rec.Status)
	}
}

func TestPollTimeout(t *testing.T) {
	// Always "starting" → never healthy → must time out within the budget.
	cli := newClient("still booting\n", det("running", true, docker.HealthStarting))
	rec, err := Poll(context.Background(), cli, Target{Service: "api"}, fastTiming(3))
	var pe *ProbeError
	if !errors.As(err, &pe) || rec.Status != statusTimeout {
		t.Fatalf("want timeout ProbeError, got status=%q err=%v", rec.Status, err)
	}
	if pe.Logs == "" {
		t.Error("timeout diagnostic should still inline logs")
	}
}

func TestPollTransientInspectErrorRecovers(t *testing.T) {
	cli := &seqClient{
		MockClient: &docker.MockClient{},
		states:     []docker.ContainerDetails{det("running", true, docker.HealthHealthy)},
		inspectErr: []error{errors.New("no such container")},
	}
	rec, err := Poll(context.Background(), cli, Target{Service: "api"}, fastTiming(10))
	if err != nil {
		t.Fatalf("Poll should recover after a transient inspect error: %v", err)
	}
	if rec.Status != statusHealthy || rec.Attempts != 2 {
		t.Errorf("rec = %+v, want healthy after 2 attempts", rec)
	}
	if rec.LastError != nil {
		t.Errorf("lastError should clear on success, got %v", *rec.LastError)
	}
}

func TestPollContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cli := newClient("", det("running", true, docker.HealthStarting))
	_, err := Poll(ctx, cli, Target{Service: "api"}, Timing{Interval: time.Hour, Retries: 100, StartPeriod: time.Hour})
	if err == nil {
		t.Fatal("want an error on a cancelled context")
	}
}

func TestCompileDefaults(t *testing.T) {
	got := Compile(nil)
	want := Timing{Interval: DefaultInterval, Timeout: DefaultTimeout, Retries: DefaultRetries, StartPeriod: DefaultStartPeriod}
	if got != want {
		t.Errorf("Compile(nil) = %+v, want %+v", got, want)
	}
}

func TestCompileOverridesAndBudget(t *testing.T) {
	hc := &config.Healthcheck{
		Kind: "http", Interval: "2s", Timeout: "1s", StartPeriod: "10s", Retries: 4,
	}
	got := Compile(hc)
	if got.Interval != 2*time.Second || got.Timeout != time.Second || got.StartPeriod != 10*time.Second || got.Retries != 4 {
		t.Fatalf("Compile = %+v", got)
	}
	// Budget = startPeriod + interval*retries = 10s + 2s*4 = 18s.
	if got.Budget() != 18*time.Second {
		t.Errorf("Budget = %v, want 18s", got.Budget())
	}
}

func TestCompileIgnoresBadDurations(t *testing.T) {
	// Bad/zero durations fall back to defaults (load-time validation is the guard).
	hc := &config.Healthcheck{Kind: "tcp", Interval: "nope", Timeout: "", StartPeriod: "0s"}
	got := Compile(hc)
	if got.Interval != DefaultInterval || got.Timeout != DefaultTimeout || got.StartPeriod != DefaultStartPeriod {
		t.Errorf("Compile with bad durations = %+v, want defaults", got)
	}
}
