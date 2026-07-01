package orchestrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/provision"
)

// This file is the imperative side of spec 15's `db reset`: DROP + re-provision a
// project's per-project tenant database on the SHARED Postgres, reusing the exact
// provision-phase host-reachability path (engineTarget → ledger port + loopback
// overlay via `compose up`) so the pgx admin connection reaches the warm server
// WITHOUT publishing a permanent host port.
//
// Unlike snapshot/restore (whose long-running dump/restore PROCESS runs OUTSIDE
// the flock), a reset is a handful of quick DDL statements, so the terminate →
// DROP DATABASE → re-provision SQL runs INSIDE the flock (spec 15: "the drop/
// recreate SQL happen inside the flock"), exactly like the provision phase. The
// never-recreate-a-stateful-shared-service guard still holds: we drop the tenant
// DATABASE only, never the shared container/volume, and terminate only THIS
// tenant's backends (never other tenants' live connections).

// ResetOptions selects the tenant to drop + re-provision.
type ResetOptions struct {
	Project  string // owner project (default: the workspace's single/first project)
	Database string // physical tenant db (default: the project's own <project> db)
	Instance string // shared Postgres instance (default: the first postgres instance)
	Force    bool   // terminate + proceed even if the tenant still has live connections
}

// ResetResult is the outcome of a reset (the recreated empty tenant).
type ResetResult struct {
	Project  string `json:"project"`
	Instance string `json:"instance"`
	Database string `json:"database"`
	Role     string `json:"role"`
}

// liveBackendsSQL detects any backend (other than our own) attached to the tenant
// database — the guard that makes reset refuse a still-connected DB unless --force.
const liveBackendsSQL = `SELECT 1 FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`

// terminateBackendsSQL terminates only THIS tenant's backends (never other
// tenants' — the shared-service isolation guard, spec 15).
const terminateBackendsSQL = `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`

// Reset drops a project's tenant database and re-runs the idempotent provisioner
// to recreate an empty tenant (role kept/recreated, database recreated fresh). The
// terminate → DROP → re-provision DDL runs inside the flock; the loopback overlay
// is applied outside it (engineTarget self-locks the port allocation).
func Reset(ctx context.Context, d UpDeps, opt ResetOptions) (ResetResult, error) {
	proj, dbName, inst, err := resolveTenant(d, opt.Project, opt.Database, opt.Instance)
	if err != nil {
		return ResetResult{}, err
	}

	// Apply the host-reachability overlay (ledger port + `compose up`), same path
	// the provision phase uses. This runs outside the flock (FreeHostPort self-locks).
	target, err := engineTarget(ctx, d, "postgres", inst)
	if err != nil {
		return ResetResult{}, err
	}
	connect := d.PgConnect
	if connect == nil {
		connect = defaultPgConnect
	}
	user := target.AdminEnv["user"]
	pass := target.AdminEnv["password"]
	dsn := provision.DSN(target.Host, target.Port, user, pass, user)

	var result ResetResult
	if err := lock.WithLock(ctx, d.LockPath, func() error {
		conn, closeConn, err := connect(ctx, dsn)
		if err != nil {
			return fmt.Errorf("connect to shared %s on %s:%d: %w", inst, target.Host, target.Port, err)
		}
		defer func() { _ = closeConn() }()

		// Refuse a still-connected tenant unless --force (data-loss guard, spec 15).
		if !opt.Force {
			live, err := conn.Exists(ctx, liveBackendsSQL, dbName)
			if err != nil {
				return fmt.Errorf("check live connections on %q: %w", dbName, err)
			}
			if live {
				return fmt.Errorf("database %q still has active connections; pass --force to terminate them and reset anyway", dbName)
			}
		}

		// Terminate only this tenant's backends, then drop the database. DROP
		// DATABASE cannot run in a transaction and fails with attached sessions —
		// so terminate first (spec 15).
		if err := conn.Exec(ctx, terminateBackendsSQL, dbName); err != nil {
			return fmt.Errorf("terminate sessions on %q: %w", dbName, err)
		}
		if err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quotePgIdent(dbName)); err != nil {
			return fmt.Errorf("drop database %q: %w", dbName, err)
		}

		// Re-provision an empty tenant via the SAME existence-guarded, idempotent
		// SQL the provision phase uses (role kept in sync / recreated, fresh db,
		// predictable dev cred == project name).
		creds, err := provision.Postgres{}.EnsureProject(ctx, conn, proj, proj)
		if err != nil {
			return fmt.Errorf("re-provision %s on %s: %w", proj, inst, err)
		}

		if err := d.DB.RecordProvisioned(proj, "role", creds.Role); err != nil {
			return err
		}
		if err := d.DB.RecordProvisioned(proj, "database", creds.Database); err != nil {
			return err
		}
		d.DB.LogEvent("db.reset", proj, fmt.Sprintf("dropped+recreated %s on %s", creds.Database, generate.SharedAlias(inst)))
		result = ResetResult{Project: proj, Instance: inst, Database: creds.Database, Role: creds.Role}
		return nil
	}); err != nil {
		return ResetResult{}, err
	}
	return result, nil
}

// quotePgIdent double-quotes a Postgres identifier, doubling embedded quotes.
// Local to orchestrate so DROP DATABASE renders a safe identifier (the tenant db
// is already hyphen-sanitized by pgTenantDB, but quoting is the belt-and-braces).
func quotePgIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
