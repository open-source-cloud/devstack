package state

import (
	"database/sql"
	"fmt"
)

// This file is the spec-26 workspace registry: a thin machine-wide pointer table
// mapping each known workspace root to its name + lifecycle timestamps. It is the
// ONLY ledger concept that knows where a workspace root lives on disk; everything
// else (projects, refs) is re-derived from the committed workspace.yaml at list
// time. Every row is scoped to the active Docker context (db.Ctx).
//
// RecordWorkspace mutates and MUST run while holding the machine-global flock.
// ListWorkspaces is a lock-free snapshot.

// Workspace is one registry row: a workspace root and its declared name.
type Workspace struct {
	Name      string // workspace.yaml `name` at the time of the last `up`
	Root      string // absolute path to the workspace root (where workspace.yaml lives)
	CreatedAt string
	LastUpAt  string // refreshed on each successful `up`; "" if never
}

// RecordWorkspace upserts the (ctx, root) pointer row, refreshing the name and
// stamping last_up_at. Called on `up` under the flock (Q-WS-REGISTER-WHEN). The
// PK is (ctx, root): a moved checkout becomes a new row, the old one stays until
// `workspace list --prune` reaps it. Mutating — hold the lock.
func (db *DB) RecordWorkspace(name, root string) error {
	_, err := db.Exec(`
		INSERT INTO workspace (ctx, name, root, last_up_at)
		VALUES (?,?,?,datetime('now'))
		ON CONFLICT(ctx, root)
		DO UPDATE SET name=excluded.name, last_up_at=datetime('now')`,
		db.Ctx, name, root)
	if err != nil {
		return fmt.Errorf("record workspace %s (%s): %w", name, root, err)
	}
	return nil
}

// ListWorkspaces returns every registered workspace for this context, ordered by
// name then root for deterministic output. Lock-free.
func (db *DB) ListWorkspaces() ([]Workspace, error) {
	rows, err := db.Query(`SELECT name, root, created_at, last_up_at
		FROM workspace WHERE ctx=? ORDER BY name, root`, db.Ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		var w Workspace
		var lastUp sql.NullString
		if err := rows.Scan(&w.Name, &w.Root, &w.CreatedAt, &lastUp); err != nil {
			return nil, err
		}
		w.LastUpAt = lastUp.String
		out = append(out, w)
	}
	return out, rows.Err()
}

// RemoveWorkspace drops the registry row for a root (the `--prune` path: a root
// that vanished from disk). Returns whether a row was removed. Mutating — hold
// the lock.
func (db *DB) RemoveWorkspace(root string) (bool, error) {
	res, err := db.Exec(`DELETE FROM workspace WHERE ctx=? AND root=?`, db.Ctx, root)
	if err != nil {
		return false, fmt.Errorf("remove workspace %s: %w", root, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
