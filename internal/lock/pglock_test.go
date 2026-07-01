package lock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAdvisoryKeyDeterministic(t *testing.T) {
	subject := "devstack-shared-ledger"
	first, second := AdvisoryKey(subject), AdvisoryKey(subject)
	if first != second {
		t.Fatalf("same subject must hash to the same key (%d != %d)", first, second)
	}
	if AdvisoryKey("a") == AdvisoryKey("b") {
		t.Fatal("different subjects should (almost surely) differ")
	}
}

// fakeAdvisory is an in-memory model of ONE cluster's advisory-lock table: a
// single map[key]bool guarded by a real mutex, so concurrent PGLockers exercise
// genuine mutual exclusion (compare-and-set), like the real server session.
type fakeAdvisory struct {
	mu   sync.Mutex
	held map[int64]bool
}

func newFakeAdvisory() *fakeAdvisory { return &fakeAdvisory{held: map[int64]bool{}} }

// session is one connection to the fake server.
type fakeSession struct {
	srv    *fakeAdvisory
	closed bool
	// hooks to simulate failures
	tryErr   error
	closeErr error
}

func (s *fakeSession) TryLock(_ context.Context, key int64) (bool, error) {
	if s.tryErr != nil {
		return false, s.tryErr
	}
	s.srv.mu.Lock()
	defer s.srv.mu.Unlock()
	if s.srv.held[key] {
		return false, nil
	}
	s.srv.held[key] = true
	return true, nil
}

func (s *fakeSession) Unlock(_ context.Context, key int64) error {
	s.srv.mu.Lock()
	defer s.srv.mu.Unlock()
	delete(s.srv.held, key)
	return nil
}

func (s *fakeSession) Close(context.Context) error {
	s.closed = true
	return s.closeErr
}

func (f *fakeAdvisory) connector() PGConnector {
	return func(context.Context) (AdvisoryConn, error) {
		return &fakeSession{srv: f}, nil
	}
}

func TestPGLocker_AcquireRunRelease(t *testing.T) {
	srv := newFakeAdvisory()
	l := &PGLocker{Subject: "s", Connect: srv.connector()}
	ran := false
	if err := l.WithLock(context.Background(), func() error {
		ran = true
		// While fn runs, the key must be held.
		srv.mu.Lock()
		defer srv.mu.Unlock()
		if !srv.held[AdvisoryKey("s")] {
			t.Error("key should be held while fn runs")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
	// Released after WithLock returns.
	if srv.held[AdvisoryKey("s")] {
		t.Error("key should be released after WithLock")
	}
}

func TestPGLocker_PropagatesFnError(t *testing.T) {
	srv := newFakeAdvisory()
	l := &PGLocker{Subject: "s", Connect: srv.connector()}
	sentinel := errors.New("boom")
	if err := l.WithLock(context.Background(), func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("want fn error propagated, got %v", err)
	}
	if srv.held[AdvisoryKey("s")] {
		t.Error("key must be released even when fn errors")
	}
}

func TestPGLocker_NoConnector(t *testing.T) {
	l := &PGLocker{Subject: "s"}
	if err := l.WithLock(context.Background(), func() error { return nil }); err == nil {
		t.Fatal("want error when no connector is configured")
	}
}

func TestPGLocker_ConnectError(t *testing.T) {
	l := &PGLocker{Subject: "s", Connect: func(context.Context) (AdvisoryConn, error) {
		return nil, errors.New("dial refused")
	}}
	ran := false
	err := l.WithLock(context.Background(), func() error { ran = true; return nil })
	if err == nil || ran {
		t.Fatalf("connect error must abort before fn (err=%v ran=%v)", err, ran)
	}
}

func TestPGLocker_CtxTimeoutWhenHeld(t *testing.T) {
	srv := newFakeAdvisory()
	// Pre-hold the key so TryLock always returns false.
	srv.held[AdvisoryKey("s")] = true
	l := &PGLocker{Subject: "s", Connect: srv.connector(), retry: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ran := false
	err := l.WithLock(ctx, func() error { ran = true; return nil })
	if err == nil || ran {
		t.Fatalf("a permanently-held key must time out before fn (err=%v ran=%v)", err, ran)
	}
}

func TestPGLocker_TryLockErrorSurfaces(t *testing.T) {
	l := &PGLocker{Subject: "s", Connect: func(context.Context) (AdvisoryConn, error) {
		return &fakeSession{srv: newFakeAdvisory(), tryErr: errors.New("conn reset")}, nil
	}}
	if err := l.WithLock(context.Background(), func() error { return nil }); err == nil {
		t.Fatal("a TryLock error must surface")
	}
}

// TestPGLocker_Serializes is the concurrency guarantee: N goroutines each take
// the lock and do a deliberately non-atomic read-modify-write; mutual exclusion
// must make the final count exact. Run under -race.
func TestPGLocker_Serializes(t *testing.T) {
	srv := newFakeAdvisory()
	l := &PGLocker{Subject: "ledger", Connect: srv.connector(), retry: time.Millisecond}
	const n = 50
	counter := 0
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			_ = l.WithLock(context.Background(), func() error {
				c := counter
				time.Sleep(time.Microsecond) // widen the race window
				counter = c + 1
				return nil
			})
		})
	}
	wg.Wait()
	if counter != n {
		t.Fatalf("mutual exclusion failed: counter = %d, want %d", counter, n)
	}
}
