// Package merge implements the layered deep-merge used to resolve a template's
// `extends` chain and to overlay project config on top of a rendered template
// fragment (spec 02, ARCHITECTURE §6). The merge operates on the generic
// map[string]any / []any trees produced by rendering YAML fragments.
//
// Semantics (DECISIONS D2, spec 02 "Deep-merge"):
//   - maps merge recursively, left-to-right (the OVER layer wins on conflict);
//   - scalars are overwritten;
//   - lists REPLACE by default (NOT append — the Python predecessor appended;
//     getting this wrong silently drops inherited volumes/env entries);
//   - append is opt-in via an explicit `$merge: append` directive (see Directive).
//
// Every value taken from a source layer is deep-CLONED into the result, so the
// merged tree never aliases either input — guarding against the shared-reference
// mutation hazard called out for koanf's maps.Merge (it copies nothing).
package merge

// MergeKey is the reserved directive key. A list authored as a map of the form
//
//	{ $merge: append, $value: [ ... ] }
//
// appends $value onto the corresponding base list instead of replacing it. Any
// other $merge value (or its absence) keeps the default replace behavior.
const (
	MergeKey    = "$merge"
	ValueKey    = "$value"
	appendMode  = "append"
	replaceMode = "replace"
)

// Merge deep-merges over onto base and returns a freshly allocated tree. Neither
// input is mutated and the result aliases neither (everything is cloned). When a
// key exists in both: two maps merge recursively, two lists replace (unless over
// carries a `$merge: append` directive), and any scalar/type-mismatch lets over
// win.
func Merge(base, over map[string]any) map[string]any {
	out := cloneMap(base)
	for k, ov := range over {
		// Route EVERY key through mergeValue (out[k] is nil when absent) so a
		// `$merge` directive is resolved even on a key the base never declared —
		// otherwise the raw {$merge,$value} map would leak into the output.
		out[k] = mergeValue(out[k], ov)
	}
	return out
}

// mergeValue merges a single over value onto the existing base value.
func mergeValue(bv, ov any) any {
	// An explicit append directive: { $merge: append, $value: [...] }.
	if dir, ok := asDirective(ov); ok {
		if dir.mode == appendMode {
			return appendLists(bv, dir.value)
		}
		// replace (or unknown) → take the directive's value.
		return clone(dir.value)
	}

	bm, bIsMap := bv.(map[string]any)
	om, oIsMap := ov.(map[string]any)
	if bIsMap && oIsMap {
		return Merge(bm, om)
	}
	// Lists replace by default; scalars and type mismatches let over win.
	return clone(ov)
}

// directive is a parsed `$merge` instruction.
type directive struct {
	mode  string
	value any
}

// asDirective recognizes a `{ $merge: <mode>, $value: <v> }` map.
func asDirective(v any) (directive, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return directive{}, false
	}
	mode, ok := m[MergeKey]
	if !ok {
		return directive{}, false
	}
	ms, _ := mode.(string)
	return directive{mode: ms, value: m[ValueKey]}, true
}

// appendLists concatenates two list-shaped values (base then add), cloning both.
// Non-list operands are treated as single-element lists so the directive still
// does something sensible on a scalar base.
func appendLists(base, add any) []any {
	out := append([]any{}, toList(base)...)
	out = append(out, toList(add)...)
	return cloneList(out)
}

func toList(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	default:
		return []any{t}
	}
}

// Clone returns a deep copy of an arbitrary YAML-shaped value (maps, slices,
// scalars). Exported because callers staging a fragment before merge need the
// same anti-aliasing guarantee.
func Clone(v any) any { return clone(v) }

func clone(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		return cloneList(t)
	default:
		return t // scalars are immutable
	}
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = clone(v)
	}
	return out
}

func cloneList(s []any) []any {
	if s == nil {
		return nil
	}
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = clone(v)
	}
	return out
}
