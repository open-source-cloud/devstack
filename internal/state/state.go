// Package state owns the machine-global SQLite ledger (DECISIONS D6, spec 08).
//
//   - pure-Go `modernc.org/sqlite` (preserves the CGO-free static binary);
//   - opened with WAL + busy_timeout=5s + foreign_keys=ON;
//   - keyed by Docker context so WSL2's two daemons (Desktop vs in-distro
//     dockerd) never share a ledger and mis-count;
//   - versioned migrations with a backup-before-migrate step.
//
// SQLite locking alone is NOT sufficient under concurrent writers (especially on
// 9p/WSL2): every mutation must additionally hold the package-lock advisory lock.
package state

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/xdg"
)

// DefaultContext is used when no Docker context can be resolved.
const DefaultContext = "default"

// DB wraps the ledger handle plus the active Docker context the ledger is keyed
// by. All higher-level helpers scope their rows to Ctx.
type DB struct {
	*sql.DB
	Ctx  string
	path string
}

// Open ensures the ledger directory exists, opens state.db with the required
// pragmas, runs pending migrations (backing up first), and records the Docker
// context. dockerContext should be the active `docker context` name or the
// effective DOCKER_HOST; empty falls back to DefaultContext.
//
// The mutating section (migrate + context insert) runs under the machine-global
// advisory lock (spec 08): SQLite WAL + busy_timeout alone do NOT make concurrent
// writers safe, so the flock is mandatory. Higher-level operations that compose
// several mutations should take the lock at their own granularity (and must not
// nest a second Open inside that critical section).
func Open(ctx context.Context, dir, dockerContext string) (*DB, error) {
	if dockerContext == "" {
		dockerContext = DefaultContext
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "state.db")

	// Build the DSN via url.URL so paths containing '?'/'#'/spaces are escaped
	// (raw interpolation would let SQLite's URI parser truncate the filename).
	// modernc.org/sqlite honours _pragma query params on every new connection.
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open state.db: %w", err)
	}
	// A single open connection keeps pragma state consistent and means the
	// backup checkpoint cannot race a second writer.
	sqlDB.SetMaxOpenConns(1)

	db := &DB{DB: sqlDB, Ctx: dockerContext, path: path}
	lockPath := filepath.Join(xdg.RuntimeDir(), "devstack.lock")
	if err := lock.WithLock(ctx, lockPath, func() error {
		// Ping forces the first real connection, which applies the DSN pragmas —
		// including the journal_mode=WAL switch that writes the DB header. That
		// conversion takes a write lock, so it MUST run inside the flock: concurrent
		// first-opens racing the WAL switch otherwise hit SQLITE_BUSY immediately
		// (busy_timeout doesn't cover the journal-mode change). Keeping Ping +
		// migrate + ensureContext together under the lock serializes the whole
		// mutating section across processes (spec 08).
		if err := sqlDB.Ping(); err != nil {
			return fmt.Errorf("ping state.db: %w", err)
		}
		if err := db.migrate(); err != nil {
			return err
		}
		return db.ensureContext()
	}); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// ensureContext records the active Docker context row (idempotent).
func (db *DB) ensureContext() error {
	_, err := db.Exec(
		`INSERT INTO docker_context(name) VALUES(?) ON CONFLICT(name) DO NOTHING`,
		db.Ctx,
	)
	if err != nil {
		return fmt.Errorf("record docker context: %w", err)
	}
	return nil
}

// LogEvent appends to the rolling event log so "my DB disappeared" becomes
// answerable. Best-effort: a logging failure never breaks the caller.
func (db *DB) LogEvent(kind, subject, reason string) {
	_, _ = db.Exec(
		`INSERT INTO event_log(ctx, kind, subject, reason) VALUES(?,?,?,?)`,
		db.Ctx, kind, subject, reason,
	)
}

// SchemaVersion returns the highest applied migration version (0 if fresh). The
// schema_version table is created by migrate before this is read.
func (db *DB) SchemaVersion() (int, error) {
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return int(v.Int64), nil
}

// backup writes a consistent, standalone copy of the ledger next to itself
// before a migration mutates it. It uses `VACUUM INTO` rather than a raw file
// copy: in WAL mode, committed rows may still live in state.db-wal, so copying
// only state.db could yield a torn or stale backup. VACUUM INTO produces a clean
// single-file database that already includes WAL content. Written to a temp path
// and renamed so a partial file is never observed. fromVersion is the
// pre-migration schema version (used only to name the file).
func (db *DB) backup(fromVersion int) error {
	dst := fmt.Sprintf("%s.bak.v%d", db.path, fromVersion)
	tmp := dst + ".tmp"
	// VACUUM INTO refuses to overwrite an existing target.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear stale backup temp: %w", err)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		return fmt.Errorf("backup (vacuum into): %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("finalize backup: %w", err)
	}
	return nil
}
