package generate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/template"
)

// sharedAliasPrefix is reserved for shared-service DNS aliases (shared-<name>);
// project services may not usurp it (ARCHITECTURE §4 collision guardrail).
const sharedAliasPrefix = "shared-"

// lintWorkspace is the generate-time collision guardrail mandated by
// ARCHITECTURE §4. Because every project service joins the tool-owned external
// `devstack_shared` network, two hazards must be rejected up front:
//
//  1. a project service named `shared-*` would usurp a shared-service alias;
//  2. the same bare service name declared in two projects would resolve
//     ambiguously to both containers over the shared network.
//
// Shared services are themselves only ever addressed via their `shared-<name>`
// alias (the resolver never emits a bare shared name), so that half of the
// guardrail is structural; this lint covers the project side.
func lintWorkspace(m *config.Model) error {
	owners := map[string][]string{} // bare service name → "project/service" owners
	for _, pname := range sortedKeys(m.Projects) {
		p := m.Projects[pname]
		for _, sname := range sortedKeys(p.Services) {
			if strings.HasPrefix(sname, sharedAliasPrefix) {
				return fmt.Errorf("project %q service %q: name must not start with %q (reserved for shared-service DNS aliases on the %s network)",
					pname, sname, sharedAliasPrefix, SharedNetwork)
			}
			owners[sname] = append(owners[sname], pname+"/"+sname)
		}
	}
	for _, sname := range sortedStrings(owners) {
		if list := owners[sname]; len(list) > 1 {
			return fmt.Errorf("service name %q is declared by multiple projects (%s); they would collide on the %s network — rename one",
				sname, strings.Join(list, ", "), SharedNetwork)
		}
	}
	return nil
}

func sortedStrings[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LintResolved validates a resolved template's service fragment by embedding it
// in a minimal one-service compose document and running it through compose-go
// (schema + consistency check + normalization). It returns the canonical
// single-service compose for display, or a descriptive error. Build contexts are
// not required to exist on disk (path resolution is off).
func LintResolved(name string, res *template.Resolved) ([]byte, error) {
	svc, ok := mapClone(res.Service)
	if !ok {
		return nil, fmt.Errorf("template %q produced no service fragment", name)
	}
	doc := composeDoc{
		"name":     "devstack-lint",
		"services": map[string]any{name: svc},
	}
	if len(res.Volumes) > 0 {
		doc["volumes"] = mustClone(res.Volumes)
	}
	return validateAndMarshal(doc, ".")
}

func mustClone(m map[string]any) map[string]any {
	out, _ := mapClone(m)
	return out
}
