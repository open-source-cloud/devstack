package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner executes external commands. It is injectable so compose command
// construction is unit-testable without a real `docker` binary.
type Runner interface {
	// Run streams stdout/stderr to the user (image-pull/up progress).
	Run(ctx context.Context, env []string, dir, name string, args ...string) error
	// Output captures stdout (for `compose ps --format json` and friends).
	Output(ctx context.Context, env []string, dir, name string, args ...string) ([]byte, error)
}

// ExecRunner runs commands via os/exec, inheriting the process env plus extra
// env. Secrets are passed through env here and never written to a file (§7.5).
// A failure is wrapped with the command + exit code + captured stderr so the
// error is self-debuggable (ARCHITECTURE §7.6).
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, env []string, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return &CmdError{Cmd: name + " " + strings.Join(args, " "), Err: err, Stderr: strings.TrimSpace(stderr.String())}
	}
	return nil
}

func (ExecRunner) Output(ctx context.Context, env []string, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &CmdError{Cmd: name + " " + strings.Join(args, " "), Err: err, Stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.Bytes(), nil
}

// CmdError carries the failed command, its error, and captured stderr.
type CmdError struct {
	Cmd    string
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("`%s` failed: %v\n%s", e.Cmd, e.Err, e.Stderr)
	}
	return fmt.Sprintf("`%s` failed: %v", e.Cmd, e.Err)
}

func (e *CmdError) Unwrap() error { return e.Err }

// Compose drives the `docker compose` CLI for a single stack with an explicit
// project name and compose file (DECISIONS D5). Lifecycle verbs run here;
// container enumeration stays on the read-only SDK Client.
type Compose struct {
	Project string   // -p <project>
	File    string   // -f <compose file>
	Dir     string   // working dir (build contexts resolve relative to it)
	Env     []string // extra env (resolved secrets), appended to os.Environ
	Runner  Runner
}

// NewCompose builds a Compose driver using the real exec runner.
func NewCompose(project, file, dir string) *Compose {
	return &Compose{Project: project, File: file, Dir: dir, Runner: ExecRunner{}}
}

func (c *Compose) base() []string {
	return []string{"compose", "-p", c.Project, "-f", c.File}
}

// Up brings the stack (or the named subset of services) up detached. With no
// services it brings the whole stack up. It does NOT pass --remove-orphans: the
// shared stack is brought up service-by-service and stateful services must never
// be disturbed implicitly (spec 03).
func (c *Compose) Up(ctx context.Context, services ...string) error {
	args := append(c.base(), "up", "-d")
	args = append(args, services...)
	return c.Runner.Run(ctx, c.Env, c.Dir, "docker", args...)
}

// Down stops and removes the stack's containers (and its default network).
// Named external networks are NOT removed by compose — devstack owns those.
// volumes=true also removes named volumes (destructive — caller confirms).
func (c *Compose) Down(ctx context.Context, volumes bool) error {
	args := append(c.base(), "down")
	if volumes {
		args = append(args, "--volumes")
	}
	return c.Runner.Run(ctx, c.Env, c.Dir, "docker", args...)
}

// Stop pauses the stack (or named services) without removing containers — used
// by `shared gc`/autostop where the data volumes must survive.
func (c *Compose) Stop(ctx context.Context, services ...string) error {
	args := append(c.base(), "stop")
	args = append(args, services...)
	return c.Runner.Run(ctx, c.Env, c.Dir, "docker", args...)
}

// Build rebuilds the named services (with --no-cache for the selective-rebuild
// contexts the generate ledger flagged). With no services, builds all.
func (c *Compose) Build(ctx context.Context, noCache bool, services ...string) error {
	args := append(c.base(), "build")
	if noCache {
		args = append(args, "--no-cache")
	}
	args = append(args, services...)
	return c.Runner.Run(ctx, c.Env, c.Dir, "docker", args...)
}
