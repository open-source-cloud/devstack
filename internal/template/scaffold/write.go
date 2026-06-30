package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"
	"time"

	"github.com/open-source-cloud/devstack/internal/template"
)

// WriteBundle writes b under parentDir/<name>/ atomically (same-dir temp + Sync +
// Chmod 0644 + Rename), creating sub-directories as needed. It refuses to clobber an
// existing template dir unless force, which first backs the prior dir up to
// <name>.bak.<unixts>/. It returns the written file paths, sorted.
func WriteBundle(parentDir, name string, b Bundle, force bool) ([]string, error) {
	target := filepath.Join(parentDir, name)
	if _, err := os.Stat(target); err == nil {
		if !force {
			return nil, fmt.Errorf("%s already exists; pass --force to overwrite (the original is backed up)", target)
		}
		backup := fmt.Sprintf("%s.bak.%d", target, time.Now().Unix())
		if err := os.Rename(target, backup); err != nil {
			return nil, fmt.Errorf("back up %s: %w", target, err)
		}
	}
	var written []string
	for _, rel := range bundlePaths(b) {
		dst := filepath.Join(target, filepath.FromSlash(rel))
		if err := writeFileAtomic(dst, b[rel]); err != nil {
			return written, err
		}
		written = append(written, dst)
	}
	sort.Strings(written)
	return written, nil
}

// bundlePaths returns the bundle's relpaths sorted (deterministic write order).
func bundlePaths(b Bundle) []string {
	out := make([]string, 0, len(b))
	for p := range b {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// WriteFile writes data to path atomically (same-dir temp + Sync + Chmod 0644 +
// Rename), creating the parent dir as needed. Used by `template new --regold`.
func WriteFile(path string, data []byte) error { return writeFileAtomic(path, data) }

// writeFileAtomic writes data to path via a same-dir temp file + Sync + Chmod 0644
// + Rename — the writeIfChanged crash-safety pattern (a kill leaves the old file or
// the new file, never a half-written one).
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".devstack-tmpl-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}

// PreviewSource exposes a Bundle as a TemplateSource so the live preview can run the
// REAL template.Resolve + generate.LintResolved path (no mock renderer). The bundle
// is chained ahead of fallback so an `extends` parent resolves from the built-ins/
// store, while the authored template wins by name. golden.yaml is excluded (it is
// the render OUTPUT, not template input).
func PreviewSource(name string, b Bundle, fallback template.TemplateSource) template.TemplateSource {
	m := fstest.MapFS{}
	for rel, data := range b {
		if rel == "golden.yaml" {
			continue
		}
		m[name+"/"+rel] = &fstest.MapFile{Data: data}
	}
	authored := template.NewFSSource(m)
	if fallback == nil {
		return authored
	}
	return template.NewChainSource(authored, fallback)
}

// BuildFilesOf returns the bundle's build/ entries keyed by their relpath — the
// input the delimiter-collision lint consumes.
func BuildFilesOf(b Bundle) map[string][]byte {
	out := map[string][]byte{}
	for rel, data := range b {
		if strings.HasPrefix(rel, "build/") {
			out[rel] = data
		}
	}
	return out
}
