package template

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// TemplateSource resolves a template reference (e.g. "php.laravel.nginx") to a
// read-only filesystem rooted at that template's directory. Sources are pluggable
// behind this interface (spec 02): v1 ships the embedded built-ins; git and OCI
// registry sources are deferred to spec 19.
type TemplateSource interface {
	// Resolve returns an fs.FS rooted at the named template's directory, or an
	// error if the template does not exist in this source.
	Resolve(ref string) (fs.FS, error)
	// Has reports whether the named template exists in this source.
	Has(ref string) bool
	// List returns the template names available in this source, sorted.
	List() []string
}

// FSSource is a TemplateSource backed by any fs.FS whose top-level directories
// are template names. It backs both the embedded built-ins (go:embed) and the
// on-disk directory source.
type FSSource struct {
	root fs.FS
}

// NewFSSource wraps an fs.FS (template-name directories at its root).
func NewFSSource(root fs.FS) *FSSource { return &FSSource{root: root} }

// validRef guards against path traversal: a template ref is a single path
// segment (dots are allowed — "php.laravel.nginx" — but no slashes or "..").
func validRef(ref string) bool {
	if ref == "" || ref == "." || ref == ".." {
		return false
	}
	return ref == path.Base(ref) && !filepath.IsAbs(ref)
}

// ValidRef reports whether ref is a valid template reference (a single path
// segment — dots allowed, no slash/".."/absolute). Exported for the authoring
// wizard (spec 23), which must validate a new template name with the EXACT rule the
// source layer enforces.
func ValidRef(ref string) bool { return validRef(ref) }

func (s *FSSource) Resolve(ref string) (fs.FS, error) {
	if !validRef(ref) {
		return nil, fmt.Errorf("invalid template reference %q", ref)
	}
	sub, err := fs.Sub(s.root, ref)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", ref, err)
	}
	if _, err := fs.Stat(sub, TemplateFile); err != nil {
		// Distinguish "no such template" (the common typo) from "directory exists
		// but lacks a manifest", and offer the available names (ARCHITECTURE §7.6).
		if _, derr := fs.Stat(s.root, ref); derr != nil {
			return nil, fmt.Errorf("template %q not found%s", ref, available(s.List()))
		}
		return nil, fmt.Errorf("template %q: directory has no %s", ref, TemplateFile)
	}
	return sub, nil
}

// available renders a "(available: …)" hint from a name list.
func available(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " (available: " + strings.Join(names, ", ") + ")"
}

func (s *FSSource) Has(ref string) bool {
	if !validRef(ref) {
		return false
	}
	sub, err := fs.Sub(s.root, ref)
	if err != nil {
		return false
	}
	_, err = fs.Stat(sub, TemplateFile)
	return err == nil
}

// List returns the names of every template in this source (top-level directories
// that contain a template.yaml), sorted.
func (s *FSSource) List() []string {
	entries, err := fs.ReadDir(s.root, ".")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && s.Has(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// NewDirSource exposes a single template directory on disk as a one-entry source,
// keyed by the directory's base name. Used by `devstack template lint <dir>`.
func NewDirSource(dir string) (*FSSource, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	name := filepath.Base(abs)
	parent := filepath.Dir(abs)
	return &FSSource{root: os.DirFS(parent)}, name, nil
}

// ChainSource resolves against an ordered list of sources, first match wins
// (e.g. a project-local override source before the embedded built-ins).
type ChainSource struct {
	sources []TemplateSource
}

// NewChainSource builds a source that tries each member in order.
func NewChainSource(sources ...TemplateSource) *ChainSource {
	return &ChainSource{sources: sources}
}

func (c *ChainSource) Resolve(ref string) (fs.FS, error) {
	for _, s := range c.sources {
		if s.Has(ref) {
			return s.Resolve(ref)
		}
	}
	return nil, fmt.Errorf("template %q not found in any source", ref)
}

func (c *ChainSource) Has(ref string) bool {
	for _, s := range c.sources {
		if s.Has(ref) {
			return true
		}
	}
	return false
}

// List returns the union of every member source's templates (deduplicated,
// sorted) — so `template list` shows custom store templates alongside built-ins.
func (c *ChainSource) List() []string {
	seen := map[string]bool{}
	for _, s := range c.sources {
		for _, n := range s.List() {
			seen[n] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
