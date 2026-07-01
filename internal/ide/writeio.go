package ide

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// marshalJSON renders v as stable, 2-space-indented JSON with a trailing newline.
// HTML escaping is disabled so schema URLs survive verbatim; struct field order is
// declaration order and map keys are sorted by encoding/json — both deterministic
// (spec 17 acceptance #4).
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const tempPrefix = ".devstack-ide-tmp-"

// writeIfChanged writes data to path only when the on-disk content differs,
// returning whether a write occurred. The write is atomic: a temp file in the same
// directory is fsync'd, chmod'd, then renamed over the target (spec 17: same
// writeIfChanged contract as internal/generate; a crash leaves old or new, never a
// half file).
func writeIfChanged(path string, data []byte) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	sweepTemp(dir)
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return false, fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
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

// sweepTemp removes stale temp files a previously-killed run left in dir.
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

// WriteResult reports what Write changed for one artifact.
type WriteResult struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Changed bool   `json:"changed"`
}

// Write materializes every artifact via the atomic writeIfChanged and reports what
// changed. Re-running with byte-identical config writes nothing (spec 17 #4).
func Write(arts []Artifact) ([]WriteResult, error) {
	out := make([]WriteResult, 0, len(arts))
	for _, a := range arts {
		changed, err := writeIfChanged(a.Path, a.Data)
		if err != nil {
			return out, err
		}
		out = append(out, WriteResult{Path: a.Rel, Kind: a.Kind, Changed: changed})
	}
	return out, nil
}

// UpToDate reports whether every artifact already matches disk (basis for --check).
func UpToDate(arts []Artifact) bool {
	for _, a := range arts {
		existing, err := os.ReadFile(a.Path)
		if err != nil || !bytes.Equal(existing, a.Data) {
			return false
		}
	}
	return true
}
