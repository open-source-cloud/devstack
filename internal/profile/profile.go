// Package profile resolves which services `up --profile` activates (spec 12,
// Q-PROFILE RESOLVED): service slices are declared at BOTH planes — per-service
// Compose `profiles:` tags and workspace-level `groups:` — unioned by name.
// `defaultProfile` is opt-in; the no-config default is the reserved `all` (every
// service). An explicit `--profile X` activates exactly the X slice (the services
// in group X plus those tagged X), never the whole workspace — that's the point
// of selective-up. The active set also pulls in the shared services those active
// services transitively `uses`.
//
// Pure config logic (no daemon): the saga consumes Active to slice compose-up and
// the health DAG prunes to the active nodes.
package profile

import (
	"slices"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/config"
)

// All is the reserved profile name that activates the whole workspace.
const All = "all"

// Active is the resolved selection: active services per project plus the shared
// instances they transitively use.
type Active struct {
	Services map[string][]string `json:"services"` // project -> sorted active service names
	Shared   []string            `json:"shared"`   // sorted shared service names used by active services
}

// Has reports whether a project's service is active.
func (a Active) Has(project, service string) bool {
	return slices.Contains(a.Services[project], service)
}

// Resolve computes the active set for the requested profiles (a repeatable,
// comma-separated union from --profile). Empty requested → defaultProfile, or the
// reserved `all` when none is configured.
func Resolve(m *config.Model, requested []string) Active {
	profiles := normalize(requested)
	if len(profiles) == 0 {
		if dp := m.Workspace.DefaultProfile; dp != "" {
			profiles[dp] = true
		} else {
			profiles[All] = true
		}
	}
	all := profiles[All]

	out := Active{Services: map[string][]string{}}
	for _, project := range sortedKeys(m.Projects) {
		p := m.Projects[project]
		var active []string
		for _, sname := range sortedKeys(p.Services) {
			if all || serviceActive(m, profiles, p.Services[sname], sname) {
				active = append(active, sname)
			}
		}
		if len(active) > 0 {
			out.Services[project] = active
		}
	}
	out.Shared = sharedUsedBy(m, out.Services)
	return out
}

// serviceActive reports whether a service is in any active group or carries an
// active profile tag.
func serviceActive(m *config.Model, profiles map[string]bool, svc config.Service, sname string) bool {
	for name := range profiles {
		if g, ok := m.Workspace.Groups[name]; ok && slices.Contains(g.Services, sname) {
			return true
		}
	}
	for _, tag := range svc.Profiles {
		if profiles[tag] {
			return true
		}
	}
	return false
}

// sharedUsedBy returns the sorted shared service names the active services use.
func sharedUsedBy(m *config.Model, active map[string][]string) []string {
	seen := map[string]bool{}
	for project, services := range active {
		p := m.Projects[project]
		for _, sname := range services {
			for _, u := range p.Services[sname].Uses {
				if ref, ok := config.ParseRef(u); ok && ref.Kind == config.RefShared {
					seen[ref.Name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// normalize splits comma-separated + repeated profile flags into a set.
func normalize(requested []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range requested {
		for part := range strings.SplitSeq(r, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out[p] = true
			}
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
