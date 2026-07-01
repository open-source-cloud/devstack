package orchestrate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-source-cloud/devstack/internal/provision"
)

func TestTransientConnErr(t *testing.T) {
	transient := []string{
		"connect to shared postgres: read tcp 127.0.0.1:46820->127.0.0.1:45432: read: connection reset by peer",
		"failed to receive message: EOF",
		"dial tcp 127.0.0.1:45432: connect: connection refused",
		"the database system is starting up",
	}
	for _, m := range transient {
		if !transientConnErr(errors.New(m)) {
			t.Errorf("expected transient: %q", m)
		}
	}
	permanent := []string{
		"password authentication failed for user \"devstack\"",
		"database \"nope\" does not exist",
		"",
	}
	for _, m := range permanent {
		if transientConnErr(errors.New(m)) {
			t.Errorf("expected permanent: %q", m)
		}
	}
	if transientConnErr(nil) {
		t.Error("nil is not transient")
	}
}

// fakeConn is a throwaway provision.Conn for connector return values.
type fakeConn struct{}

func (fakeConn) Exec(context.Context, string, ...any) error           { return nil }
func (fakeConn) Exists(context.Context, string, ...any) (bool, error) { return false, nil }

func withNoSleep(t *testing.T) {
	t.Helper()
	orig := sleepFn
	sleepFn = func(ctx context.Context, _ time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	t.Cleanup(func() { sleepFn = orig })
}

func TestRetryingPgConnect_EventuallySucceeds(t *testing.T) {
	withNoSleep(t)
	calls := 0
	base := PgConnector(func(context.Context, string) (provision.Conn, func() error, error) {
		calls++
		if calls < 3 {
			return nil, nil, errors.New("read: connection reset by peer")
		}
		return fakeConn{}, func() error { return nil }, nil
	})
	conn, closeFn, err := retryingPgConnect(base)(context.Background(), "dsn")
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if conn == nil || closeFn == nil {
		t.Fatal("want a live conn + close on success")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (two transient failures then success)", calls)
	}
}

func TestRetryingPgConnect_FailsFastOnPermanent(t *testing.T) {
	withNoSleep(t)
	calls := 0
	base := PgConnector(func(context.Context, string) (provision.Conn, func() error, error) {
		calls++
		return nil, nil, errors.New("password authentication failed")
	})
	_, _, err := retryingPgConnect(base)(context.Background(), "dsn")
	if err == nil {
		t.Fatal("want the permanent error surfaced")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on a permanent error)", calls)
	}
}

func TestRetryingPgConnect_BudgetBounded(t *testing.T) {
	withNoSleep(t)
	// Drive a synthetic clock so the 30s budget elapses without real waiting.
	origNow := nowFn
	tick := time.Unix(0, 0)
	nowFn = func() time.Time { tick = tick.Add(5 * time.Second); return tick }
	t.Cleanup(func() { nowFn = origNow })

	calls := 0
	base := PgConnector(func(context.Context, string) (provision.Conn, func() error, error) {
		calls++
		return nil, nil, errors.New("connection refused")
	})
	_, _, err := retryingPgConnect(base)(context.Background(), "dsn")
	if err == nil {
		t.Fatal("want the transient error surfaced after the budget elapses")
	}
	if calls < 2 {
		t.Errorf("calls = %d, want ≥2 (retried before giving up)", calls)
	}
}

func TestRetryingPgConnect_HonorsContextCancel(t *testing.T) {
	withNoSleep(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	base := PgConnector(func(context.Context, string) (provision.Conn, func() error, error) {
		calls++
		return nil, nil, errors.New("connection reset by peer")
	})
	_, _, err := retryingPgConnect(base)(ctx, "dsn")
	if err == nil {
		t.Fatal("want an error when ctx is already cancelled")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (a cancelled ctx stops the retry loop)", calls)
	}
}

func TestRetryingPgConnect_NilPassthrough(t *testing.T) {
	if retryingPgConnect(nil) != nil {
		t.Error("retryingPgConnect(nil) must be nil so the default connector is used downstream")
	}
}
