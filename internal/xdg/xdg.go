// Package xdg resolves the tool's machine-global directories (config, data,
// state, cache, runtime, bin) under XDG base dirs, and owns the WSL2 runtime
// rules that the rest of the tool depends on:
//
//   - state/cache MUST live on the Linux filesystem (never a 9p/drvfs mount);
//   - refuse to operate from a Windows-mounted `/mnt/*` working dir under WSL2;
//   - detect 9p/networked state dirs where SQLite + flock locking is unreliable
//     (doctor warns; see spec 08).
package xdg

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	axdg "github.com/adrg/xdg"
)

// AppName is the subdirectory under each XDG base dir. Kept stable even when the
// binary is invoked under an alias (rq/uranus) so all aliases share one ledger.
const AppName = "devstack"

// base returns the live value of env when set to an ABSOLUTE path (so a process
// or test that exports XDG_* is honored without an adrg reload), else the
// platform default adrg resolved at init. Per the XDG spec, relative env values
// must be ignored.
func base(env, adrgDefault string) string {
	if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
		return v
	}
	return adrgDefault
}

// ConfigHome is $XDG_CONFIG_HOME/devstack (alias registry, global settings).
func ConfigHome() string { return filepath.Join(base("XDG_CONFIG_HOME", axdg.ConfigHome), AppName) }

// DataHome is $XDG_DATA_HOME/devstack (SQLite ledger, durable artifacts).
func DataHome() string { return filepath.Join(base("XDG_DATA_HOME", axdg.DataHome), AppName) }

// StateHome is $XDG_STATE_HOME/devstack (logs, lock fallback).
func StateHome() string { return filepath.Join(base("XDG_STATE_HOME", axdg.StateHome), AppName) }

// CacheHome is $XDG_CACHE_HOME/devstack (template cache; needs GC/TTL).
func CacheHome() string { return filepath.Join(base("XDG_CACHE_HOME", axdg.CacheHome), AppName) }

// RuntimeDir is $XDG_RUNTIME_DIR/devstack when available, else falls back to
// StateHome (RuntimeDir is often unset on macOS and minimal WSL2 distros). The
// cross-process lockfile lives here. Reads the env live so a process that sets
// XDG_RUNTIME_DIR (or a test) is honored without an xdg reload.
func RuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, AppName)
	}
	return StateHome()
}

// BinHome is $XDG_BIN_HOME (or ~/.local/bin) where alias symlinks are installed.
func BinHome() string {
	if v := os.Getenv("XDG_BIN_HOME"); v != "" && filepath.IsAbs(v) {
		return v
	}
	if axdg.Home != "" {
		return filepath.Join(axdg.Home, ".local", "bin")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin")
}

// EnsureDir creates dir (0o700) if missing and returns it, for ergonomic chaining.
func EnsureDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create dir %s: %w", dir, err)
	}
	return dir, nil
}

// IsWSL2 reports whether we are running inside a WSL2 distribution. This single
// detector feeds every WSL2 capability branch; doctor must exercise the real
// branch, not docs.
func IsWSL2() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if v := strings.ToLower(os.Getenv("WSL_DISTRO_NAME")); v != "" {
		return true
	}
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	s := strings.ToLower(string(b))
	return strings.Contains(s, "microsoft") || strings.Contains(s, "wsl")
}

// RefuseWindowsMount returns an error if, under WSL2, the working dir lives on a
// Windows mount (`/mnt/*`). Docker/file semantics there are broken enough that
// we refuse outright rather than fail mysteriously later.
func RefuseWindowsMount(cwd string) error {
	if !IsWSL2() {
		return nil
	}
	clean := filepath.Clean(cwd)
	if clean == "/mnt" || strings.HasPrefix(clean, "/mnt/") {
		return fmt.Errorf(
			"refusing to operate from a Windows-mounted path under WSL2: %s\n"+
				"move the workspace onto the Linux filesystem (e.g. ~/code/...) for correct Docker and file behavior",
			cwd)
	}
	return nil
}

// FilesystemType returns the filesystem type backing path by consulting
// /proc/mounts and selecting the longest matching mount point. Empty string when
// it cannot be determined (non-Linux, /proc unavailable). Used by doctor to warn
// on 9p/v9fs/drvfs state dirs where SQLite/flock locking is unreliable.
func FilesystemType(path string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return ""
	}
	var bestMount, bestType string
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mountPoint, fsType := fields[1], fields[2]
		if abs == mountPoint || strings.HasPrefix(abs, strings.TrimRight(mountPoint, "/")+"/") || mountPoint == "/" {
			if len(mountPoint) >= len(bestMount) {
				bestMount, bestType = mountPoint, fsType
			}
		}
	}
	return bestType
}

// IsUnreliableLockFS reports whether fsType is a network/9p filesystem on which
// SQLite + flock advisory locking is known to be unreliable.
func IsUnreliableLockFS(fsType string) bool {
	switch fsType {
	case "9p", "v9fs", "drvfs", "nfs", "nfs4", "cifs", "smb3", "smbfs", "fuse.sshfs":
		return true
	default:
		return false
	}
}
