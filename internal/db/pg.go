// Package db is the data-lifecycle seam for the shared engines (spec 15): it
// captures and replays a single project's tenant namespace on the warm shared
// Postgres via the engine's own external client tooling (pg_dump / pg_restore /
// psql). The tools are shelled behind the Dumper interface — exactly the
// docker/git wrapping discipline — so the release binary stays a pure-Go,
// CGO-free static binary and every risky external tool gets an internal/ seam
// plus a mock. Only the pg dumper exists in this milestone; redis/minio dumpers
// slot in behind the same interface (Full scope).
//
// The dumper never touches the shared container via the SDK: Compose owns
// containers, the tools run as HOST binaries against a ledger-allocated,
// 127.0.0.1-only host-port overlay (the same reachability path the provision
// phase uses). The password is passed via PGPASSWORD in the process env, never
// on the argv (so it does not leak into `ps`) — the same secret-handling posture
// as the rest of devstack (§7.5).
package db

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Runner shells an external command. Its shape matches docker.Runner so the real
// docker.ExecRunner satisfies it directly, and tests inject a recording fake.
type Runner interface {
	Run(ctx context.Context, env []string, dir, name string, args ...string) error
	Output(ctx context.Context, env []string, dir, name string, args ...string) ([]byte, error)
}

// ConnInfo is a host-reachable admin endpoint for one tenant database. Host/Port
// come from the ledger-allocated 127.0.0.1 overlay; User/Password are the shared
// instance's admin credentials; Database is the per-project tenant db.
type ConnInfo struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// Dumper captures and replays a single database behind an external client tool.
// Snapshot writes a content-addressable dump to outPath; Restore replays inPath
// into the (recreated/clean) database; IsEmpty reports whether the tenant has any
// user tables (the restore-over-non-empty guard); Preflight checks the tool is
// present and version-compatible.
type Dumper interface {
	Preflight(ctx context.Context) error
	Snapshot(ctx context.Context, conn ConnInfo, outPath string) error
	Restore(ctx context.Context, conn ConnInfo, inPath string) error
	IsEmpty(ctx context.Context, conn ConnInfo) (bool, error)
}

// ErrToolMissing is returned by Preflight when the required client binary is not
// on PATH. It carries a one-line remediation (ARCHITECTURE §7.6).
type ErrToolMissing struct {
	Tool        string
	Remediation string
}

func (e *ErrToolMissing) Error() string {
	return fmt.Sprintf("%s not found: %s", e.Tool, e.Remediation)
}

// PgDumper shells pg_dump / pg_restore / psql. The client major must be ≥ the
// server major to restore reliably (spec 15); this milestone stores no version
// gate but the seam is here. Runner is injectable (nil → the real exec runner is
// supplied by the caller); LookPath is injectable so Preflight is unit-testable.
type PgDumper struct {
	Runner   Runner
	LookPath func(string) (string, error) // nil → exec.LookPath
}

// pgClientTools are the external binaries the pg dumper needs on PATH.
var pgClientTools = []string{"pg_dump", "pg_restore", "psql"}

// Preflight verifies the PostgreSQL client tools are installed. Absence degrades
// the db verbs only (never blocks up), consistent with the mkcert/cloudflared
// external-binary posture (DECISIONS D11/D12).
func (p PgDumper) Preflight(_ context.Context) error {
	look := p.LookPath
	if look == nil {
		look = exec.LookPath
	}
	for _, tool := range pgClientTools {
		if _, err := look(tool); err != nil {
			return &ErrToolMissing{
				Tool:        tool,
				Remediation: "install the PostgreSQL client tools (e.g. `apt install postgresql-client`, `brew install libpq`, or `dnf install postgresql`) so `" + tool + "` is on PATH",
			}
		}
	}
	return nil
}

// connFlags builds the shared -h/-p/-U/-d connection flags. The password is NOT
// here — it rides PGPASSWORD in the env (pgEnv).
func connFlags(c ConnInfo) []string {
	return []string{"-h", c.Host, "-p", strconv.Itoa(c.Port), "-U", c.User, "-d", c.Database}
}

// pgEnv passes the password out-of-band so it never lands on the argv.
func pgEnv(c ConnInfo) []string { return []string{"PGPASSWORD=" + c.Password} }

// Snapshot dumps the tenant database to outPath in the custom (compressed,
// selectively-restorable) format, owner-stripped so it replays into a
// freshly-recreated role. pg_dump opens a REPEATABLE READ snapshot, so it is
// consistent against a busy tenant WITHOUT stopping the shared server (spec 15).
func (p PgDumper) Snapshot(ctx context.Context, conn ConnInfo, outPath string) error {
	args := append(connFlags(conn), "--format=custom", "--no-owner", "--no-privileges", "--file", outPath)
	if err := p.Runner.Run(ctx, pgEnv(conn), "", "pg_dump", args...); err != nil {
		return fmt.Errorf("pg_dump %s: %w", conn.Database, err)
	}
	return nil
}

// Restore replays inPath into the tenant database, dropping conflicting objects
// first (--clean --if-exists) and ignoring dump ownership (--no-owner). The
// caller must have terminated live backends + recreated a clean database (the
// tenant scope) before calling; the drop/recreate SQL is provisioning's guarded
// pgx path (spec 15). --exit-on-error so a partial restore fails loudly.
func (p PgDumper) Restore(ctx context.Context, conn ConnInfo, inPath string) error {
	args := append(connFlags(conn), "--clean", "--if-exists", "--no-owner", "--no-privileges", "--exit-on-error", inPath)
	if err := p.Runner.Run(ctx, pgEnv(conn), "", "pg_restore", args...); err != nil {
		return fmt.Errorf("pg_restore %s: %w", conn.Database, err)
	}
	return nil
}

// emptyCountSQL counts user tables (excluding the system schemas) so a restore
// can refuse to clobber a tenant that already has data unless --force.
const emptyCountSQL = `SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')`

// IsEmpty reports whether the tenant database has no user tables.
func (p PgDumper) IsEmpty(ctx context.Context, conn ConnInfo) (bool, error) {
	args := append(connFlags(conn), "-tAX", "-c", emptyCountSQL)
	out, err := p.Runner.Output(ctx, pgEnv(conn), "", "psql", args...)
	if err != nil {
		return false, fmt.Errorf("psql count tables in %s: %w", conn.Database, err)
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		return false, fmt.Errorf("parse table count %q: %w", strings.TrimSpace(string(out)), perr)
	}
	return n == 0, nil
}

// IsToolMissing reports whether err is (or wraps) an ErrToolMissing.
func IsToolMissing(err error) bool {
	var e *ErrToolMissing
	return errors.As(err, &e)
}
