package lock

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestSerializesAcquire verifies the core invariant: while one holder has the
// lock, a second acquire with a deadline fails, and succeeds once released. This
// is the concurrency guarantee the whole ledger relies on (spec 08).
func TestSerializesAcquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devstack.lock")

	first := New(path)
	release, err := first.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// A competing holder must NOT get the lock while it is held.
	second := New(path)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := second.Acquire(ctx); err == nil {
		t.Fatal("second acquire succeeded while lock was held; expected timeout")
	}

	// After release, the competitor acquires promptly.
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	rel2, err := second.Acquire(ctx2)
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	_ = rel2()
}

// TestWithLock runs the critical section and releases automatically.
func TestWithLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devstack.lock")
	ran := false
	if err := WithLock(context.Background(), path, func() error {
		ran = true
		// Re-entrancy from a different handle must block — verify it can't be
		// taken with a zero deadline mid-section.
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := New(path).Acquire(ctx); err == nil {
			t.Error("acquired the lock while WithLock held it")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !ran {
		t.Fatal("critical section did not run")
	}
}
