// Package provision creates per-project data isolation on the shared engines
// (spec 03, ARCHITECTURE §5): an idempotent, existence-guarded Postgres role +
// database per project (NOT initdb.d, which only runs on a first-init empty
// PGDATA). The SQL logic sits behind the Conn interface so it is unit-testable
// without a live server; the pgx-backed Conn (pgx.go) is the real path.
//
// All provisioning mutations happen while the caller holds the machine-global
// flock (CREATE ROLE / CREATE DATABASE race otherwise — DECISIONS D7/D8).
package provision

import (
	"context"
	"fmt"
	"strings"
)

// Conn is the minimal Postgres surface the provisioner needs.
type Conn interface {
	// Exec runs a statement (DDL/GRANT). CREATE DATABASE cannot run in a
	// transaction, so implementations must execute statements unwrapped.
	Exec(ctx context.Context, sql string, args ...any) error
	// Exists reports whether a guard query (e.g. SELECT 1 FROM pg_roles ...)
	// returns at least one row.
	Exists(ctx context.Context, sql string, args ...any) (bool, error)
}

// Credentials is the per-project Postgres connection identity, returned so the
// orchestrator can inject it into the consuming service's env (the password is a
// secret — passed via exec env, never written to a generated file, §7.5).
type Credentials struct {
	Role     string
	Database string
	Password string
}

// Postgres provisions per-project roles and databases on a shared Postgres.
type Postgres struct{}

// EnsureProject idempotently ensures a login role and an owned database exist for
// project, with password kept in sync, and locks down PUBLIC so each role sees
// only its own database. Existence-guarded because CREATE ROLE / CREATE DATABASE
// are not idempotent (DECISIONS D8). Returns the resolved credentials.
func (Postgres) EnsureProject(ctx context.Context, conn Conn, project, password string) (Credentials, error) {
	role := pgIdent(project)
	db := role // per-project database shares the role's name

	// 1. Role — create or keep its password in sync.
	roleExists, err := conn.Exists(ctx, `SELECT 1 FROM pg_roles WHERE rolname = $1`, role)
	if err != nil {
		return Credentials{}, fmt.Errorf("check role %q: %w", role, err)
	}
	if roleExists {
		if err := conn.Exec(ctx, `ALTER ROLE `+quoteIdent(role)+` WITH LOGIN PASSWORD `+quoteLiteral(password)); err != nil {
			return Credentials{}, fmt.Errorf("alter role %q: %w", role, err)
		}
	} else {
		if err := conn.Exec(ctx, `CREATE ROLE `+quoteIdent(role)+` WITH LOGIN PASSWORD `+quoteLiteral(password)); err != nil {
			return Credentials{}, fmt.Errorf("create role %q: %w", role, err)
		}
	}

	// 2. Database — guarded create (CREATE DATABASE is not idempotent and cannot
	// run in a transaction).
	dbExists, err := conn.Exists(ctx, `SELECT 1 FROM pg_database WHERE datname = $1`, db)
	if err != nil {
		return Credentials{}, fmt.Errorf("check database %q: %w", db, err)
	}
	if !dbExists {
		if err := conn.Exec(ctx, `CREATE DATABASE `+quoteIdent(db)+` OWNER `+quoteIdent(role)); err != nil {
			return Credentials{}, fmt.Errorf("create database %q: %w", db, err)
		}
	}

	// 3. Privileges — revoke PUBLIC, grant the owning role (idempotent).
	for _, stmt := range []string{
		`REVOKE ALL ON DATABASE ` + quoteIdent(db) + ` FROM PUBLIC`,
		`GRANT ALL ON DATABASE ` + quoteIdent(db) + ` TO ` + quoteIdent(role),
	} {
		if err := conn.Exec(ctx, stmt); err != nil {
			return Credentials{}, fmt.Errorf("grant on %q: %w", db, err)
		}
	}

	return Credentials{Role: role, Database: db, Password: password}, nil
}

// GrantLevel is the privilege tier a role receives on a database (spec 29 §db
// role/grant). read = SELECT; write = +INSERT/UPDATE/DELETE; admin = ALL.
type GrantLevel string

const (
	// GrantRead grants CONNECT + USAGE + SELECT (a reporting/read-replica role).
	GrantRead GrantLevel = "read"
	// GrantWrite grants read + INSERT/UPDATE/DELETE (an application role).
	GrantWrite GrantLevel = "write"
	// GrantAdmin grants ALL on the database + schema (an owner-equivalent role).
	GrantAdmin GrantLevel = "admin"
)

// EnsureRole idempotently ensures a LOGIN role exists with the given password,
// keeping the password in sync on re-run. Existence-guarded because CREATE ROLE is
// not idempotent (DECISIONS D8); the SQL runs under the caller's flock. Returns the
// sanitized role identifier (hyphens → underscores).
func (Postgres) EnsureRole(ctx context.Context, conn Conn, role, password string) (string, error) {
	r := pgIdent(role)
	exists, err := conn.Exists(ctx, `SELECT 1 FROM pg_roles WHERE rolname = $1`, r)
	if err != nil {
		return "", fmt.Errorf("check role %q: %w", r, err)
	}
	if exists {
		if err := conn.Exec(ctx, `ALTER ROLE `+quoteIdent(r)+` WITH LOGIN PASSWORD `+quoteLiteral(password)); err != nil {
			return "", fmt.Errorf("alter role %q: %w", r, err)
		}
	} else {
		if err := conn.Exec(ctx, `CREATE ROLE `+quoteIdent(r)+` WITH LOGIN PASSWORD `+quoteLiteral(password)); err != nil {
			return "", fmt.Errorf("create role %q: %w", r, err)
		}
	}
	return r, nil
}

// EnsureDatabase idempotently ensures database db exists, owned by ownerRole.
// Existence-guarded (CREATE DATABASE is not idempotent and cannot run in a
// transaction). Unlike EnsureProject it creates NO new role — the owner is an
// existing role (typically the project role), matching `db create <name>` where
// the owner is the project (spec 29). Returns the sanitized database identifier.
func (Postgres) EnsureDatabase(ctx context.Context, conn Conn, db, ownerRole string) (string, error) {
	d := pgIdent(db)
	owner := pgIdent(ownerRole)
	exists, err := conn.Exists(ctx, `SELECT 1 FROM pg_database WHERE datname = $1`, d)
	if err != nil {
		return "", fmt.Errorf("check database %q: %w", d, err)
	}
	if !exists {
		if err := conn.Exec(ctx, `CREATE DATABASE `+quoteIdent(d)+` OWNER `+quoteIdent(owner)); err != nil {
			return "", fmt.Errorf("create database %q: %w", d, err)
		}
	}
	return d, nil
}

// Grant applies a privilege tier to role on database db. GRANT is idempotent so
// this is safe to re-run. The schema/table grants apply to the CURRENTLY-CONNECTED
// database's public schema, so the caller must connect to db before calling Grant
// (the resource.Postgres provisioner does). The database-level CONNECT/ALL grants
// apply regardless of the connected database.
func (Postgres) Grant(ctx context.Context, conn Conn, role, db string, level GrantLevel) error {
	r := quoteIdent(pgIdent(role))
	d := quoteIdent(pgIdent(db))
	stmts := []string{`GRANT CONNECT ON DATABASE ` + d + ` TO ` + r}
	switch level {
	case GrantRead:
		stmts = append(stmts,
			`GRANT USAGE ON SCHEMA public TO `+r,
			`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+r,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO `+r,
		)
	case GrantWrite:
		stmts = append(stmts,
			`GRANT USAGE ON SCHEMA public TO `+r,
			`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO `+r,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO `+r,
		)
	case GrantAdmin:
		stmts = append(stmts,
			`GRANT ALL PRIVILEGES ON DATABASE `+d+` TO `+r,
			`GRANT ALL ON SCHEMA public TO `+r,
			`GRANT ALL ON ALL TABLES IN SCHEMA public TO `+r,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO `+r,
		)
	default:
		return fmt.Errorf("unknown grant level %q (want read|write|admin)", level)
	}
	for _, s := range stmts {
		if err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("grant %s on %q to %q: %w", level, db, role, err)
		}
	}
	return nil
}

// pgIdent maps a (dsname-validated) project name to a safe unquoted-friendly
// Postgres identifier: hyphens become underscores. The result is still quoted at
// use so any residual characters are handled.
func pgIdent(project string) string {
	return strings.ReplaceAll(project, "-", "_")
}

// quoteIdent double-quotes a Postgres identifier, doubling embedded quotes.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteLiteral single-quotes a Postgres string literal, doubling embedded
// quotes. Used for the role password (which cannot be a bind parameter in
// CREATE/ALTER ROLE).
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
