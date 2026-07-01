package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// countingSink records how many times Send was invoked so we can prove the
// consent gate short-circuits before ever touching a sink.
type countingSink struct {
	calls  int
	lastEv Event
}

func (c *countingSink) Send(_ context.Context, ev Event) error {
	c.calls++
	c.lastEv = ev
	return nil
}

func TestRecorderDisabledNeverCallsSink(t *testing.T) {
	sink := &countingSink{}
	r := Recorder{Enabled: false, Sink: sink}
	sent := r.Record(context.Background(), Event{Command: "up"})
	if sent {
		t.Fatal("Record reported sent=true while disabled")
	}
	if sink.calls != 0 {
		t.Fatalf("disabled recorder called the sink %d times; want 0", sink.calls)
	}
}

func TestRecorderEnabledCallsSink(t *testing.T) {
	sink := &countingSink{}
	r := Recorder{Enabled: true, Sink: sink}
	sent := r.Record(context.Background(), Event{Command: "up"})
	if !sent || sink.calls != 1 {
		t.Fatalf("enabled recorder: sent=%v calls=%d; want true/1", sent, sink.calls)
	}
	if sink.lastEv.Command != "up" {
		t.Fatalf("sink got command %q", sink.lastEv.Command)
	}
}

// failingSink returns an error to prove the Recorder swallows it (best-effort).
type failingSink struct{ calls int }

func (f *failingSink) Send(context.Context, Event) error {
	f.calls++
	return errors.New("collector exploded")
}

func TestRecorderSwallowsSinkError(t *testing.T) {
	sink := &failingSink{}
	r := Recorder{Enabled: true, Sink: sink}
	if sent := r.Record(context.Background(), Event{}); !sent {
		t.Fatal("expected sent=true even when the sink errors")
	}
	if sink.calls != 1 {
		t.Fatalf("sink calls = %d, want 1", sink.calls)
	}
}

func TestNoopSinkIsDefaultAndSendsNothing(t *testing.T) {
	if _, ok := DefaultSink().(NoopSink); !ok {
		t.Fatalf("DefaultSink() = %T, want NoopSink (ship-empty)", DefaultSink())
	}
	if DefaultEndpoint != "" {
		t.Fatalf("DefaultEndpoint = %q, want empty (ship-empty)", DefaultEndpoint)
	}
	if err := (NoopSink{}).Send(context.Background(), Event{}); err != nil {
		t.Fatalf("NoopSink.Send err = %v", err)
	}
}

// TestEventAllowlistOnly is the privacy guardrail: the Event struct must contain
// ONLY the exhaustive allowlist of coarse fields and no free-form string map or
// escape hatch through which PII could travel.
func TestEventAllowlistOnly(t *testing.T) {
	allowed := map[string]bool{
		"Command": true, "Flags": true, "Outcome": true, "ErrorCategory": true,
		"DurationMS": true, "OS": true, "Arch": true, "IsWSL2": true,
		"ToolVersion": true, "InstallID": true,
	}
	et := reflect.TypeOf(Event{})
	for i := 0; i < et.NumField(); i++ {
		f := et.Field(i)
		if !allowed[f.Name] {
			t.Errorf("Event has un-allowlisted field %q — PII risk", f.Name)
		}
		// No map fields: a map[string]... would be a denylist-defeating escape hatch.
		if f.Type.Kind() == reflect.Map {
			t.Errorf("Event field %q is a map — no free-form metadata allowed", f.Name)
		}
	}
}

// TestNewEventRedactsRawError proves the raw err.Error() text (which may embed a
// path, IP, or username) never reaches the payload — only the category enum does.
func TestNewEventRedactsRawError(t *testing.T) {
	raw := "open /home/alice/acme-secrets/.env: dial tcp 10.0.0.5:5432: connection refused"
	ev := NewEvent(EventInput{
		Command:     "up",
		Err:         errors.New(raw),
		Duration:    1234 * time.Millisecond,
		ToolVersion: "9.9.9",
		InstallID:   "install-xyz",
	})
	if ev.Outcome != OutcomeError {
		t.Fatalf("outcome = %q, want error", ev.Outcome)
	}
	if ev.ErrorCategory == "" {
		t.Fatal("expected an error category")
	}
	blob, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	s := string(blob)
	for _, leak := range []string{"alice", "acme-secrets", ".env", "10.0.0.5", "5432", "connection refused"} {
		if strings.Contains(s, leak) {
			t.Errorf("payload leaked sensitive substring %q: %s", leak, s)
		}
	}
}

func TestNewEventBucketsDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want int64
	}{
		{0, 0},
		{-5 * time.Millisecond, 0},
		{49 * time.Millisecond, 0},
		{51 * time.Millisecond, 100},
		{1840 * time.Millisecond, 1800},
		{1851 * time.Millisecond, 1900},
	}
	for _, c := range cases {
		if got := NewEvent(EventInput{Duration: c.in}).DurationMS; got != c.want {
			t.Errorf("bucket(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNewEventOKOutcomeHasNoCategory(t *testing.T) {
	ev := NewEvent(EventInput{Command: "status", Err: nil})
	if ev.Outcome != OutcomeOK {
		t.Fatalf("outcome = %q, want ok", ev.Outcome)
	}
	if ev.ErrorCategory != "" {
		t.Fatalf("ok outcome carried a category %q", ev.ErrorCategory)
	}
}

func TestCategorizeError(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"Cannot connect to the Docker daemon at unix:///var/run/docker.sock", CategoryDockerDaemonUnreachable},
		{"docker compose version 2.10 is too old, requires >= 2.20", CategoryComposeTooOld},
		{"Error: port is already allocated", CategoryPortInUse},
		{"fatal: Authentication failed for 'https://github.com/x/y'", CategoryGitAuthPrompt},
		{"database is locked", CategoryStateLocked},
		{"some totally unknown failure with /home/bob/secret", CategoryOther},
	}
	for _, c := range cases {
		if got := CategorizeError(errors.New(c.msg)); got != c.want {
			t.Errorf("CategorizeError(%q) = %q, want %q", c.msg, got, c.want)
		}
	}
	if got := CategorizeError(nil); got != "" {
		t.Errorf("CategorizeError(nil) = %q, want empty", got)
	}
}

func TestHTTPSinkDisabledWhenEndpointEmpty(t *testing.T) {
	// An empty endpoint must never dial — belt-and-braces against accidental
	// activation of the (unused) HTTP sink.
	if err := (HTTPSink{Endpoint: ""}).Send(context.Background(), Event{}); err != nil {
		t.Fatalf("HTTPSink with empty endpoint should be a no-op, got %v", err)
	}
}
