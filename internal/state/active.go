package state

import (
	"database/sql"
	"fmt"
)

// This file is the spec-30 active-context pointer: the persisted "current"
// workspace root + project for a Docker context. It is the default the CLI
// resolves when neither --project nor DEVSTACK_PROJECT/DEVSTACK_WORKSPACE is set.
// One row per db.Ctx.
//
// SetActiveContext / ClearActiveContext mutate and MUST run while holding the
// machine-global flock. ActiveContext is a lock-free snapshot.

// ActiveContext is the persisted active workspace+project for a Docker context.
type ActiveContext struct {
	WorkspaceRoot string // absolute workspace root (where workspace.yaml lives), "" if unset
	Project       string // active project name within that workspace, "" if unset
	UpdatedAt     string
}

// SetActiveContext upserts the (ctx) active-context row. An empty project clears
// the active project while keeping the workspace root. Mutating — hold the lock.
func (db *DB) SetActiveContext(workspaceRoot, project string) error {
	_, err := db.Exec(`
		INSERT INTO active_context (ctx, workspace_root, project, updated_at)
		VALUES (?,?,?,datetime('now'))
		ON CONFLICT(ctx)
		DO UPDATE SET workspace_root=excluded.workspace_root,
		              project=excluded.project,
		              updated_at=datetime('now')`,
		db.Ctx, workspaceRoot, project)
	if err != nil {
		return fmt.Errorf("set active context (%s/%s): %w", workspaceRoot, project, err)
	}
	return nil
}

// ActiveContext returns the persisted active context for this Docker context.
// The bool is false when none is set. Lock-free.
func (db *DB) ActiveContext() (ActiveContext, bool, error) {
	var a ActiveContext
	var root, project sql.NullString
	err := db.QueryRow(
		`SELECT workspace_root, project, updated_at FROM active_context WHERE ctx=?`, db.Ctx).
		Scan(&root, &project, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return ActiveContext{}, false, nil
	}
	if err != nil {
		return ActiveContext{}, false, fmt.Errorf("read active context: %w", err)
	}
	a.WorkspaceRoot = root.String
	a.Project = project.String
	return a, true, nil
}

// ClearActiveContext removes the active-context row for this context (a no-op if
// absent). Mutating — hold the lock.
func (db *DB) ClearActiveContext() error {
	if _, err := db.Exec(`DELETE FROM active_context WHERE ctx=?`, db.Ctx); err != nil {
		return fmt.Errorf("clear active context: %w", err)
	}
	return nil
}
