package state

import "fmt"

// migration is one forward-only schema step. Within a major version migrations
// are strictly additive and never reordered.
type migration struct {
	version int
	stmt    string
}

// migrations is the ordered list applied on Open. Append new steps; never edit
// or remove a released one.
var migrations = []migration{
	{version: 1, stmt: schemaV1},
}

// schemaV1 is the initial ledger (spec 08 §Tables). Every mutable row is scoped
// by `ctx` (the Docker context) so two daemons never share counts.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS docker_context (
    name     TEXT PRIMARY KEY,
    endpoint TEXT
);

CREATE TABLE IF NOT EXISTS shared_service (
    ctx           TEXT NOT NULL,
    name          TEXT NOT NULL,
    engine        TEXT NOT NULL,
    major_version TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'unknown',
    started_at    TEXT,
    PRIMARY KEY (ctx, engine, major_version),
    FOREIGN KEY (ctx) REFERENCES docker_context(name) ON DELETE CASCADE
);

-- ref count for a shared service = COUNT(*) of its rows.
CREATE TABLE IF NOT EXISTS service_ref (
    ctx            TEXT NOT NULL,
    project        TEXT NOT NULL,
    service        TEXT NOT NULL,
    shared_service TEXT NOT NULL,
    PRIMARY KEY (ctx, project, service, shared_service),
    FOREIGN KEY (ctx) REFERENCES docker_context(name) ON DELETE CASCADE
);

-- persisted port allocations; UNIQUE(port) per context eliminates the
-- inter-invocation TOCTOU.
CREATE TABLE IF NOT EXISTS port_alloc (
    ctx     TEXT NOT NULL,
    owner   TEXT NOT NULL,
    purpose TEXT NOT NULL,
    port    INTEGER NOT NULL,
    PRIMARY KEY (ctx, port),
    FOREIGN KEY (ctx) REFERENCES docker_context(name) ON DELETE CASCADE
);

-- ownership ledger: ties each provisioned db/role/bucket to a project so a
-- reaper can reclaim orphans.
CREATE TABLE IF NOT EXISTS provisioned (
    ctx        TEXT NOT NULL,
    project    TEXT NOT NULL,
    kind       TEXT NOT NULL,           -- db | role | bucket | redis_index
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (ctx, project, kind, name),
    FOREIGN KEY (ctx) REFERENCES docker_context(name) ON DELETE CASCADE
);

-- idempotency for firstRun/seed hooks, keyed per provisioned data volume.
CREATE TABLE IF NOT EXISTS hook_run (
    ctx       TEXT NOT NULL,
    project   TEXT NOT NULL,
    hook      TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    ran_at    TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (ctx, project, hook, scope_key),
    FOREIGN KEY (ctx) REFERENCES docker_context(name) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS event_log (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ctx     TEXT NOT NULL,
    ts      TEXT NOT NULL DEFAULT (datetime('now')),
    kind    TEXT NOT NULL,
    subject TEXT,
    reason  TEXT
);
CREATE INDEX IF NOT EXISTS idx_event_log_ctx_ts ON event_log(ctx, ts);

CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// migrate applies any pending migrations inside a transaction per step, backing
// up the DB file before the first mutating step. Forward-only.
func (db *DB) migrate() error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
        version INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return fmt.Errorf("ensure schema_version: %w", err)
	}

	current, err := db.SchemaVersion()
	if err != nil {
		return err
	}

	backedUp := false
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		// Only back up when there is prior applied schema to lose; a fresh DB
		// (current == 0) has nothing worth a backup file.
		if !backedUp && current > 0 {
			if err := db.backup(current); err != nil {
				return err
			}
			backedUp = true
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration v%d: %w", m.version, err)
		}
		if _, err := tx.Exec(m.stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration v%d: %w", m.version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES(?)`, m.version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration v%d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration v%d: %w", m.version, err)
		}
	}
	return nil
}
