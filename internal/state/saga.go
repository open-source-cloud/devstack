package state

import (
	"database/sql"
	"fmt"
)

// Saga phase statuses.
const (
	PhasePending   = "pending"
	PhaseStarted   = "started"
	PhaseSatisfied = "satisfied"
	PhaseFailed    = "failed"
)

// SagaPhase is one row of the saga_phase resumability table (spec 08/09). A phase
// is keyed by (workspace, scope, phase); scope is "" for workspace-wide phases or
// a project name for per-project ones.
type SagaPhase struct {
	Workspace   string
	Scope       string
	Phase       string
	Status      string
	Fingerprint string
	StartedAt   string
	SatisfiedAt string
	Error       string
}

// StartPhase marks a phase started with the given input fingerprint (upsert).
// Clears any prior satisfied/failed/error state for a fresh attempt. Hold the lock.
func (db *DB) StartPhase(workspace, scope, phase, fingerprint string) error {
	_, err := db.Exec(`
		INSERT INTO saga_phase (ctx, workspace, scope, phase, status, fingerprint, started_at)
		VALUES (?,?,?,?,?,?,datetime('now'))
		ON CONFLICT(ctx, workspace, scope, phase)
		DO UPDATE SET status=excluded.status, fingerprint=excluded.fingerprint,
		             started_at=excluded.started_at, satisfied_at=NULL, error=NULL`,
		db.Ctx, workspace, scope, phase, PhaseStarted, fingerprint)
	if err != nil {
		return fmt.Errorf("start phase %s/%s/%s: %w", workspace, scope, phase, err)
	}
	return nil
}

// SatisfyPhase marks a phase satisfied (completed). Hold the lock.
func (db *DB) SatisfyPhase(workspace, scope, phase string) error {
	_, err := db.Exec(`UPDATE saga_phase SET status=?, satisfied_at=datetime('now'), error=NULL
		WHERE ctx=? AND workspace=? AND scope=? AND phase=?`,
		PhaseSatisfied, db.Ctx, workspace, scope, phase)
	if err != nil {
		return fmt.Errorf("satisfy phase %s/%s/%s: %w", workspace, scope, phase, err)
	}
	return nil
}

// FailPhase marks a phase failed with an error message. Hold the lock.
func (db *DB) FailPhase(workspace, scope, phase, errMsg string) error {
	_, err := db.Exec(`UPDATE saga_phase SET status=?, error=?
		WHERE ctx=? AND workspace=? AND scope=? AND phase=?`,
		PhaseFailed, errMsg, db.Ctx, workspace, scope, phase)
	if err != nil {
		return fmt.Errorf("fail phase %s/%s/%s: %w", workspace, scope, phase, err)
	}
	return nil
}

// GetPhase returns a phase row, ok=false if it has never been recorded.
func (db *DB) GetPhase(workspace, scope, phase string) (SagaPhase, bool, error) {
	var p SagaPhase
	var started, satisfied, errMsg sql.NullString
	err := db.QueryRow(`SELECT workspace, scope, phase, status, fingerprint, started_at, satisfied_at, error
		FROM saga_phase WHERE ctx=? AND workspace=? AND scope=? AND phase=?`,
		db.Ctx, workspace, scope, phase).
		Scan(&p.Workspace, &p.Scope, &p.Phase, &p.Status, &p.Fingerprint, &started, &satisfied, &errMsg)
	if err == sql.ErrNoRows {
		return SagaPhase{}, false, nil
	}
	if err != nil {
		return SagaPhase{}, false, fmt.Errorf("get phase: %w", err)
	}
	p.StartedAt, p.SatisfiedAt, p.Error = started.String, satisfied.String, errMsg.String
	return p, true, nil
}

// PhaseSatisfied reports whether a phase is already satisfied for the SAME input
// fingerprint — the skip check a re-run uses to avoid redoing unchanged work. A
// changed fingerprint (config/template/param edit) re-arms the phase.
func (db *DB) PhaseSatisfied(workspace, scope, phase, fingerprint string) (bool, error) {
	p, ok, err := db.GetPhase(workspace, scope, phase)
	if err != nil || !ok {
		return false, err
	}
	return p.Status == PhaseSatisfied && p.Fingerprint == fingerprint, nil
}

// PhasesFor returns all recorded phases for a workspace, ordered, for `status`.
func (db *DB) PhasesFor(workspace string) ([]SagaPhase, error) {
	rows, err := db.Query(`SELECT workspace, scope, phase, status, fingerprint, started_at, satisfied_at, error
		FROM saga_phase WHERE ctx=? AND workspace=? ORDER BY scope, phase`, db.Ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("phases for %s: %w", workspace, err)
	}
	defer rows.Close()
	var out []SagaPhase
	for rows.Next() {
		var p SagaPhase
		var started, satisfied, errMsg sql.NullString
		if err := rows.Scan(&p.Workspace, &p.Scope, &p.Phase, &p.Status, &p.Fingerprint, &started, &satisfied, &errMsg); err != nil {
			return nil, err
		}
		p.StartedAt, p.SatisfiedAt, p.Error = started.String, satisfied.String, errMsg.String
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClearPhase removes a phase row (compensation / forced re-run). Hold the lock.
func (db *DB) ClearPhase(workspace, scope, phase string) error {
	_, err := db.Exec(`DELETE FROM saga_phase WHERE ctx=? AND workspace=? AND scope=? AND phase=?`,
		db.Ctx, workspace, scope, phase)
	if err != nil {
		return fmt.Errorf("clear phase %s/%s/%s: %w", workspace, scope, phase, err)
	}
	return nil
}
