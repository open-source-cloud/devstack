package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/registry"
	"github.com/open-source-cloud/devstack/internal/store"
	"github.com/open-source-cloud/devstack/internal/template"
)

// materializeBundle unpacks a bundle tar into the digest-keyed cache dir, atomically
// and idempotently. When cacheDir already holds the bundle (a prior pull of the same
// digest), it is reused as-is — the digest is the content address, so identical
// digest ⇒ identical content. Otherwise the tar is unpacked into a sibling temp dir
// and renamed into place, so a kill mid-unpack never leaves a half-populated,
// wrongly-trusted cache entry (spec 19 §"unpack atomically"). Returns the bundle's
// template name (its single top-level directory).
func materializeBundle(tarData []byte, cacheDir string) (string, error) {
	// Fast path: already cached — find the single top-level template dir.
	if name, ok := cachedBundleName(cacheDir); ok {
		return name, nil
	}
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(cacheDir), ".tmp-tmpl-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	name, err := registry.UnpackBundle(tarData, tmp)
	if err != nil {
		return "", err
	}
	// Rename the populated temp dir into the final cache dir. If a concurrent pin
	// won the race (dir now exists), reuse it.
	if err := os.Rename(tmp, cacheDir); err != nil {
		if _, statErr := os.Stat(cacheDir); statErr == nil {
			return name, nil
		}
		return "", err
	}
	return name, nil
}

// cachedBundleName returns the single template-name subdir of a populated cache
// dir, or ("", false) when the dir is absent/empty.
func cachedBundleName(cacheDir string) (string, bool) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(cacheDir, e.Name(), template.TemplateFile)); err == nil {
			return e.Name(), true
		}
	}
	return "", false
}

// bundleSchemaVersion reads the cached template's declared schemaVersion.
func bundleSchemaVersion(cacheDir, name string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(cacheDir, name, template.TemplateFile))
	if err != nil {
		return 0, err
	}
	var m struct {
		SchemaVersion int `yaml:"schemaVersion"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return 0, fmt.Errorf("template %q: %s: %w", name, template.TemplateFile, err)
	}
	return m.SchemaVersion, nil
}

// renderPinned renders the pinned (cached) template's single-service compose,
// resolving its extends chain through the built-in source (a base may be a
// built-in). Used by `template diff` to compare against the remote render.
func renderPinned(name, digest string) ([]byte, error) {
	dir, err := store.TemplateCacheDir(digest)
	if err != nil {
		return nil, err
	}
	src := template.NewChainSource(template.NewFSSource(os.DirFS(dir)), builtinSource())
	return renderVia(src, name)
}

// renderPulled renders a freshly-pulled bundle by unpacking it to a temp dir and
// resolving through the built-in source. Used by `template diff`.
func renderPulled(name string, pulled registry.Pulled) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "devstack-diff-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	top, err := registry.UnpackBundle(pulled.Tar, tmp)
	if err != nil {
		return nil, err
	}
	src := template.NewChainSource(template.NewFSSource(os.DirFS(tmp)), builtinSource())
	return renderVia(src, top)
}

// renderVia resolves name through src and renders its single-service compose.
func renderVia(src template.TemplateSource, name string) ([]byte, error) {
	res, err := template.Resolve(src, name, nil)
	if err != nil {
		return nil, err
	}
	return generate.LintResolved(name, res)
}

// refWithTagDigest returns a copy of ref pinned to digest (tag preserved).
func refWithTagDigest(ref registry.Reference, digest string) registry.Reference {
	ref.Digest = digest
	return ref
}

// templateCached reports whether a registered template's content is present in the
// digest cache.
func templateCached(rt store.RemoteTemplate) bool {
	dir, err := store.TemplateCacheDir(rt.Digest)
	if err != nil {
		return false
	}
	_, ok := cachedBundleName(dir)
	return ok
}

// shortDigest renders the first 12 hex chars of a sha256:… digest for display.
func shortManifestDigest(d string) string {
	const prefix = "sha256:"
	if len(d) > len(prefix)+12 {
		return d[len(prefix) : len(prefix)+12]
	}
	return d
}
