package lock

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"time"
)

// This file is the DISTRIBUTED lock (spec 21, Q-REMOTE-LOCK RESOLVED): a
// session-scoped pg_advisory_lock on the shared cluster Postgres. It serializes
// two developers on two machines against ONE remote backend — the guarantee a
// local flock structurally cannot provide.
//
// Why pg_advisory_lock and not a coordinator daemon: the team Postgres we already
// run IS the coordinator (zero new infra), the lock is session-scoped so it is
// auto-released when the holder disconnects or crashes (a kill -9 on one machine
// lets another machine's next command reconcile cleanly), and it reuses the pgx
// path provisioning already uses. Gotcha (spec 21): advisory locks live on a
// SESSION — a transaction-pooling pgbouncer silently breaks them, so the
// connector must hand out a direct/session connection, not a pooled one.

// advisoryRetry is how often PGLocker re-polls pg_try_advisory_lock while waiting,
// mirroring the flock's TryLockContext cadence.
const advisoryRetry = 100 * time.Millisecond

// AdvisoryConn is the minimal session-connection surface PGLocker needs. It MUST
// be a single dedicated Postgres session (never a pool / transaction-mode
// pgbouncer). Injectable so the locker is unit-testable without a live server.
type AdvisoryConn interface {
	// TryLock runs `SELECT pg_try_advisory_lock($1)` and reports whether the lock
	// was granted on this session (non-blocking).
	TryLock(ctx context.Context, key int64) (bool, error)
	// Unlock runs `SELECT pg_advisory_unlock($1)` for this session.
	Unlock(ctx context.Context, key int64) error
	// Close ends the session (a crash-safe backstop that releases any held lock).
	Close(ctx context.Context) error
}

// PGConnector opens a fresh AdvisoryConn (a dedicated session). PGLocker opens
// one per WithLock and closes it after, so the advisory lock never outlives the
// critical section even if a release is missed.
type PGConnector func(ctx context.Context) (AdvisoryConn, error)

// PGLocker is the distributed Locker. It hashes Subject to a deterministic
// bigint key and takes a session-scoped pg_advisory_lock on it.
type PGLocker struct {
	Subject string
	Connect PGConnector
	// retry is injectable for tests; 0 → advisoryRetry.
	retry time.Duration
}

// NewPGLocker returns a PGLocker for a lock subject and a session connector.
func NewPGLocker(subject string, connect PGConnector) *PGLocker {
	return &PGLocker{Subject: subject, Connect: connect}
}

// AdvisoryKey maps a lock subject to the deterministic int64 pg_advisory_lock
// key: FNV-64a over the subject, reinterpreted as a signed bigint. Deterministic
// across machines and runs, so every client of one cluster hashes the same
// subject to the same key. A hash collision only causes extra (safe)
// serialization between two subjects — never a missed exclusion.
func AdvisoryKey(subject string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(subject))
	return int64(h.Sum64())
}

// WithLock opens a dedicated session, polls pg_try_advisory_lock until the key is
// held or ctx is done, runs fn, then releases the lock and closes the session.
// Polling (rather than the blocking pg_advisory_lock) honors ctx cancellation and
// timeouts instead of parking a backend indefinitely.
func (p *PGLocker) WithLock(ctx context.Context, fn func() error) error {
	if p.Connect == nil {
		return errors.New("pg advisory lock: no session connector configured")
	}
	retry := p.retry
	if retry == 0 {
		retry = advisoryRetry
	}
	key := AdvisoryKey(p.Subject)

	conn, err := p.Connect(ctx)
	if err != nil {
		return fmt.Errorf("open advisory-lock session: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for {
		ok, err := conn.TryLock(ctx, key)
		if err != nil {
			return fmt.Errorf("acquire advisory lock: %w", err)
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for the shared-backend lock (subject %q); another machine may be holding it: %w", p.Subject, ctx.Err())
		case <-time.After(retry):
		}
	}
	// Held. Release (Unlock) runs before Close because deferred calls are LIFO.
	defer func() { _ = conn.Unlock(ctx, key) }()
	return fn()
}
