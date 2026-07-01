package state

import (
	"database/sql"
	"fmt"
)

// This file is the M2 data layer over the spec-08 tables: shared-service rows,
// reference counting, port allocation, and the provisioning ownership ledger
// (spec 03, spec 08). Every row is scoped to the active Docker context (db.Ctx).
//
// MUTATING methods here must be called while holding the machine-global flock
// (internal/lock) — SQLite WAL + busy_timeout alone do not make concurrent
// writers safe (DECISIONS D6/D7). Read methods are lock-free snapshots.

// SharedService is one shared infrastructure instance, keyed by (engine,
// majorVersion) per the version-conflict policy (one instance per pair).
type SharedService struct {
	Name         string // instance/alias name, e.g. "shared-postgres-16"
	Engine       string // postgres | redis | minio | ...
	MajorVersion string
	Status       string // running | stopped | unknown
	StartedAt    string // RFC3339-ish, "" if never started
}

// Ref is one service_ref row: a project's service consuming a shared instance.
type Ref struct {
	Project       string
	Service       string
	SharedService string
}

// Provisioned is one ownership row (db/role/bucket/redis_index → project).
type Provisioned struct {
	Project   string
	Kind      string
	Name      string
	CreatedAt string
}

// --- shared services -------------------------------------------------------

// UpsertSharedService records (or updates the status of) a shared instance keyed
// by (engine, majorVersion). Mutating — hold the lock.
func (db *DB) UpsertSharedService(s SharedService) error {
	_, err := db.Exec(`
		INSERT INTO shared_service (ctx, name, engine, major_version, status, started_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(ctx, engine, major_version)
		DO UPDATE SET name=excluded.name, status=excluded.status,
		             started_at=COALESCE(excluded.started_at, shared_service.started_at)`,
		db.Ctx, s.Name, s.Engine, s.MajorVersion, nz(s.Status, "unknown"), nullIf(s.StartedAt))
	if err != nil {
		return fmt.Errorf("upsert shared service %s/%s: %w", s.Engine, s.MajorVersion, err)
	}
	return nil
}

// SetSharedStatus updates the status, stamping started_at when a service
// transitions to running. Hold the lock.
func (db *DB) SetSharedStatus(engine, major, status string) error {
	var err error
	if status == "running" {
		_, err = db.Exec(`UPDATE shared_service
			SET status=?, started_at=datetime('now')
			WHERE ctx=? AND engine=? AND major_version=?`, status, db.Ctx, engine, major)
	} else {
		_, err = db.Exec(`UPDATE shared_service SET status=?
			WHERE ctx=? AND engine=? AND major_version=?`, status, db.Ctx, engine, major)
	}
	if err != nil {
		return fmt.Errorf("set status for %s/%s: %w", engine, major, err)
	}
	return nil
}

// GetSharedService returns the instance for (engine, major), ok=false if absent.
func (db *DB) GetSharedService(engine, major string) (SharedService, bool, error) {
	var s SharedService
	var started sql.NullString
	err := db.QueryRow(`SELECT name, engine, major_version, status, started_at
		FROM shared_service WHERE ctx=? AND engine=? AND major_version=?`,
		db.Ctx, engine, major).Scan(&s.Name, &s.Engine, &s.MajorVersion, &s.Status, &started)
	if err == sql.ErrNoRows {
		return SharedService{}, false, nil
	}
	if err != nil {
		return SharedService{}, false, fmt.Errorf("get shared service: %w", err)
	}
	s.StartedAt = started.String
	return s, true, nil
}

// ListSharedServices returns all shared instances for this context, ordered.
func (db *DB) ListSharedServices() ([]SharedService, error) {
	rows, err := db.Query(`SELECT name, engine, major_version, status, started_at
		FROM shared_service WHERE ctx=? ORDER BY engine, major_version`, db.Ctx)
	if err != nil {
		return nil, fmt.Errorf("list shared services: %w", err)
	}
	defer rows.Close()
	var out []SharedService
	for rows.Next() {
		var s SharedService
		var started sql.NullString
		if err := rows.Scan(&s.Name, &s.Engine, &s.MajorVersion, &s.Status, &started); err != nil {
			return nil, err
		}
		s.StartedAt = started.String
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- reference counting ----------------------------------------------------

// AddRef records that project's service consumes shared (idempotent). Hold the lock.
func (db *DB) AddRef(project, service, shared string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO service_ref (ctx, project, service, shared_service)
		VALUES (?,?,?,?)`, db.Ctx, project, service, shared)
	if err != nil {
		return fmt.Errorf("add ref %s/%s -> %s: %w", project, service, shared, err)
	}
	return nil
}

// RemoveProjectRefs deletes every ref row for a project (used by `down`).
// Returns the number removed. Hold the lock.
func (db *DB) RemoveProjectRefs(project string) (int, error) {
	res, err := db.Exec(`DELETE FROM service_ref WHERE ctx=? AND project=?`, db.Ctx, project)
	if err != nil {
		return 0, fmt.Errorf("remove refs for %s: %w", project, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RefCount returns how many refs point at a shared instance (the ref count =
// COUNT(*), derived from reality, not a trusted counter).
func (db *DB) RefCount(shared string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM service_ref WHERE ctx=? AND shared_service=?`,
		db.Ctx, shared).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("ref count for %s: %w", shared, err)
	}
	return n, nil
}

// ProjectsUsing returns the distinct projects referencing a shared instance.
func (db *DB) ProjectsUsing(shared string) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT project FROM service_ref
		WHERE ctx=? AND shared_service=? ORDER BY project`, db.Ctx, shared)
	if err != nil {
		return nil, fmt.Errorf("projects using %s: %w", shared, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllRefs returns every ref row for this context.
func (db *DB) AllRefs() ([]Ref, error) {
	rows, err := db.Query(`SELECT project, service, shared_service FROM service_ref
		WHERE ctx=? ORDER BY project, service, shared_service`, db.Ctx)
	if err != nil {
		return nil, fmt.Errorf("all refs: %w", err)
	}
	defer rows.Close()
	var out []Ref
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.Project, &r.Service, &r.SharedService); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneRefsForProjectsNotIn deletes ref rows whose project is not in live, and
// returns what was pruned (so the caller can event-log it). This is the
// self-healing reconcile: the ref set is corrected against running reality on
// every command (spec 03). Hold the lock.
func (db *DB) PruneRefsForProjectsNotIn(live map[string]bool) ([]Ref, error) {
	all, err := db.AllRefs()
	if err != nil {
		return nil, err
	}
	var pruned []Ref
	for _, r := range all {
		if !live[r.Project] {
			if _, err := db.Exec(`DELETE FROM service_ref WHERE ctx=? AND project=? AND service=? AND shared_service=?`,
				db.Ctx, r.Project, r.Service, r.SharedService); err != nil {
				return pruned, fmt.Errorf("prune ref %s/%s: %w", r.Project, r.Service, err)
			}
			pruned = append(pruned, r)
		}
	}
	return pruned, nil
}

// --- port allocation -------------------------------------------------------

// AllocatedPorts returns the set of ports already persisted for this context
// (one half of the union the bind-test must consult).
func (db *DB) AllocatedPorts() (map[int]bool, error) {
	rows, err := db.Query(`SELECT port FROM port_alloc WHERE ctx=?`, db.Ctx)
	if err != nil {
		return nil, fmt.Errorf("allocated ports: %w", err)
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// PortFor returns the port already allocated to (owner, purpose), if any.
func (db *DB) PortFor(owner, purpose string) (int, bool, error) {
	var p int
	err := db.QueryRow(`SELECT port FROM port_alloc WHERE ctx=? AND owner=? AND purpose=?`,
		db.Ctx, owner, purpose).Scan(&p)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("port for %s/%s: %w", owner, purpose, err)
	}
	return p, true, nil
}

// AllocatePort returns a stable host port for (owner, purpose): the existing one
// if already allocated, else the first port in [base, max] that is neither
// persisted nor rejected by isFree (the advisory bind-test ∪ Docker-published
// union, supplied by the caller). The chosen port is persisted immediately,
// inside the lock, which eliminates the inter-invocation TOCTOU. Hold the lock.
func (db *DB) AllocatePort(owner, purpose string, base, max int, isFree func(int) bool) (int, error) {
	if p, ok, err := db.PortFor(owner, purpose); err != nil || ok {
		return p, err
	}
	allocated, err := db.AllocatedPorts()
	if err != nil {
		return 0, err
	}
	for port := base; port <= max; port++ {
		if allocated[port] {
			continue
		}
		if isFree != nil && !isFree(port) {
			continue
		}
		// UNIQUE(ctx, port) is the backstop against a concurrent insert; under the
		// flock this never trips, but treat a conflict as "try the next port".
		if _, err := db.Exec(`INSERT INTO port_alloc (ctx, owner, purpose, port) VALUES (?,?,?,?)`,
			db.Ctx, owner, purpose, port); err != nil {
			allocated[port] = true
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port in range %d-%d for %s/%s", base, max, owner, purpose)
}

// ReleasePortsFor removes all port allocations owned by owner (used on teardown).
func (db *DB) ReleasePortsFor(owner string) error {
	_, err := db.Exec(`DELETE FROM port_alloc WHERE ctx=? AND owner=?`, db.Ctx, owner)
	if err != nil {
		return fmt.Errorf("release ports for %s: %w", owner, err)
	}
	return nil
}

// --- provisioning ownership ledger ----------------------------------------

// RecordProvisioned ties a provisioned db/role/bucket/redis_index to a project
// (idempotent). Hold the lock.
func (db *DB) RecordProvisioned(project, kind, name string) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO provisioned (ctx, project, kind, name)
		VALUES (?,?,?,?)`, db.Ctx, project, kind, name)
	if err != nil {
		return fmt.Errorf("record provisioned %s %s/%s: %w", kind, project, name, err)
	}
	return nil
}

// ProvisionedFor returns the ownership rows for a project.
func (db *DB) ProvisionedFor(project string) ([]Provisioned, error) {
	return db.queryProvisioned(`SELECT project, kind, name, created_at FROM provisioned
		WHERE ctx=? AND project=? ORDER BY kind, name`, db.Ctx, project)
}

// AllProvisioned returns every ownership row for this context.
func (db *DB) AllProvisioned() ([]Provisioned, error) {
	return db.queryProvisioned(`SELECT project, kind, name, created_at FROM provisioned
		WHERE ctx=? ORDER BY project, kind, name`, db.Ctx)
}

// OrphanedProvisioned returns ownership rows whose project is not in active —
// the candidates a `db gc` reaper reclaims (with explicit confirmation).
func (db *DB) OrphanedProvisioned(active map[string]bool) ([]Provisioned, error) {
	all, err := db.AllProvisioned()
	if err != nil {
		return nil, err
	}
	var orphans []Provisioned
	for _, p := range all {
		if !active[p.Project] {
			orphans = append(orphans, p)
		}
	}
	return orphans, nil
}

// RemoveProvisioned drops a single ownership row (project, kind, name) after the
// underlying resource has been dropped — the single-resource teardown used by
// `resource rm` and `resource gc` (spec 27). Idempotent (a no-op if absent).
// `kind` is free-text, so no migration is needed for new kinds. Hold the lock.
func (db *DB) RemoveProvisioned(project, kind, name string) error {
	_, err := db.Exec(`DELETE FROM provisioned WHERE ctx=? AND project=? AND kind=? AND name=?`,
		db.Ctx, project, kind, name)
	if err != nil {
		return fmt.Errorf("remove provisioned %s %s/%s: %w", kind, project, name, err)
	}
	return nil
}

// RemoveProvisionedForProject drops a project's ownership rows (after the actual
// db/role/bucket has been dropped). Hold the lock.
func (db *DB) RemoveProvisionedForProject(project string) (int, error) {
	res, err := db.Exec(`DELETE FROM provisioned WHERE ctx=? AND project=?`, db.Ctx, project)
	if err != nil {
		return 0, fmt.Errorf("remove provisioned for %s: %w", project, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AllocateRedisIndex returns a stable logical Redis DB index (0-15) for a
// project: the existing one if already allocated, else the lowest free index,
// recorded as a provisioned(kind=redis_index). Hold the lock.
func (db *DB) AllocateRedisIndex(project string) (int, error) {
	// Already allocated?
	rows, err := db.ProvisionedFor(project)
	if err != nil {
		return 0, err
	}
	for _, p := range rows {
		if p.Kind == "redis_index" {
			var idx int
			if _, err := fmt.Sscanf(p.Name, "%d", &idx); err == nil {
				return idx, nil
			}
		}
	}
	// Find the lowest free index across all projects.
	used := map[int]bool{}
	all, err := db.AllProvisioned()
	if err != nil {
		return 0, err
	}
	for _, p := range all {
		if p.Kind == "redis_index" {
			var idx int
			if _, err := fmt.Sscanf(p.Name, "%d", &idx); err == nil {
				used[idx] = true
			}
		}
	}
	for i := 0; i <= 15; i++ {
		if !used[i] {
			if err := db.RecordProvisioned(project, "redis_index", fmt.Sprintf("%d", i)); err != nil {
				return 0, err
			}
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free Redis DB index (0-15 exhausted); use a key prefix beyond 16 projects")
}

// --- helpers ---------------------------------------------------------------

func (db *DB) queryProvisioned(query string, args ...any) ([]Provisioned, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query provisioned: %w", err)
	}
	defer rows.Close()
	var out []Provisioned
	for rows.Next() {
		var p Provisioned
		if err := rows.Scan(&p.Project, &p.Kind, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// nz returns s, or def when s is empty.
func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// nullIf maps "" to a SQL NULL so an absent started_at stays NULL.
func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}
