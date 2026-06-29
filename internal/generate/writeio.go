package generate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeIfChanged writes data to path only when the on-disk content differs,
// returning whether a write occurred. The write is atomic: data lands in a temp
// file in the SAME directory (so os.Rename is a same-filesystem move, never a
// cross-device copy) and is renamed over the target — a simulated crash leaves
// either the old file or the new file, never a half-written one (spec 02
// acceptance #6).
func writeIfChanged(path string, data []byte) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && bytesEqual(existing, data) {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return false, fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return false, fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return true, nil
}

// tempPrefix is the prefix of the in-progress temp files writeIfChanged creates.
const tempPrefix = ".devstack-tmp-"

// sweepTemp removes stale temp files left in dir by a previously killed run
// (deferred cleanup does not run on SIGKILL / power loss). Best-effort.
func sweepTemp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), tempPrefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// bytesEqual is a tiny dependency-free []byte compare.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
