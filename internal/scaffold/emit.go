// Package scaffold builds and emits a workspace.yaml from typed inputs. It is the
// shared, UI-agnostic core behind `devstack init`: both the flag path and the
// Bubble Tea wizard populate an Inputs, call BuildWorkspace, then EmitWorkspaceYAML.
// It takes no lock, touches no ledger, and starts no Docker — pure config
// authorship (spec 22). Emission is deterministic (ordered goccy MapSlice, sorted
// keys) so re-running is byte-stable.
package scaffold

import (
	"sort"

	"github.com/goccy/go-yaml"

	"github.com/open-source-cloud/devstack/internal/config"
)

// EmitWorkspaceYAML renders a workspace.yaml from a typed model with a fixed key
// order (apiVersion, kind, name, aliases, profiles, shared, projects), omitting
// empty sections. Output is deterministic: shared keys, the project list, and each
// service's params are sorted, and values go through an ordered MapSlice — never
// yaml.Marshal of a Go map (goccy randomizes map order and renders 16 as 16.0).
func EmitWorkspaceYAML(ws config.Workspace) ([]byte, error) {
	apiVersion := ws.APIVersion
	if apiVersion == "" {
		apiVersion = config.APIVersion
	}
	kind := ws.Kind
	if kind == "" {
		kind = config.KindWorkspace
	}

	doc := yaml.MapSlice{
		{Key: "apiVersion", Value: apiVersion},
		{Key: "kind", Value: kind},
		{Key: "name", Value: ws.Name},
	}
	if len(ws.Aliases) > 0 {
		doc = append(doc, yaml.MapItem{Key: "aliases", Value: append([]string(nil), ws.Aliases...)})
	}
	if ws.Profiles.Default != "" {
		doc = append(doc, yaml.MapItem{Key: "profiles",
			Value: yaml.MapSlice{{Key: "default", Value: ws.Profiles.Default}}})
	}
	if len(ws.Shared) > 0 {
		shared := yaml.MapSlice{}
		for _, name := range sortedKeys(ws.Shared) {
			shared = append(shared, yaml.MapItem{Key: name, Value: sharedEntry(ws.Shared[name])})
		}
		doc = append(doc, yaml.MapItem{Key: "shared", Value: shared})
	}
	if len(ws.Projects) > 0 {
		refs := make([]yaml.MapSlice, 0, len(ws.Projects))
		for _, p := range sortedProjects(ws.Projects) {
			refs = append(refs, projectEntry(p))
		}
		doc = append(doc, yaml.MapItem{Key: "projects", Value: refs})
	}
	return yaml.Marshal(doc)
}

// sharedEntry renders one `shared:` value: template, then optional sorted params.
func sharedEntry(s config.SharedSvc) yaml.MapSlice {
	entry := yaml.MapSlice{{Key: "template", Value: s.Template}}
	if len(s.Params) > 0 {
		params := yaml.MapSlice{}
		for _, k := range sortedKeys(s.Params) {
			params = append(params, yaml.MapItem{Key: k, Value: s.Params[k]})
		}
		entry = append(entry, yaml.MapItem{Key: "params", Value: params})
	}
	return entry
}

// projectEntry renders one `projects:` entry: name, path, then optional git. git
// is emitted verbatim — shorthand expansion is `devstack import`'s job, not init's.
func projectEntry(p config.ProjectRef) yaml.MapSlice {
	entry := yaml.MapSlice{{Key: "name", Value: p.Name}, {Key: "path", Value: p.Path}}
	if p.Git != "" {
		entry = append(entry, yaml.MapItem{Key: "git", Value: p.Git})
	}
	return entry
}

// sortedKeys returns a map's keys sorted, for deterministic iteration/output.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedProjects returns a copy of refs sorted by Name (deterministic output).
func sortedProjects(refs []config.ProjectRef) []config.ProjectRef {
	out := append([]config.ProjectRef(nil), refs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
