package config

import (
	"fmt"
	"strings"
)

// Resolver supplies values for the ${...} interpolation grammar (spec 01).
// It is injected so the same grammar resolves against the workspace service
// graph at generate time without coupling this package to internal/workspace.
type Resolver interface {
	// Env returns a host environment variable.
	Env(name string) (string, bool)
	// Self returns an attribute of the current service (e.g. "host").
	Self(attr string) (string, bool)
	// Ref resolves an explicit cross-service reference path such as
	// "workspace.shared.postgres.host".
	Ref(path string) (string, error)
	// Profile is the active env-overlay profile (e.g. "dev").
	Profile() string
	// WorkspaceName is the workspace name.
	WorkspaceName() string
}

// Interpolate expands the typed ${...} grammar in s using r, returning an error
// on any unknown or unresolvable reference (StrictUndefined — missing params
// fail fast rather than silently emitting empty strings). The escape `$$`
// yields a literal `$`. This grammar is resolved by us — never the shell, never
// the template engine; shell-style ${VAR:-default} applies only inside
// generated files (spec 01).
func Interpolate(s string, r Resolver) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		// c == '$'
		if i+1 < len(s) && s[i+1] == '$' { // $$ -> literal $
			b.WriteByte('$')
			i += 2
			continue
		}
		if i+1 >= len(s) || s[i+1] != '{' { // a lone '$' is literal
			b.WriteByte('$')
			i++
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated interpolation: %q", s[i:])
		}
		expr := s[i+2 : i+2+end]
		val, err := resolveExpr(strings.TrimSpace(expr), r)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		i += 2 + end + 1
	}
	return b.String(), nil
}

// resolveExpr resolves a single interpolation expression (the text inside
// ${...}) against r.
func resolveExpr(expr string, r Resolver) (string, error) {
	switch {
	case expr == "profile":
		return r.Profile(), nil
	case expr == "workspace.name":
		return r.WorkspaceName(), nil
	case strings.HasPrefix(expr, "env."):
		name := expr[len("env."):]
		if name == "" {
			return "", fmt.Errorf("${env.} requires a variable name")
		}
		v, ok := r.Env(name)
		if !ok {
			return "", fmt.Errorf("${env.%s}: environment variable not set", name)
		}
		return v, nil
	case strings.HasPrefix(expr, "self."):
		attr := expr[len("self."):]
		if attr == "" {
			return "", fmt.Errorf("${self.} requires an attribute")
		}
		v, ok := r.Self(attr)
		if !ok {
			return "", fmt.Errorf("${self.%s}: unknown service attribute", attr)
		}
		return v, nil
	case strings.HasPrefix(expr, "ref:"):
		path := expr[len("ref:"):]
		if path == "" {
			return "", fmt.Errorf("${ref:} requires a reference path")
		}
		return r.Ref(path)
	default:
		return "", fmt.Errorf("unknown interpolation ${%s} (expected env./self./ref:/profile/workspace.name)", expr)
	}
}

// InterpolationRefs scans s and returns the reference paths used by any
// ${ref:...} expressions (for cross-ref validation before values are known).
// It tolerates malformed input by skipping it; validation of well-formedness is
// Interpolate's job.
func InterpolationRefs(s string) []string {
	var refs []string
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) || s[i+1] != '{' {
			i++
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			break
		}
		expr := strings.TrimSpace(s[i+2 : i+2+end])
		if path, ok := strings.CutPrefix(expr, "ref:"); ok && path != "" {
			refs = append(refs, path)
		}
		i += 2 + end + 1
	}
	return refs
}
