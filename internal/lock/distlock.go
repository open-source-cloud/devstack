package lock

import "context"

// Locker serializes every mutation of the machine-global ledger or the shared
// Docker stack. It is the seam that lets the concurrency spine span a REMOTE
// team backend (spec 21): the local implementation is the gofrs/flock advisory
// lock (a single machine); the remote implementation is a session-scoped
// pg_advisory_lock on the shared cluster Postgres, which serializes across
// MACHINES against one backend.
//
// This exists because a local flock cannot serialize two developers on two
// laptops mutating the SAME remote ledger rows / provisioning / port allocation
// (spec 21 §"the central unsolved problem"). Q-REMOTE-LOCK is RESOLVED: use
// pg_advisory_lock on the cluster DB as the coordinator — zero new infra,
// crash-safe (session-scoped → auto-released on disconnect), reusing pgx.
type Locker interface {
	// WithLock runs fn while holding the lock, releasing it before returning
	// (on success or error). It blocks until the lock is held or ctx is done.
	WithLock(ctx context.Context, fn func() error) error
}

// FileLocker is the LOCAL Locker: the coarse gofrs/flock at a lockfile path
// (today's behavior, verbatim). An empty Path runs fn unlocked (tests).
type FileLocker struct{ Path string }

// NewFileLocker returns a FileLocker bound to a lockfile path (under XDG_RUNTIME_DIR).
func NewFileLocker(path string) FileLocker { return FileLocker{Path: path} }

// WithLock takes the flock at Path, runs fn, and releases it.
func (f FileLocker) WithLock(ctx context.Context, fn func() error) error {
	if f.Path == "" {
		return fn()
	}
	return WithLock(ctx, f.Path, fn)
}

// LockerFor selects the concurrency primitive for a backend. A remote backend
// with a reachable cluster Postgres (connect != nil) serializes across machines
// on a pg_advisory_lock keyed by subject; everything else uses the local flock
// at path. connect is nil until remote host-reachability is wired, so a remote
// backend degrades safely to the local flock (single-machine correctness holds;
// cross-machine serialization is the follow-up that needs the cluster reachable).
//
// The parameter is a plain bool, not a docker.Backend, so internal/lock stays a
// dependency-free leaf (no import cycle).
func LockerFor(remote bool, subject, path string, connect PGConnector) Locker {
	if remote && connect != nil {
		return NewPGLocker(subject, connect)
	}
	return NewFileLocker(path)
}
