package template

import (
	"fmt"
	"io/fs"
	"maps"
	"path"
	"sort"

	"github.com/goccy/go-yaml"

	"github.com/open-source-cloud/devstack/internal/merge"
)

// maxExtendsDepth bounds the extends chain so a cycle or pathological hierarchy
// fails fast instead of looping.
const maxExtendsDepth = 32

// Resolved is the fully-rendered, extends-merged result for one template.
type Resolved struct {
	// Name is the leaf template reference.
	Name string
	// Chain is the resolved template names, root ancestor first, leaf last.
	Chain []string
	// Service is the merged compose service fragment (base→leaf deep-merge).
	Service map[string]any
	// Volumes is the merged top-level named-volume fragment.
	Volumes map[string]any
	// BuildFiles maps a path RELATIVE to the template's build/ dir to its
	// rendered bytes (child layers override a parent's same-path file).
	BuildFiles map[string][]byte
	// Provides is the capability this template offers (inherited leaf→root).
	Provides string
	// Exports lists the attributes a consumer may import (inherited leaf→root).
	Exports []string
	// DefaultPort is the in-network port for a shared engine (inherited leaf→root).
	DefaultPort int
	// Params is the effective param set applied to every layer.
	Params map[string]any
}

// layer is one resolved level of the extends chain.
type layer struct {
	name string
	fsys fs.FS
	raw  []byte
	meta *Manifest
}

// Resolve renders the template named ref from src, resolving its extends chain,
// applying userParams over the chain defaults, and deep-merging each rendered
// layer base→leaf. Missing required params or an unresolvable/cyclic parent are
// reported as errors.
func Resolve(src TemplateSource, ref string, userParams map[string]any) (*Resolved, error) {
	chain, err := loadChain(src, ref)
	if err != nil {
		return nil, err
	}

	// Collect param specs across the chain (ancestors first so leaf wins).
	specs := map[string]ParamSpec{}
	for _, l := range chain {
		maps.Copy(specs, l.meta.Params)
	}
	params, err := effectiveParams(specs, userParams)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", ref, err)
	}
	data := map[string]any{"params": params}

	res := &Resolved{
		Name:       ref,
		Service:    map[string]any{},
		Volumes:    map[string]any{},
		BuildFiles: map[string][]byte{},
		Params:     params,
	}
	for _, l := range chain {
		res.Chain = append(res.Chain, l.name)

		// Render this layer's service fragment with the shared params.
		man, err := renderManifest(l.name, l.raw, data)
		if err != nil {
			return nil, err
		}
		if man.Service != nil {
			res.Service = merge.Merge(res.Service, man.Service)
		}
		if man.Volumes != nil {
			res.Volumes = merge.Merge(res.Volumes, man.Volumes)
		}

		// Inherit identity attrs leaf-wins: a later (leaf) non-empty value
		// overrides the ancestor's.
		if l.meta.Provides != "" {
			res.Provides = l.meta.Provides
		}
		if len(l.meta.Exports) > 0 {
			res.Exports = l.meta.Exports
		}
		if l.meta.DefaultPort != 0 {
			res.DefaultPort = l.meta.DefaultPort
		}

		// Render this layer's build/ tree (child paths override parent paths).
		if err := renderBuildTree(l, data, res.BuildFiles); err != nil {
			return nil, err
		}
	}

	// post_init.yaml on the leaf is the documented post-chain merge layer
	// (spec 02): extends base → leaf template.yaml → post_init.yaml.
	leaf := chain[len(chain)-1]
	if err := mergePostInit(leaf, data, res); err != nil {
		return nil, err
	}
	return res, nil
}

// mergePostInit renders the leaf template's optional post_init.yaml and merges its
// service:/volumes: fragments on top of the extends-merged result.
func mergePostInit(leaf layer, data any, res *Resolved) error {
	raw, err := fs.ReadFile(leaf.fsys, PostInitFile)
	if err != nil {
		return nil // absent → nothing to merge
	}
	man, err := renderManifest(leaf.name+"/"+PostInitFile, raw, data)
	if err != nil {
		return err
	}
	if man.Service != nil {
		res.Service = merge.Merge(res.Service, man.Service)
	}
	if man.Volumes != nil {
		res.Volumes = merge.Merge(res.Volumes, man.Volumes)
	}
	return nil
}

// loadChain walks ref's extends pointers to the root, returning the chain ordered
// root-ancestor-first, leaf-last. Cycles and excessive depth are errors.
func loadChain(src TemplateSource, ref string) ([]layer, error) {
	var rev []layer
	seen := map[string]bool{}
	cur := ref
	for depth := 0; cur != ""; depth++ {
		if depth >= maxExtendsDepth {
			return nil, fmt.Errorf("template %q: extends chain too deep (cycle?)", ref)
		}
		if seen[cur] {
			return nil, fmt.Errorf("template %q: extends cycle detected at %q", ref, cur)
		}
		seen[cur] = true

		fsys, err := src.Resolve(cur)
		if err != nil {
			return nil, err
		}
		raw, err := fs.ReadFile(fsys, TemplateFile)
		if err != nil {
			return nil, fmt.Errorf("template %q: read %s: %w", cur, TemplateFile, err)
		}
		meta, err := parseMeta(cur, raw)
		if err != nil {
			return nil, err
		}
		rev = append(rev, layer{name: cur, fsys: fsys, raw: raw, meta: meta})
		cur = meta.Extends
	}
	// rev is leaf→root; reverse to root→leaf.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// renderManifest renders the full template.yaml text with data and parses the
// result, yielding a Manifest whose Service block has params substituted.
func renderManifest(name string, raw []byte, data any) (*Manifest, error) {
	rendered, err := RenderText(name+"/"+TemplateFile, raw, data)
	if err != nil {
		return nil, err
	}
	// Re-parse the rendered document; reuse parseMeta then keep the service block.
	full := map[string]any{}
	if err := yaml.Unmarshal(rendered, &full); err != nil {
		return nil, fmt.Errorf("template %s: rendered %s is not valid YAML: %w", name, TemplateFile, err)
	}
	man := &Manifest{}
	if svc, ok := full["service"].(map[string]any); ok {
		man.Service = svc
	}
	if vol, ok := full["volumes"].(map[string]any); ok {
		man.Volumes = vol
	}
	return man, nil
}

// renderBuildTree renders every file under the layer's build/ dir into dst,
// keyed by its path relative to build/.
func renderBuildTree(l layer, data any, dst map[string][]byte) error {
	if _, err := fs.Stat(l.fsys, BuildDir); err != nil {
		return nil // no build/ tree for this layer
	}
	return fs.WalkDir(l.fsys, BuildDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(l.fsys, p)
		if err != nil {
			return fmt.Errorf("template %q: read %s: %w", l.name, p, err)
		}
		rel, err := relUnderBuild(p)
		if err != nil {
			return err
		}
		rendered, err := RenderText(l.name+"/"+p, raw, data)
		if err != nil {
			return err
		}
		dst[rel] = rendered
		return nil
	})
}

// relUnderBuild strips the leading "build/" from a walked path.
func relUnderBuild(p string) (string, error) {
	rel := p
	if len(p) > len(BuildDir)+1 && p[:len(BuildDir)+1] == BuildDir+"/" {
		rel = p[len(BuildDir)+1:]
	}
	return path.Clean(rel), nil
}

// BuildFileNames returns the rendered build-file paths in sorted order (handy for
// deterministic iteration when writing them out).
func (r *Resolved) BuildFileNames() []string {
	names := make([]string, 0, len(r.BuildFiles))
	for n := range r.BuildFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
