package db

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// This file is the Redis snapshot/restore Dumper (spec 15). It mirrors the pg
// dumper discipline: the external `redis-cli` client is shelled behind the
// injectable Runner (nil in tests → a recording fake), the password rides
// REDISCLI_AUTH in the process env (never on the argv, so it does not leak into
// `ps`), and Preflight degrades the db verbs only (never blocks `up`) when the
// tool is absent, with a one-line remediation.
//
// BEST-EFFORT (documented on purpose): a host client cannot cheaply carve one
// tenant's logical DB out of a live shared Redis. Snapshot therefore captures a
// point-in-time RDB of the WHOLE instance via `redis-cli --rdb` (redis-cli opens
// a replication SYNC and writes the transferred RDB locally — it does NOT stop
// the shared server, so other tenants are undisturbed). Restore streams that
// artifact back through redis-cli's mass-insert pipe. A byte-faithful whole-RDB
// reload into a LIVE shared instance is not possible without a controlled restart
// + dump.rdb swap, which devstack refuses to do to a shared service (the
// never-recreate-a-stateful-shared-service guard). Callers who need per-tenant
// fidelity should run a dedicated Redis instance or a key-prefix workflow. This is
// exactly the engine spec 15 flags as "best-effort" for the shared model.

// RedisDumper shells `redis-cli`. Runner is injectable (tests inject a recording
// fake); LookPath is injectable so Preflight is unit-testable without the binary.
type RedisDumper struct {
	Runner   Runner
	LookPath func(string) (string, error) // nil → exec.LookPath
}

// Ensure RedisDumper satisfies the Dumper seam at compile time.
var _ Dumper = RedisDumper{}

// redisClientTool is the external binary the redis dumper needs on PATH.
const redisClientTool = "redis-cli"

// Preflight verifies redis-cli is installed. Absence degrades the db verbs only
// (never blocks up), consistent with the mkcert/cloudflared external-binary
// posture (DECISIONS D11/D12).
func (r RedisDumper) Preflight(_ context.Context) error {
	look := r.LookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look(redisClientTool); err != nil {
		return &ErrToolMissing{
			Tool:        redisClientTool,
			Remediation: "install the Redis client tools (e.g. `apt install redis-tools`, `brew install redis`, or `dnf install redis`) so `redis-cli` is on PATH",
		}
	}
	return nil
}

// redisFlags builds the shared -h/-p connection flags. When ConnInfo.Database is
// a logical index (e.g. "0"), it is passed as `-n <index>`. The password is NOT
// here — it rides REDISCLI_AUTH in the env (redisEnv).
func redisFlags(c ConnInfo) []string {
	f := []string{"-h", c.Host, "-p", strconv.Itoa(c.Port)}
	if c.Database != "" {
		f = append(f, "-n", c.Database)
	}
	return f
}

// redisEnv passes the AUTH password out-of-band so it never lands on the argv.
// An empty password (the default auth-less shared Redis) yields no env entry —
// sending AUTH to an auth-less server is itself an error.
func redisEnv(c ConnInfo) []string {
	if c.Password == "" {
		return nil
	}
	return []string{"REDISCLI_AUTH=" + c.Password}
}

// Snapshot captures a whole-instance RDB to outPath via `redis-cli --rdb`. The
// SYNC does not stop the shared server, so other tenants are undisturbed (spec
// 15). Whole-instance capture is the documented best-effort tradeoff.
func (r RedisDumper) Snapshot(ctx context.Context, conn ConnInfo, outPath string) error {
	args := append(redisFlags(conn), "--rdb", outPath)
	if err := r.Runner.Run(ctx, redisEnv(conn), "", redisClientTool, args...); err != nil {
		return fmt.Errorf("redis-cli --rdb: %w", err)
	}
	return nil
}

// Restore streams the captured dump back through redis-cli's mass-insert pipe.
// Because the Runner has no stdin channel, the redirection is expressed through
// `sh -c` (the standard `redis-cli --pipe < dump` incantation). This is the
// best-effort restore path (see the package note): it never restarts or bounces
// the shared container. The password still rides REDISCLI_AUTH in the env.
func (r RedisDumper) Restore(ctx context.Context, conn ConnInfo, inPath string) error {
	pipe := redisClientTool + " " + shellJoin(redisFlags(conn)) + " --pipe < " + shellQuote(inPath)
	if err := r.Runner.Run(ctx, redisEnv(conn), "", "sh", "-c", pipe); err != nil {
		return fmt.Errorf("redis-cli --pipe restore: %w", err)
	}
	return nil
}

// IsEmpty reports whether the target keyspace has no keys, via `DBSIZE`. For a
// logical index it counts that index; otherwise it counts db 0. This backs the
// restore-over-non-empty guard.
func (r RedisDumper) IsEmpty(ctx context.Context, conn ConnInfo) (bool, error) {
	args := append(redisFlags(conn), "DBSIZE")
	out, err := r.Runner.Output(ctx, redisEnv(conn), "", redisClientTool, args...)
	if err != nil {
		return false, fmt.Errorf("redis-cli DBSIZE: %w", err)
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		return false, fmt.Errorf("parse DBSIZE %q: %w", strings.TrimSpace(string(out)), perr)
	}
	return n == 0, nil
}

// shellQuote single-quotes a path for a POSIX `sh -c` string (embedded single
// quotes are escaped the standard '"'"' way).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// shellJoin space-joins already-safe redis-cli flags (host/port/index — no shell
// metacharacters) for the `sh -c` string.
func shellJoin(args []string) string {
	return strings.Join(args, " ")
}
