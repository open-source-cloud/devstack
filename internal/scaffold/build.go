package scaffold

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/template"
)

// ServiceInput is one shared engine the user chose, with raw string param
// overrides. The `engine@ver` shorthand is folded into Params["version"] by the
// caller before reaching BuildWorkspace.
type ServiceInput struct {
	Engine string
	Params map[string]string
}

// Inputs is the UI-agnostic description of a workspace to author. Both the flag
// path and the Bubble Tea wizard populate it, then call BuildWorkspace — the
// spec-22 "two faces, one builder" seam.
type Inputs struct {
	Name     string
	Profile  string
	Aliases  []string
	Services []ServiceInput
	Projects []config.ProjectRef
	// FromStore seeds shared services from the global store (already config types);
	// nil when --from-store was not given. An explicit Service overrides a same-named seed.
	FromStore map[string]config.SharedSvc
}

// BuildWorkspace assembles a typed config.Workspace from Inputs, resolving each
// chosen engine's params against its template metadata. It rejects a non-shared
// template (empty Provides), an unknown engine, an unknown param key, or a missing
// required param — the same failures generation would hit later, surfaced now.
func BuildWorkspace(src template.TemplateSource, in Inputs) (config.Workspace, error) {
	shared := map[string]config.SharedSvc{}
	// Seed from the store first (verbatim) so explicit --service entries override.
	for name, svc := range in.FromStore {
		shared[name] = config.SharedSvc{Template: svc.Template, Params: maps.Clone(svc.Params)}
	}

	for _, s := range in.Services {
		desc, err := template.Describe(src, s.Engine)
		if err != nil {
			return config.Workspace{}, fmt.Errorf("unknown template %q: %w", s.Engine, err)
		}
		if desc.Provides == "" {
			return config.Workspace{}, fmt.Errorf(
				"%q is not a shared engine (only engines that declare `provides:`, e.g. postgres/redis/minio, can be shared)", s.Engine)
		}
		params, err := resolveParams(s.Engine, desc.Params, s.Params)
		if err != nil {
			return config.Workspace{}, err
		}
		shared[s.Engine] = config.SharedSvc{Template: s.Engine, Params: params}
	}

	return config.Workspace{
		APIVersion: config.APIVersion,
		Kind:       config.KindWorkspace,
		Name:       in.Name,
		Aliases:    in.Aliases,
		Profiles:   config.Profiles{Default: in.Profile},
		Shared:     shared,
		Projects:   in.Projects,
	}, nil
}

// resolveParams overlays user string params on a template's declared params: it
// rejects unknown keys, drops a value equal to the template default (minimal,
// diff-stable output), and fails fast when a required param has neither a default
// nor a user value (mirrors template.effectiveParams). Kept values stay strings,
// so the emitter never produces a float (16, never 16.0) and `@ver` == --from-store.
func resolveParams(engine string, specs map[string]template.ParamSpec, user map[string]string) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range user {
		spec, ok := specs[k]
		if !ok {
			return nil, fmt.Errorf("service %q: unknown param %q (valid: %s)", engine, k, strings.Join(sortedKeys(specs), ", "))
		}
		if spec.Default != nil && fmt.Sprint(spec.Default) == v {
			continue // already at the template default → omit for minimal output
		}
		out[k] = v
	}

	var missing []string
	for name, spec := range specs {
		if !spec.Required {
			continue
		}
		if _, set := out[name]; !set && spec.Default == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("service %q: missing required param(s): %s", engine, strings.Join(missing, ", "))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
