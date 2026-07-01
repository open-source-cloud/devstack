package store

import (
	"fmt"
	"os"
	"sort"

	"github.com/opencontainers/go-digest"

	"github.com/open-source-cloud/devstack/internal/xdg"
)

// RemoteTemplate is one registered remote template source in the store config
// (spec 19). It is the lockfile row: a local `name` resolvable in the template
// source chain, the `source` registry repository, the human `version` tag (kept
// for provenance / diffs only), and the `digest` that is ACTUALLY fetched and
// verified. `schemaVersion` is the template-bundle schema the pinned content
// declares — a bundle newer than this binary understands is refused on add.
//
// The task-scoped v2 slice keeps this lockfile in the store config
// (~/.devstack/config.yaml). The full spec-19 target is the committed
// `templates:` block in workspace.yaml; the shape here is deliberately identical
// so that migration is a move, not a redesign.
type RemoteTemplate struct {
	Name          string `yaml:"name"`
	Source        string `yaml:"source"`
	Version       string `yaml:"version"`
	Digest        string `yaml:"digest"`
	SchemaVersion int    `yaml:"schemaVersion,omitempty"`
}

// TemplateCacheRoot is the digest-keyed template cache root:
// $XDG_CACHE_HOME/devstack/templates. Content is addressed by digest (never by
// tag) so a re-pushed tag lands under a NEW key and a pinned workspace never
// silently reaches poisoned content (spec 19 §"Cache poisoning if the key is the
// tag"). The cache lives on the Linux side under WSL2 (xdg refuses /mnt/*).
func TemplateCacheRoot() string {
	return xdg.CacheHome() + string(os.PathSeparator) + "templates"
}

// TemplateCacheDir returns the cache directory for a manifest digest:
// <cacheRoot>/<algo>/<hex>. The unpacked bundle's single "<name>/…" tree lives
// under it, so an FSSource rooted here lists the template by name.
func TemplateCacheDir(dig string) (string, error) {
	d, err := digest.Parse(dig)
	if err != nil {
		return "", fmt.Errorf("invalid digest %q: %w", dig, err)
	}
	return TemplateCacheRoot() + string(os.PathSeparator) + d.Algorithm().String() + string(os.PathSeparator) + d.Encoded(), nil
}

// Template returns the registered remote template with the given name.
func (c *Config) Template(name string) (RemoteTemplate, bool) {
	for _, t := range c.Templates {
		if t.Name == name {
			return t, true
		}
	}
	return RemoteTemplate{}, false
}

// UpsertTemplate inserts or replaces a remote-template entry by name and returns
// whether an existing entry was replaced. Callers hold the flock (this mutates
// machine-global state) and Save afterwards.
func (c *Config) UpsertTemplate(rt RemoteTemplate) (replaced bool) {
	for i, t := range c.Templates {
		if t.Name == rt.Name {
			c.Templates[i] = rt
			return true
		}
	}
	c.Templates = append(c.Templates, rt)
	sort.Slice(c.Templates, func(i, j int) bool { return c.Templates[i].Name < c.Templates[j].Name })
	return false
}

// RemoveTemplate drops the entry with the given name, reporting whether it existed.
func (c *Config) RemoveTemplate(name string) (removed bool) {
	out := c.Templates[:0]
	for _, t := range c.Templates {
		if t.Name == name {
			removed = true
			continue
		}
		out = append(out, t)
	}
	c.Templates = out
	return removed
}
