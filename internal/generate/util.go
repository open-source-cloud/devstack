package generate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/merge"
)

// mapClone returns a deep copy of a YAML-shaped map. ok is false when the input
// is nil/empty (a template that produced no service fragment).
func mapClone(m map[string]any) (map[string]any, bool) {
	if len(m) == 0 {
		return nil, false
	}
	return merge.Clone(m).(map[string]any), true
}

// scalarString renders a scalar env value as a string deterministically. Maps and
// slices are not valid env values and stringify via fmt as a last resort.
func scalarString(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case uint64:
		return fmt.Sprintf("%d", t)
	case float64:
		// Render integral floats without a trailing ".0" (YAML may decode "5" as float).
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// sortedKeys returns a map's keys sorted, for deterministic iteration.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lower(s string) string { return strings.ToLower(s) }
