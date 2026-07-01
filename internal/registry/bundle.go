package registry

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// bundleEpoch is the fixed modification time stamped on every packed tar entry so
// the artifact digest never depends on filesystem timestamps. A constant non-zero
// epoch (rather than the tar zero value) keeps the PAX header stable across
// archive/tar versions.
func bundleEpoch() time.Time { return time.Unix(0, 0).UTC() }

// TemplateFile is the manifest at the root of every template bundle. Duplicated
// from internal/template to avoid an import cycle (template imports nothing from
// registry, but the CLI wires them together).
const TemplateFile = "template.yaml"

// bundleMode is the fixed file mode stamped on every packed entry so the tar (and
// thus the artifact digest) is independent of the author's umask. Directories get
// the exec bit; regular files are 0644.
const (
	bundleFileMode = 0o644
	bundleDirMode  = 0o755
)

// maxBundleBytes caps the unpacked bundle size to defuse a decompression/pull
// bomb from an untrusted registry (templates are small — a few KB).
const maxBundleBytes = 32 << 20 // 32 MiB

// bundleMeta is the subset of template.yaml the registry cares about: the
// bundle-schema version gate (spec 19 §"What travels"). It is parsed from the
// UNrendered manifest (the meta fields never carry template actions).
type bundleMeta struct {
	SchemaVersion int `yaml:"schemaVersion"`
}

// PackBundle reads a template directory and returns a DETERMINISTIC tar of it,
// rooted at "<name>/…" where name is dir's base (so the unpacked cache dir has a
// single top-level template-name directory, exactly like the embedded FSSource
// layout). Entries are emitted in sorted path order with zeroed timestamps and
// fixed ownership/mode so re-packing identical content yields byte-identical bytes
// — the artifact digest is a pure function of content (spec 19 determinism AC).
//
// The directory MUST contain a template.yaml; its schemaVersion is recorded so a
// consumer can reject a bundle newer than it understands.
func PackBundle(dir string) (data []byte, name string, schemaVersion int, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", 0, err
	}
	name = filepath.Base(abs)
	if !isValidName(name) {
		return nil, "", 0, fmt.Errorf("template bundle dir %q is not a valid template name (single path segment, no slash/..)", name)
	}

	manifest, err := os.ReadFile(filepath.Join(abs, TemplateFile))
	if err != nil {
		return nil, "", 0, fmt.Errorf("template bundle %q: missing %s: %w", name, TemplateFile, err)
	}
	var meta bundleMeta
	if err := yaml.Unmarshal(manifest, &meta); err != nil {
		return nil, "", 0, fmt.Errorf("template bundle %q: %s is not valid YAML: %w", name, TemplateFile, err)
	}
	schemaVersion = meta.SchemaVersion

	// Collect every regular file under dir, keyed by its slash path relative to
	// dir, then sort for deterministic emission.
	files := map[string][]byte{}
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(abs, p)
		if rerr != nil {
			return rerr
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, "", 0, err
	}

	rels := make([]string, 0, len(files))
	for k := range files {
		rels = append(rels, k)
	}
	sort.Strings(rels)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, rel := range rels {
		body := files[rel]
		hdr := &tar.Header{
			Name:     path.Join(name, rel),
			Mode:     bundleFileMode,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
			// Zeroed metadata for reproducibility.
			ModTime: bundleEpoch(),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", 0, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, "", 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", 0, err
	}
	return buf.Bytes(), name, schemaVersion, nil
}

// UnpackBundle extracts a bundle tar into destDir (which must not yet exist),
// creating a "<name>/…" tree. It refuses path traversal and oversize payloads. It
// returns the template name (the single top-level directory in the tar).
//
// The caller is responsible for atomicity (unpack into a temp dir, then rename) so
// a kill mid-pull never leaves a half-populated, wrongly-trusted cache dir
// (spec 19 §"OCI digests are over the canonical manifest").
func UnpackBundle(data []byte, destDir string) (name string, err error) {
	if err := os.MkdirAll(destDir, bundleDirMode); err != nil {
		return "", err
	}
	tr := tar.NewReader(bytes.NewReader(data))
	var total int64
	top := map[string]struct{}{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read bundle tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // bundles carry only regular files
		}
		clean := path.Clean(hdr.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") || strings.Contains(clean, "/../") {
			return "", fmt.Errorf("bundle entry %q escapes the destination", hdr.Name)
		}
		seg := strings.SplitN(clean, "/", 2)
		top[seg[0]] = struct{}{}

		total += hdr.Size
		if total > maxBundleBytes {
			return "", fmt.Errorf("bundle exceeds %d bytes — refusing to unpack", maxBundleBytes)
		}
		target := filepath.Join(destDir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), bundleDirMode); err != nil {
			return "", err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, bundleFileMode)
		if err != nil {
			return "", err
		}
		if _, err := io.CopyN(f, tr, hdr.Size); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	if len(top) != 1 {
		return "", fmt.Errorf("bundle must contain exactly one top-level template directory, found %d", len(top))
	}
	for n := range top {
		name = n
	}
	if _, err := os.Stat(filepath.Join(destDir, name, TemplateFile)); err != nil {
		return "", fmt.Errorf("bundle %q has no %s", name, TemplateFile)
	}
	return name, nil
}

// isValidName mirrors template.ValidRef without importing it (a template name is a
// single path segment; dots allowed, no slash/"..").
func isValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return name == path.Base(name) && !filepath.IsAbs(name) && !strings.ContainsAny(name, "/\\")
}
