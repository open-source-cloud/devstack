package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-source-cloud/devstack/internal/provision"
)

// PgConnector opens an admin Postgres connection to a DSN. Injectable so the
// Postgres provisioner is unit-testable without a live server (the default wraps
// provision.Connect / pgx). Mirrors orchestrate.PgConnector.
type PgConnector func(ctx context.Context, dsn string) (provision.Conn, func() error, error)

func defaultPgConnect(ctx context.Context, dsn string) (provision.Conn, func() error, error) {
	c, closeFn, err := provision.Connect(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	return c, closeFn, nil
}

// Postgres is the postgres Provisioner: per-project role+database isolation on a
// shared Postgres, wrapping provision.EnsureProject verbatim (same existence-
// guarded, idempotent SQL; same predictable dev-cred where password == owner
// project). It is the reference implementation the other engine provisioners copy.
type Postgres struct {
	// Connect opens the admin connection from a Target DSN; nil → the pgx default.
	Connect PgConnector
}

// Engine reports the shared-template capability this provisioner serves.
func (Postgres) Engine() string { return "postgres" }

// Kinds are the resource kinds this provisioner can create.
func (Postgres) Kinds() []string { return []string{"database", "role", "user"} }

func (p Postgres) connect(ctx context.Context, dsn string) (provision.Conn, func() error, error) {
	if p.Connect != nil {
		return p.Connect(ctx, dsn)
	}
	return defaultPgConnect(ctx, dsn)
}

// dsn builds the admin DSN for the target instance from its admin creds.
func (Postgres) dsn(t Target) string {
	user := t.AdminEnv["user"]
	pass := t.AdminEnv["password"]
	adminDB := t.AdminEnv["database"]
	if adminDB == "" {
		adminDB = user
	}
	return provision.DSN(t.Host, t.Port, user, pass, adminDB)
}

// password resolves the resource's credential: an explicit generated value
// (Params["password"]) when present, else the predictable dev cred (== owner).
func (Postgres) password(r Resource) string {
	if v, ok := r.Params["password"].(string); ok && v != "" {
		return v
	}
	return r.Owner
}

// rolePassword resolves a role/user credential: an explicit generated value when
// present, else the predictable dev cred (== the role NAME, spec 29 §db user).
func (Postgres) rolePassword(r Resource, role string) string {
	if v, ok := r.Params["password"].(string); ok && v != "" {
		return v
	}
	return role
}

// paramStr reads a string param (empty when absent or non-string).
func paramStr(params map[string]any, key string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// dsnDB builds the admin DSN for the target instance, connected to database db
// (falling back to the admin database when db is empty). Used by the role/user
// path so schema/table grants land on the tenant database, not the admin one.
func (Postgres) dsnDB(t Target, db string) string {
	user := t.AdminEnv["user"]
	pass := t.AdminEnv["password"]
	if db == "" {
		db = t.AdminEnv["database"]
		if db == "" {
			db = user
		}
	}
	return provision.DSN(t.Host, t.Port, user, pass, db)
}

// Ensure idempotently provisions the resource. Dispatch by kind:
//
//   - role|user: a tenant-scoped LOGIN role, optionally GRANTed on a target
//     database (Params["db"] + Params["level"]); nothing else is created.
//   - database with Params["owner"]: just CREATE DATABASE … OWNER <owner> (the
//     owner is an existing role, typically the project) — `db create <name>`.
//   - database (no owner): the legacy role+database pair via EnsureProject (the
//     `resource create postgres database` substrate behaviour, byte-for-byte).
//
// Returns the connection facts; the secret password is included so callers mask it.
func (p Postgres) Ensure(ctx context.Context, t Target, r Resource) (Attrs, error) {
	switch r.Kind {
	case "role", "user":
		return p.ensureRole(ctx, t, r)
	case "database":
		if owner := paramStr(r.Params, "owner"); owner != "" {
			return p.ensureDatabase(ctx, t, r, owner)
		}
	}
	identity := r.Name
	if identity == "" {
		identity = r.Owner
	}
	pass := p.password(r)
	conn, closeConn, err := p.connect(ctx, p.dsn(t))
	if err != nil {
		return nil, fmt.Errorf("connect to shared %s on %s:%d: %w", t.Instance, t.Host, t.Port, err)
	}
	defer func() { _ = closeConn() }()

	creds, err := provision.Postgres{}.EnsureProject(ctx, conn, identity, pass)
	if err != nil {
		return nil, err
	}
	return Attrs{
		"host":     sharedHost(t.Instance),
		"port":     "5432",
		"user":     creds.Role,
		"role":     creds.Role,
		"database": creds.Database,
		"password": creds.Password,
	}, nil
}

// ensureDatabase creates just a database owned by an existing role (no new role),
// so `db create orders` on project api yields `api_orders OWNER api`. It returns no
// "role" attr, so the caller records only the database ownership row.
func (p Postgres) ensureDatabase(ctx context.Context, t Target, r Resource, owner string) (Attrs, error) {
	db := r.Name
	if db == "" {
		db = r.Owner
	}
	conn, closeConn, err := p.connect(ctx, p.dsn(t))
	if err != nil {
		return nil, fmt.Errorf("connect to shared %s on %s:%d: %w", t.Instance, t.Host, t.Port, err)
	}
	defer func() { _ = closeConn() }()

	dbIdent, err := provision.Postgres{}.EnsureDatabase(ctx, conn, db, owner)
	if err != nil {
		return nil, err
	}
	return Attrs{
		"host":     sharedHost(t.Instance),
		"port":     "5432",
		"user":     pgIdent(owner),
		"database": dbIdent,
		"owner":    pgIdent(owner),
		"password": owner, // predictable dev-cred == owner project name
	}, nil
}

// ensureRole creates a tenant-scoped LOGIN role (existence-guarded), optionally
// granting it a privilege tier on a target database. When Params["grant_only"] is
// set the role is assumed to exist (`db grant`) and only the GRANT runs — the
// password is never reset. Connects to the target database so schema/table grants
// land on the right database, not the admin one.
func (p Postgres) ensureRole(ctx context.Context, t Target, r Resource) (Attrs, error) {
	role := r.Name
	if role == "" {
		role = r.Owner
	}
	db := paramStr(r.Params, "db")
	conn, closeConn, err := p.connect(ctx, p.dsnDB(t, db))
	if err != nil {
		return nil, fmt.Errorf("connect to shared %s on %s:%d: %w", t.Instance, t.Host, t.Port, err)
	}
	defer func() { _ = closeConn() }()

	pass := p.rolePassword(r, role)
	roleIdent := pgIdent(role)
	grantOnly := paramStr(r.Params, "grant_only") != ""
	if !grantOnly {
		roleIdent, err = (provision.Postgres{}).EnsureRole(ctx, conn, role, pass)
		if err != nil {
			return nil, err
		}
	}
	if db != "" {
		level := provision.GrantLevel(paramStr(r.Params, "level"))
		if level == "" {
			level = provision.GrantRead
		}
		if err := (provision.Postgres{}).Grant(ctx, conn, role, db, level); err != nil {
			return nil, err
		}
	}
	attrs := Attrs{
		"host":     sharedHost(t.Instance),
		"port":     "5432",
		"user":     roleIdent,
		"role":     roleIdent,
		"password": pass,
	}
	if db != "" {
		attrs["database"] = pgIdent(db)
	}
	return attrs, nil
}

// Drop removes the resource's database and/or role, guarded so it is idempotent
// and never bounces the shared container. For a database it terminates the
// tenant's own sessions before DROP DATABASE (never other tenants'), then drops
// the owning role; for a role/user it drops just the role.
func (p Postgres) Drop(ctx context.Context, t Target, r Resource) error {
	identity := r.Name
	if identity == "" {
		identity = r.Owner
	}
	ident := pgIdent(identity)
	conn, closeConn, err := p.connect(ctx, p.dsn(t))
	if err != nil {
		return fmt.Errorf("connect to shared %s on %s:%d: %w", t.Instance, t.Host, t.Port, err)
	}
	defer func() { _ = closeConn() }()

	if r.Kind == "database" {
		// Terminate only THIS database's backends (never other tenants').
		if err := conn.Exec(ctx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`,
			ident); err != nil {
			return fmt.Errorf("terminate sessions on %q: %w", ident, err)
		}
		if err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(ident)); err != nil {
			return fmt.Errorf("drop database %q: %w", ident, err)
		}
	}
	// The role is safe to drop once its owned database is gone (IF EXISTS →
	// idempotent). A role that still owns objects will error, surfaced to the caller.
	if err := conn.Exec(ctx, `DROP ROLE IF EXISTS `+quoteIdent(ident)); err != nil {
		return fmt.Errorf("drop role %q: %w", ident, err)
	}
	return nil
}

// Preflight verifies the admin endpoint is reachable (a connect + close). Absence
// of a live engine degrades this provisioner's verbs, never blocks `up`.
func (p Postgres) Preflight(ctx context.Context, t Target) error {
	_, closeConn, err := p.connect(ctx, p.dsn(t))
	if err != nil {
		return err
	}
	return closeConn()
}

// sharedHost is the stable DNS alias a consumer container reaches the instance by.
func sharedHost(instance string) string { return "shared-" + instance }

// pgIdent maps a project/resource name to a safe Postgres identifier (hyphens →
// underscores), matching provision.pgIdent so identities are stable across the
// implicit and explicit provisioning paths.
func pgIdent(name string) string { return strings.ReplaceAll(name, "-", "_") }

// quoteIdent double-quotes a Postgres identifier, doubling embedded quotes.
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
