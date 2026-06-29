package template

import (
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

// funcMap is the curated, DETERMINISTIC helper set available inside templates.
// Every function is a pure transform of its inputs — there is intentionally no
// `now`, `uuid`, `randAlphaNum`, or environment access, because such helpers
// would break the byte-identical-output guarantee (spec 02 acceptance #3) and
// the SHA-256 rebuild hash. This is the "ported filters" set from the spec,
// reimplemented in-tree (rather than pulling Masterminds/sprig, which carries
// the non-deterministic helpers) so it stays small, auditable, and swappable.
func funcMap() template.FuncMap {
	return template.FuncMap{
		// defaulting
		"default": func(def, given any) any {
			if isEmpty(given) {
				return def
			}
			return given
		},
		"coalesce": func(vals ...any) any {
			for _, v := range vals {
				if !isEmpty(v) {
					return v
				}
			}
			return nil
		},
		// case
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"title": titleCase,
		// trimming
		"trim":       strings.TrimSpace,
		"trimPrefix": func(prefix, s string) string { return strings.TrimPrefix(s, prefix) },
		"trimSuffix": func(suffix, s string) string { return strings.TrimSuffix(s, suffix) },
		// substitution / predicates
		"replace":   func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
		"contains":  func(substr, s string) bool { return strings.Contains(s, substr) },
		"hasPrefix": func(prefix, s string) bool { return strings.HasPrefix(s, prefix) },
		"hasSuffix": func(suffix, s string) bool { return strings.HasSuffix(s, suffix) },
		// list joins / splits. join accepts both []string and the []any that a
		// YAML-declared list param decodes to, stringifying each element.
		"join":  joinAny,
		"split": func(sep, s string) []string { return strings.Split(s, sep) },
		// quoting
		"quote":  func(s string) string { return `"` + s + `"` },
		"squote": func(s string) string { return "'" + s + "'" },
		// indentation (handy for embedding scripts into YAML fragments)
		"indent":  func(n int, s string) string { return indent(n, s) },
		"nindent": func(n int, s string) string { return "\n" + indent(n, s) },
		"repeat":  func(n int, s string) string { return strings.Repeat(s, n) },
		// numeric — atoi parses the leading integer (e.g. major version "16"),
		// enabling version-conditional fragments with the builtin lt/ge compares.
		"atoi": atoi,
	}
}

// atoi parses the leading integer of s (the major version of "16" or "9.6"),
// returning 0 when there is no leading integer. Pure and deterministic.
func atoi(s string) int {
	s = strings.TrimSpace(s)
	if before, _, found := strings.Cut(s, "."); found {
		s = before
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// joinAny joins a list with sep, accepting []string, []any (the form a YAML list
// param decodes to), or a single scalar. Each element is stringified with fmt so
// `[[ join "," .params.items ]]` works on a declared list param.
func joinAny(sep string, list any) string {
	switch t := list.(type) {
	case []string:
		return strings.Join(t, sep)
	case []any:
		parts := make([]string, len(t))
		for i, v := range t {
			parts[i] = fmt.Sprint(v)
		}
		return strings.Join(parts, sep)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// titleCase upper-cases the first letter of each space-separated word (ASCII).
// A small, dependency-free stand-in for the deprecated strings.Title.
func titleCase(s string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// isEmpty reports whether v is the zero value for its kind (used by `default`).
func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case int:
		return t == 0
	case int64:
		return t == 0
	case float64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// indent prefixes every line of s with n spaces.
func indent(n int, s string) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln != "" {
			lines[i] = pad + ln
		}
	}
	return strings.Join(lines, "\n")
}
