package provision

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/open-source-cloud/devstack/internal/lock"
)

// AdvisoryConn is a pgx/v5 session implementing lock.AdvisoryConn for the
// distributed pg_advisory_lock (spec 21, Q-REMOTE-LOCK). It is the production
// backing for internal/lock's PGLocker — internal/lock stays a pgx-free leaf, and
// the concrete session lives here where pgx already does.
//
// It MUST be a DIRECT session: pg_advisory_lock is session-scoped, so a
// transaction-pooling pgbouncer in front of the cluster silently defeats it (the
// lock and its release can land on different pooled backends). Point the DSN at a
// direct/session endpoint.
type AdvisoryConn struct{ conn *pgx.Conn }

// ConnectAdvisory opens a dedicated pgx session to dsn for advisory locking.
func ConnectAdvisory(ctx context.Context, dsn string) (*AdvisoryConn, error) {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect advisory session: %w", err)
	}
	return &AdvisoryConn{conn: c}, nil
}

// TryLock runs `SELECT pg_try_advisory_lock($1)` and reports whether the session
// now holds the lock (non-blocking).
func (a *AdvisoryConn) TryLock(ctx context.Context, key int64) (bool, error) {
	var ok bool
	if err := a.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// Unlock releases the session's advisory lock for key.
func (a *AdvisoryConn) Unlock(ctx context.Context, key int64) error {
	_, err := a.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
	return err
}

// Close ends the session (releasing any lock it still holds — the crash-safe
// backstop that makes advisory locks self-heal on a dead holder).
func (a *AdvisoryConn) Close(ctx context.Context) error { return a.conn.Close(ctx) }

// PGLockConnector returns a lock.PGConnector that opens a FRESH advisory session
// per acquisition against dsn — the session-per-critical-section discipline
// PGLocker relies on so a lock never outlives its section.
func PGLockConnector(dsn string) lock.PGConnector {
	return func(ctx context.Context) (lock.AdvisoryConn, error) {
		return ConnectAdvisory(ctx, dsn)
	}
}
