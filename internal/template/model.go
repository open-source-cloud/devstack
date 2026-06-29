package template

import (
	"fmt"
	"maps"

	"github.com/goccy/go-yaml"
)

// TemplateFile is the metadata + fragment manifest at the root of every template
// directory.
const TemplateFile = "template.yaml"

// BuildDir is the optional subtree of file templates (Dockerfiles, entrypoints,
// proxy conf) rendered verbatim with renderText.
const BuildDir = "build"

// PostInitFile is an optional per-template fragment merged AFTER the whole
// extends chain (spec 02 merge order: extends base → leaf template.yaml →
// post_init.yaml → project overrides). It carries the same service:/volumes:
// shape as template.yaml.
const PostInitFile = "post_init.yaml"

// Manifest is the parsed template.yaml. The meta fields (everything except
// service:) must not themselves contain template actions — they are read from the
// UNrendered parse so params/extends are known before rendering. The service:
// fragment is read from the rendered parse (params substituted).
type Manifest struct {
	SchemaVersion int                  `yaml:"schemaVersion"`
	Extends       string               `yaml:"extends"`
	Description   string               `yaml:"description"`
	Provides      string               `yaml:"provides"` // capability, e.g. "postgres" (spec 01 capability matching)
	Exports       []string             `yaml:"exports"`  // attrs a consumer may import (host, port, user, ...)
	DefaultPort   int                  `yaml:"defaultPort"`
	Params        map[string]ParamSpec `yaml:"params"`
	// Service is the compose service fragment. Parsed from the RENDERED file; the
	// value captured during the meta parse is ignored.
	Service map[string]any `yaml:"service"`
	// Volumes is the top-level named-volume fragment this template contributes
	// (e.g. a shared engine's data volume). Also parsed from the rendered file.
	Volumes map[string]any `yaml:"volumes"`
}

// ParamSpec is one typed, defaulted parameter declaration.
type ParamSpec struct {
	Type        string `yaml:"type"` // string|int|bool (advisory in v1)
	Default     any    `yaml:"default"`
	Required    bool   `yaml:"required"`
	Description string `yaml:"description"`
}

// parseMeta reads the meta fields (schemaVersion/extends/params/...) from the
// unrendered template.yaml bytes. The service: block is intentionally not trusted
// here (it still carries template actions).
func parseMeta(name string, raw []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("template %s: %s: %w", name, TemplateFile, err)
	}
	// Discard the un-rendered fragments; they still carry template actions and are
	// re-read from the rendered document.
	m.Service = nil
	m.Volumes = nil
	return &m, nil
}

// effectiveParams overlays user-supplied params on the merged chain defaults and
// verifies every required param is satisfied. specs is the param declarations
// collected across the whole extends chain (leaf last so leaf defaults win).
func effectiveParams(specs map[string]ParamSpec, user map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for name, spec := range specs {
		if spec.Default != nil {
			out[name] = spec.Default
		}
	}
	maps.Copy(out, user)
	// Required-param enforcement (spec 02 acceptance #5): fail fast, never render
	// an empty string for a missing required value.
	var missing []string
	for name, spec := range specs {
		if !spec.Required {
			continue
		}
		if _, ok := out[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required template param(s): %v", sortedStrings(missing))
	}
	return out, nil
}

func sortedStrings(s []string) []string {
	// tiny insertion sort to avoid importing sort for a 1-3 element slice
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
	return s
}
