// Package lock is the concurrency spine (DECISIONS D7, spec 08). A coarse
// `gofrs/flock` advisory lock serializes EVERY operation that mutates the
// machine-global ledger or the shared Docker stack across processes. Without it,
// concurrent invocations (two terminals, IDE + terminal, a watch script) race on
// network-ensure TOCTOU, port allocation, ref-count rows, and `CREATE ROLE`.
//
// Single writer; reads are lock-free snapshots. This must wrap mutations from
// the very first commit — retrofitting concurrency safety is the single most
// expensive mistake available here.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// retryInterval is how often we re-poll the lock while waiting.
const retryInterval = 50 * time.Millisecond

// Lock is a process-wide advisory lock backed by a lockfile.
type Lock struct {
	fl   *flock.Flock
	path string
}

// New returns a Lock backed by the given lockfile path. The parent directory is
// created on Acquire. Use a path under xdg.RuntimeDir() (fallback StateHome).
func New(path string) *Lock {
	return &Lock{fl: flock.New(path), path: path}
}

// Release frees the lock. Safe to call via the returned func from Acquire.
type Release func() error

// Acquire blocks until the lock is held or ctx is done. It returns a Release
// closure to unlock. Pass a ctx with a timeout to bound the wait.
func (l *Lock) Acquire(ctx context.Context) (Release, error) {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	ok, err := l.fl.TryLockContext(ctx, retryInterval)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("timed out waiting for the devstack lock (%s); another invocation may be holding it: %w", l.path, err)
		}
		return nil, fmt.Errorf("acquire lock %s: %w", l.path, err)
	}
	if !ok {
		return nil, fmt.Errorf("could not acquire the devstack lock (%s)", l.path)
	}
	return l.fl.Unlock, nil
}

// WithLock acquires the lock at path, runs fn while holding it, and releases it.
// The lock is released even if fn panics is NOT guaranteed — fn should not panic;
// errors are returned.
func WithLock(ctx context.Context, path string, fn func() error) error {
	l := New(path)
	release, err := l.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	return fn()
}
