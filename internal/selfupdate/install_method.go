package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Method classifies how the running binary was installed — which decides whether
// `self update` may replace it or must defer to a package manager.
type Method string

const (
	SelfManaged Method = "self-managed" // a plain binary we may replace
	Homebrew    Method = "homebrew"
	Dpkg        Method = "dpkg"
	Rpm         Method = "rpm"
	NotWritable Method = "not-writable"
)

// Install describes the detected install method plus the exact remediation to
// print when devstack must not self-replace.
type Install struct {
	Method Method
	Path   string // resolved binary path
	Hint   string // user-facing upgrade command / reason
}

// CanSelfReplace reports whether `self update` may overwrite the binary.
func (i Install) CanSelfReplace() bool { return i.Method == SelfManaged }

// DetectInstall resolves the running binary (through symlinks, so an alias does
// not mask the real install) and classifies it. Detection prefers the package
// database over path heuristics and defaults to REFUSE when ambiguous — a missed
// managed-install detection that proceeds to overwrite a Cellar/dpkg file bricks
// the package manager (spec 14).
func DetectInstall(ctx context.Context) (Install, error) {
	exe, err := os.Executable()
	if err != nil {
		return Install{}, fmt.Errorf("resolve own path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// Package-database queries first (most reliable).
	if pkgOwns(ctx, "dpkg", "-S", exe) {
		return Install{Method: Dpkg, Path: exe, Hint: "installed via dpkg — upgrade with `sudo apt upgrade " + Binary + "`"}, nil
	}
	if pkgOwns(ctx, "rpm", "-qf", exe) {
		return Install{Method: Rpm, Path: exe, Hint: "installed via rpm — upgrade with `sudo dnf upgrade " + Binary + "`"}, nil
	}
	if isHomebrew(ctx, exe) {
		return Install{Method: Homebrew, Path: exe, Hint: "installed via Homebrew — upgrade with `brew upgrade " + Binary + "`"}, nil
	}
	if !writable(exe) {
		return Install{Method: NotWritable, Path: exe, Hint: "the binary at " + exe + " is not writable; reinstall it or use your package manager"}, nil
	}
	return Install{Method: SelfManaged, Path: exe}, nil
}

// pkgOwns reports whether a package-manager query (dpkg -S / rpm -qf) claims the
// path. A missing tool or a non-zero exit means "not owned".
func pkgOwns(ctx context.Context, tool string, args ...string) bool {
	if _, err := exec.LookPath(tool); err != nil {
		return false
	}
	return exec.CommandContext(ctx, tool, args...).Run() == nil
}

// isHomebrew matches a Cellar/opt path or the `brew --prefix` root.
func isHomebrew(ctx context.Context, exe string) bool {
	if strings.Contains(exe, "/Cellar/") || strings.Contains(exe, "/homebrew/") {
		return true
	}
	if _, err := exec.LookPath("brew"); err != nil {
		return false
	}
	out, err := exec.CommandContext(ctx, "brew", "--prefix").Output()
	if err != nil {
		return false
	}
	prefix := strings.TrimSpace(string(out))
	return prefix != "" && strings.HasPrefix(exe, prefix+string(os.PathSeparator))
}

// writable reports whether the update can replace path. The update writes a temp
// file in path's DIRECTORY and renames it over the target, so only directory
// writability matters — deliberately NOT os.OpenFile(path, O_WRONLY), which fails
// with ETXTBSY on the currently-running binary on Linux (a false negative that
// would block every self-update).
func writable(path string) bool {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".devstack-wtest-")
	if err != nil {
		return false
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	return true
}

// defaultToken reads a GitHub token from the environment.
func defaultToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("GH_TOKEN")
}
