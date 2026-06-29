// Package git (gitx) is the hardened wrapper around the system `git` binary
// (DECISIONS D9, spec 06). devstack shells out to git — NOT go-git — so it
// inherits the developer's SSH agent, ~/.ssh/config (IdentityFile, Host aliases,
// ProxyJump/Include), known_hosts, and OS credential helpers for free: "your
// existing `git push` setup just works" is the whole value proposition.
//
// Every invocation runs with a hardened environment so a hidden credential
// prompt never hangs a parallel batch — it fails fast per-repo instead.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// MinVersion is the git floor (spec 06). 2.30 is conservative (porcelain=v2
// needs only ~2.13) but matches the documented contributor/runtime gate.
var MinVersion = Version{2, 30}

// Git is a configured handle to the system git binary.
type Git struct {
	bin string
	env []string
	// configArgs are `-c key=val` pairs prepended to every invocation (used by
	// WithToken to disable inherited credential persistence).
	configArgs []string
}

// Version is a major.minor pair for the git floor check.
type Version struct{ Major, Minor int }

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

func (v Version) atLeast(min Version) bool {
	if v.Major != min.Major {
		return v.Major > min.Major
	}
	return v.Minor >= min.Minor
}

// New locates git, verifies it is >= MinVersion, and returns a handle with the
// hardened environment baked in. Done once at startup with an actionable error.
func New() (*Git, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git not found on PATH (devstack needs git >= %s): %w", MinVersion, err)
	}
	g := &Git{bin: bin, env: hardenedEnv(os.Environ())}
	v, err := g.version(context.Background())
	if err != nil {
		return nil, err
	}
	if !v.atLeast(MinVersion) {
		return nil, fmt.Errorf("git %s is too old; devstack needs >= %s (Ubuntu 20.04's 2.25 is below the floor)", v, MinVersion)
	}
	return g, nil
}

// hardenedEnv augments the process env so git never blocks on an interactive
// prompt and always emits stable, parseable output (spec 06). GIT_TERMINAL_PROMPT
// and GCM_INTERACTIVE cover git's own prompts and Git Credential Manager (the
// HTTPS path); for the SSH path, BatchMode is added when NO terminal is attached
// so an unknown host key or a passphrase-protected key with no agent fails fast
// per-repo instead of hanging the parallel batch — while still honoring
// ~/.ssh/config (IdentityFile, Host aliases, ProxyJump) and known_hosts. When a
// TTY IS attached, interactive passphrase/host-key entry is left intact, and a
// pre-existing GIT_SSH_COMMAND is always respected.
func hardenedEnv(base []string) []string {
	env := append(base,
		"GIT_TERMINAL_PROMPT=0", // never prompt on the terminal
		"GCM_INTERACTIVE=never", // Git Credential Manager: never pop UI
		"LC_ALL=C",              // stable, parseable, locale-independent output
	)
	if !hasEnv(base, "GIT_SSH_COMMAND") && !term.IsTerminal(int(os.Stderr.Fd())) {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
	}
	return env
}

// hasEnv reports whether env already defines key.
func hasEnv(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// run executes git in dir (empty = inherit CWD), capturing stdout. A failure is
// wrapped with the args + captured stderr so it is self-debuggable (§7.6).
func (g *Git) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := args
	if len(g.configArgs) > 0 {
		full = append(append([]string{}, g.configArgs...), args...)
	}
	cmd := exec.CommandContext(ctx, g.bin, full...)
	cmd.Dir = dir
	cmd.Env = g.env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &Error{Args: args, Dir: dir, Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return []byte(stdout.String()), nil
}

// version parses `git --version`.
func (g *Git) version(ctx context.Context) (Version, error) {
	out, err := g.run(ctx, "", "--version")
	if err != nil {
		return Version{}, err
	}
	for field := range strings.FieldsSeq(string(out)) {
		parts := strings.SplitN(field, ".", 3)
		if len(parts) < 2 {
			continue
		}
		major, err1 := strconv.Atoi(parts[0])
		minor, err2 := strconv.Atoi(parts[1])
		if err1 == nil && err2 == nil {
			return Version{major, minor}, nil
		}
	}
	return Version{}, fmt.Errorf("could not parse git version from %q", strings.TrimSpace(string(out)))
}

// Error wraps a failed git invocation with its args, dir, and captured stderr.
type Error struct {
	Args   []string
	Dir    string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	cmd := "git " + strings.Join(e.Args, " ")
	if e.Stderr != "" {
		return fmt.Sprintf("`%s` failed: %v\n%s", cmd, e.Err, e.Stderr)
	}
	return fmt.Sprintf("`%s` failed: %v", cmd, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// CloneOptions tunes a clone (all default-off).
type CloneOptions struct {
	Submodules bool // --recurse-submodules
	Blobless   bool // --filter=blob:none (preferred over --depth for large repos)
}

// Clone clones url into dir. Idempotent at the caller layer (check IsRepo first).
func (g *Git) Clone(ctx context.Context, url, dir string, opts CloneOptions) error {
	args := []string{"clone"}
	if opts.Submodules {
		args = append(args, "--recurse-submodules")
	}
	if opts.Blobless {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, url, dir)
	_, err := g.run(ctx, "", args...)
	return err
}

// Fetch updates remotes (with prune) without touching the working tree.
func (g *Git) Fetch(ctx context.Context, dir string) error {
	_, err := g.run(ctx, dir, "fetch", "--prune", "--no-tags")
	return err
}

// Pull does a fast-forward-only pull (never creates a merge commit implicitly).
func (g *Git) Pull(ctx context.Context, dir string) error {
	_, err := g.run(ctx, dir, "pull", "--ff-only")
	return err
}

// RemoteURL returns origin's URL (for idempotent clone validation).
func (g *Git) RemoteURL(ctx context.Context, dir string) (string, error) {
	out, err := g.run(ctx, dir, "--no-optional-locks", "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Run executes an arbitrary git subcommand in dir, discarding stdout and
// returning a positioned error (with stderr) on failure. Used by `ws git` to
// fan a command across repos.
func (g *Git) Run(ctx context.Context, dir string, args ...string) error {
	_, err := g.run(ctx, dir, args...)
	return err
}

// IsRepo reports whether dir is the top of a git working tree.
func (g *Git) IsRepo(ctx context.Context, dir string) bool {
	out, err := g.run(ctx, dir, "--no-optional-locks", "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
