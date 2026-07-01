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

// InteractiveRunner runs a command with ALL THREE std streams inherited and NO
// capture, for an interactive `compose exec` (a login shell into a container).
// Unlike ExecRunner it wires Stdin=os.Stdin (so the shell receives input) and
// does not tee stderr into a buffer (the child owns the terminal). The child's
// exit code is propagated verbatim via the returned *exec.ExitError, so callers
// can mirror it as the process exit code (spec 26 `shell`).
type InteractiveRunner struct{}

func (InteractiveRunner) Run(ctx context.Context, env []string, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Return the raw error (an *exec.ExitError carries the child exit code) so the
	// caller can propagate it — do NOT wrap it in CmdError (no captured stderr).
	return cmd.Run()
}

// Output is not meaningful for an interactive runner (the child owns stdout).
func (InteractiveRunner) Output(context.Context, []string, string, string, ...string) ([]byte, error) {
	return nil, fmt.Errorf("InteractiveRunner does not support captured Output")
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
	Project   string   // -p <project>
	File      string   // -f <compose file>
	Overrides []string // additional -f overlays, applied in order after File (up-time only)
	Dir       string   // working dir (build contexts resolve relative to it)
	Env       []string // extra env (resolved secrets), appended to os.Environ
	// ContextEnv pins the compose CLI to a specific Docker backend/endpoint
	// (DOCKER_HOST / DOCKER_CONTEXT), from Backend.ComposeEnv() (spec 21). Empty for
	// the local backend. Kept separate from Env so a remote endpoint and resolved
	// secrets compose cleanly; both are appended to the child env, never written to
	// a file. ContextEnv is applied AFTER Env so the endpoint selection always wins.
	ContextEnv []string
	Runner     Runner
}

// env returns the extra environment the Runner appends to os.Environ for every
// verb: the resolved-secret Env followed by the backend-selecting ContextEnv.
func (c *Compose) env() []string {
	if len(c.ContextEnv) == 0 {
		return c.Env
	}
	out := make([]string, 0, len(c.Env)+len(c.ContextEnv))
	out = append(out, c.Env...)
	out = append(out, c.ContextEnv...)
	return out
}

// NewCompose builds a Compose driver using the real exec runner.
func NewCompose(project, file, dir string) *Compose {
	return &Compose{Project: project, File: file, Dir: dir, Runner: ExecRunner{}}
}

func (c *Compose) base() []string {
	// File is optional for label-driven verbs (down/stop discover the stack from
	// the project name's container labels); up/build require it.
	if c.File == "" {
		return []string{"compose", "-p", c.Project}
	}
	args := []string{"compose", "-p", c.Project, "-f", c.File}
	// Overlays (e.g. the up-time provision port mapping) are applied after the base
	// file so their values win; later files override earlier ones (compose merge).
	for _, ov := range c.Overrides {
		args = append(args, "-f", ov)
	}
	return args
}

// Up brings the stack (or the named subset of services) up detached. With no
// services it brings the whole stack up. It does NOT pass --remove-orphans: the
// shared stack is brought up service-by-service and stateful services must never
// be disturbed implicitly (spec 03).
func (c *Compose) Up(ctx context.Context, services ...string) error {
	args := append(c.base(), "up", "-d")
	args = append(args, services...)
	return c.Runner.Run(ctx, c.env(), c.Dir, "docker", args...)
}

// Down stops and removes the stack's containers (and its default network).
// Named external networks are NOT removed by compose — devstack owns those.
// volumes=true also removes named volumes (destructive — caller confirms).
func (c *Compose) Down(ctx context.Context, volumes bool) error {
	args := append(c.base(), "down")
	if volumes {
		args = append(args, "--volumes")
	}
	return c.Runner.Run(ctx, c.env(), c.Dir, "docker", args...)
}

// Stop pauses the stack (or named services) without removing containers — used
// by `shared gc`/autostop where the data volumes must survive.
func (c *Compose) Stop(ctx context.Context, services ...string) error {
	args := append(c.base(), "stop")
	args = append(args, services...)
	return c.Runner.Run(ctx, c.env(), c.Dir, "docker", args...)
}

// Exec runs `docker compose exec` for a service, letting compose resolve the
// container from the project + service (no manual SDK enumeration). When
// interactive it requests a real TTY (-it) and the caller MUST supply an
// InteractiveRunner (stdin-wired, non-capturing) so the shell doesn't hang;
// otherwise it passes -T (no TTY allocation) so a non-interactive caller never
// blocks on a missing terminal. cmd is the command + args to run in the container
// (empty → the image default). Returns the child's error verbatim (an
// *exec.ExitError carries its exit code) so `shell` can mirror it (spec 26).
func (c *Compose) Exec(ctx context.Context, service string, interactive bool, cmd ...string) error {
	args := append(c.base(), "exec")
	if interactive {
		args = append(args, "-it")
	} else {
		args = append(args, "-T")
	}
	args = append(args, service)
	args = append(args, cmd...)
	return c.Runner.Run(ctx, c.env(), c.Dir, "docker", args...)
}

// Build rebuilds the named services (with --no-cache for the selective-rebuild
// contexts the generate ledger flagged). With no services, builds all.
func (c *Compose) Build(ctx context.Context, noCache bool, services ...string) error {
	args := append(c.base(), "build")
	if noCache {
		args = append(args, "--no-cache")
	}
	args = append(args, services...)
	return c.Runner.Run(ctx, c.env(), c.Dir, "docker", args...)
}
