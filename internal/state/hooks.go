package state

import (
	"database/sql"
	"fmt"
)

// This file is the CRUD over the spec-08/spec-11 `hook_run` table: the
// idempotency ledger for firstRun/postPull hooks. A row exists IFF that hook is
// satisfied for that scope_key (e.g. the provisioned data-volume identity for
// firstRun, the resolved commit SHA for postPull). The cardinal rule (spec 11):
// a row is written ONLY on hook success, inside the flock — so the ledger always
// reflects success and a failed/interrupted hook re-runs next time.
//
// HookSatisfied is a lock-free read; RecordHookRun / DeleteHookRuns mutate and
// MUST be called while holding the machine-global flock (internal/lock).

// HookSatisfied reports whether (project, hook, scopeKey) already has a recorded
// successful run for this Docker context. Lock-free.
func (db *DB) HookSatisfied(project, hook, scopeKey string) (bool, error) {
	var one int
	err := db.QueryRow(`SELECT 1 FROM hook_run
		WHERE ctx=? AND project=? AND hook=? AND scope_key=?`,
		db.Ctx, project, hook, scopeKey).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("hook satisfied check %s/%s: %w", project, hook, err)
	}
	return true, nil
}

// RecordHookRun marks (project, hook, scopeKey) satisfied (idempotent — a repeat
// is a no-op). Call only on hook success, inside the flock.
func (db *DB) RecordHookRun(project, hook, scopeKey string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO hook_run (ctx, project, hook, scope_key)
		VALUES (?,?,?,?)`, db.Ctx, project, hook, scopeKey)
	if err != nil {
		return fmt.Errorf("record hook run %s/%s: %w", project, hook, err)
	}
	return nil
}

// DeleteHookRuns clears recorded runs so the hooks re-arm (the `--force-hooks`
// path, spec 11). With hook=="" it clears every hook for the project; otherwise
// just the named hook. Returns the number of rows removed. Hold the flock.
func (db *DB) DeleteHookRuns(project, hook string) (int, error) {
	var (
		res sql.Result
		err error
	)
	if hook == "" {
		res, err = db.Exec(`DELETE FROM hook_run WHERE ctx=? AND project=?`, db.Ctx, project)
	} else {
		res, err = db.Exec(`DELETE FROM hook_run WHERE ctx=? AND project=? AND hook=?`, db.Ctx, project, hook)
	}
	if err != nil {
		return 0, fmt.Errorf("delete hook runs %s/%s: %w", project, hook, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
