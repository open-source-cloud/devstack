package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/open-source-cloud/devstack/internal/config"
)

type callResult struct {
	out string
	err error
}

// fakeExecer consumes a scripted result sequence across both transports and
// records which transport each call used.
type fakeExecer struct {
	seq     []callResult
	i       int
	block   bool // block until ctx is done (to exercise timeout)
	calls   []string
	lastEnv []string
}

func (f *fakeExecer) run(ctx context.Context) (string, error) {
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	idx := f.i
	if idx >= len(f.seq) {
		idx = len(f.seq) - 1
	}
	f.i++
	if idx < 0 {
		return "", nil
	}
	return f.seq[idx].out, f.seq[idx].err
}

func (f *fakeExecer) Host(ctx context.Context, workdir string, env, _ []string) (string, error) {
	f.calls = append(f.calls, "host:"+workdir)
	f.lastEnv = env
	return f.run(ctx)
}

func (f *fakeExecer) Exec(ctx context.Context, service, _ string, env, _ []string) (string, error) {
	f.calls = append(f.calls, "exec:"+service)
	f.lastEnv = env
	return f.run(ctx)
}

type fakeLedger struct {
	satisfied map[string]bool
	recorded  []string
}

func lkey(p, h, s string) string { return p + "|" + h + "|" + s }

func (l *fakeLedger) HookSatisfied(p, h, s string) (bool, error) {
	return l.satisfied[lkey(p, h, s)], nil
}
func (l *fakeLedger) RecordHookRun(p, h, s string) error {
	if l.satisfied == nil {
		l.satisfied = map[string]bool{}
	}
	l.satisfied[lkey(p, h, s)] = true
	l.recorded = append(l.recorded, lkey(p, h, s))
	return nil
}

func hostHook(name string, onFail string) config.Hook {
	return config.Hook{Name: name, Run: "host", Command: []string{"true"}, OnFailure: onFail}
}

func TestRunHostSuccess(t *testing.T) {
	fe := &fakeExecer{seq: []callResult{{out: "ok"}}}
	r := &Runner{Execer: fe}
	out, attempts, err := r.Run(context.Background(), hostHook("x", ""), nil)
	if err != nil || out != "ok" || attempts != 1 {
		t.Fatalf("Run = %q, %d, %v", out, attempts, err)
	}
	if len(fe.calls) != 1 || fe.calls[0] != "host:" {
		t.Errorf("calls = %v, want one host call", fe.calls)
	}
}

func TestRunExecRequiresService(t *testing.T) {
	r := &Runner{Execer: &fakeExecer{}}
	_, _, err := r.Run(context.Background(), config.Hook{Name: "m", Run: "exec", Command: []string{"true"}}, nil)
	if err == nil {
		t.Fatal("run:exec without a service should error")
	}
}

func TestRunRetriesThenSucceeds(t *testing.T) {
	fe := &fakeExecer{seq: []callResult{
		{err: errors.New("boom")}, {err: errors.New("boom")}, {out: "done"},
	}}
	r := &Runner{Execer: fe, Backoff: time.Millisecond}
	h := config.Hook{Name: "m", Run: "host", Command: []string{"true"}, Retries: 3}
	out, attempts, err := r.Run(context.Background(), h, nil)
	if err != nil || out != "done" || attempts != 3 {
		t.Fatalf("Run = %q, %d, %v; want done,3,nil", out, attempts, err)
	}
}

func TestRunRetriesExhausted(t *testing.T) {
	fe := &fakeExecer{seq: []callResult{{err: errors.New("boom")}}}
	r := &Runner{Execer: fe, Backoff: time.Millisecond}
	h := config.Hook{Name: "m", Run: "host", Command: []string{"true"}, Retries: 2}
	_, attempts, err := r.Run(context.Background(), h, nil)
	if err == nil || attempts != 3 {
		t.Fatalf("Run attempts=%d err=%v; want 3 attempts + error", attempts, err)
	}
}

func TestRunTimeout(t *testing.T) {
	fe := &fakeExecer{block: true}
	r := &Runner{Execer: fe}
	h := config.Hook{Name: "slow", Run: "host", Command: []string{"sleep"}, Timeout: "10ms"}
	out, attempts, err := r.Run(context.Background(), h, nil)
	if err == nil {
		t.Fatal("a hook exceeding its timeout should fail")
	}
	if out != "" || attempts != 1 {
		t.Errorf("out=%q attempts=%d, want empty + 1 attempt", out, attempts)
	}
}

func TestRunPhaseAbortStops(t *testing.T) {
	fe := &fakeExecer{seq: []callResult{{err: errors.New("boom")}, {out: "second"}}}
	r := &Runner{Execer: fe}
	hooks := []config.Hook{hostHook("a", "abort"), hostHook("b", "")}
	res, err := r.RunPhase(context.Background(), hooks, PhaseOpts{Phase: "postUp"})
	if err == nil {
		t.Fatal("abort should fail the phase")
	}
	if len(res) != 1 || res[0].Status != StatusFailed {
		t.Fatalf("results = %+v, want one failed", res)
	}
	if len(fe.calls) != 1 {
		t.Errorf("second hook should not run after abort; calls=%v", fe.calls)
	}
}

func TestRunPhaseWarnContinues(t *testing.T) {
	fe := &fakeExecer{seq: []callResult{{err: errors.New("boom")}, {out: "ok"}}}
	r := &Runner{Execer: fe}
	hooks := []config.Hook{hostHook("a", "warn"), hostHook("b", "")}
	res, err := r.RunPhase(context.Background(), hooks, PhaseOpts{Phase: "postUp"})
	if err != nil {
		t.Fatalf("warn should not fail the phase: %v", err)
	}
	if len(res) != 2 || res[0].Status != StatusWarned || res[1].Status != StatusRan {
		t.Fatalf("results = %+v", res)
	}
}

func TestRunPhaseContinueFailsPhaseButRunsRest(t *testing.T) {
	fe := &fakeExecer{seq: []callResult{{err: errors.New("boom")}, {out: "ok"}}}
	r := &Runner{Execer: fe}
	hooks := []config.Hook{hostHook("a", "continue"), hostHook("b", "")}
	res, err := r.RunPhase(context.Background(), hooks, PhaseOpts{Phase: "postUp"})
	if err == nil {
		t.Fatal("continue should still fail the phase overall")
	}
	if len(res) != 2 {
		t.Fatalf("both hooks should run; results=%+v", res)
	}
}

func TestRunPhaseIdempotentSkip(t *testing.T) {
	led := &fakeLedger{satisfied: map[string]bool{lkey("api", "firstRun", "vol-1"): true}}
	fe := &fakeExecer{seq: []callResult{{out: "ran"}}}
	r := &Runner{Execer: fe, Ledger: led}
	hooks := []config.Hook{hostHook("migrate", "")}
	res, err := r.RunPhase(context.Background(), hooks, PhaseOpts{
		Project: "api", Phase: "firstRun", Idempotent: true,
		ScopeKey: func(config.Hook) string { return "vol-1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != StatusSkipped {
		t.Fatalf("results = %+v, want skipped", res)
	}
	if len(fe.calls) != 0 {
		t.Errorf("a satisfied hook must not execute; calls=%v", fe.calls)
	}
}

func TestRunPhaseIdempotentRecordsOnSuccessNotFailure(t *testing.T) {
	// Success path: records under the lock.
	led := &fakeLedger{}
	locked := 0
	fe := &fakeExecer{seq: []callResult{{out: "ok"}}}
	r := &Runner{Execer: fe, Ledger: led, Lock: func(_ context.Context, fn func() error) error {
		locked++
		return fn()
	}}
	scope := func(config.Hook) string { return "vol-1" }
	if _, err := r.RunPhase(context.Background(), []config.Hook{hostHook("migrate", "")}, PhaseOpts{
		Project: "api", Phase: "firstRun", Idempotent: true, ScopeKey: scope,
	}); err != nil {
		t.Fatal(err)
	}
	if len(led.recorded) != 1 || led.recorded[0] != lkey("api", "firstRun", "vol-1") {
		t.Fatalf("recorded = %v, want one row", led.recorded)
	}
	if locked != 1 {
		t.Errorf("record should happen inside the lock exactly once, got %d", locked)
	}

	// Failure path: nothing recorded.
	led2 := &fakeLedger{}
	feFail := &fakeExecer{seq: []callResult{{err: errors.New("boom")}}}
	r2 := &Runner{Execer: feFail, Ledger: led2, Lock: func(_ context.Context, fn func() error) error { return fn() }}
	_, _ = r2.RunPhase(context.Background(), []config.Hook{hostHook("migrate", "abort")}, PhaseOpts{
		Project: "api", Phase: "firstRun", Idempotent: true, ScopeKey: scope,
	})
	if len(led2.recorded) != 0 {
		t.Errorf("a failed firstRun must record nothing, got %v", led2.recorded)
	}
}

func TestBuildEnvSortedSecretsLast(t *testing.T) {
	got := buildEnv(map[string]string{"B": "2", "A": "1"}, []string{"SECRET=x"})
	want := []string{"A=1", "B=2", "SECRET=x"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("buildEnv = %v, want %v", got, want)
	}
}

func TestRunExecTransportRouting(t *testing.T) {
	fe := &fakeExecer{seq: []callResult{{out: "ok"}}}
	r := &Runner{Execer: fe}
	h := config.Hook{Name: "m", Run: "exec", Service: "api", Command: []string{"true"}, Env: map[string]string{"K": "V"}}
	if _, _, err := r.Run(context.Background(), h, []string{"S=1"}); err != nil {
		t.Fatal(err)
	}
	if len(fe.calls) != 1 || fe.calls[0] != "exec:api" {
		t.Fatalf("calls = %v, want exec:api", fe.calls)
	}
	if len(fe.lastEnv) != 2 || fe.lastEnv[0] != "K=V" || fe.lastEnv[1] != "S=1" {
		t.Errorf("env = %v, want [K=V S=1]", fe.lastEnv)
	}
}

// TestOSExecerHost exercises the real host transport (os/exec) hermetically:
// env injection + working directory. No daemon needed.
func TestOSExecerHost(t *testing.T) {
	dir := t.TempDir()
	e := OSExecer{BaseDir: dir}
	out, err := e.Host(context.Background(), "", []string{"GREETING=hi"},
		[]string{"sh", "-c", "echo $GREETING; pwd"})
	if err != nil {
		t.Fatalf("Host: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "hi\n") {
		t.Errorf("output = %q, want it to start with \"hi\\n\"", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("output %q should contain the working dir %q", out, dir)
	}
}
