package envingest

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// rewriteProjectEnv rewrites the target service's env block in src (raw
// devstack.yaml bytes), inserting each decision's emitted value as a literal
// (config) or secret:// ref (secret). It is comment/order preserving: existing
// keys keep their inline comments; the apiVersion header and key order survive.
// Secrets route to env.prefixed when prefixed is true, config keys always to
// env.raw. Returns the rewritten bytes.
func rewriteProjectEnv(src []byte, service string, decisions []Decision, prefixed bool) ([]byte, error) {
	f, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse devstack.yaml: %w", err)
	}

	raw := map[string]string{}  // KEY -> emitted yaml-literal value
	pref := map[string]string{} // KEY -> emitted yaml-literal value (prefixed block)
	for _, d := range decisions {
		if d.IsSecret() && prefixed {
			pref[d.Key] = d.Ref
		} else {
			raw[d.Key] = d.Ref
		}
	}

	if len(raw) > 0 {
		if err := mergeEnvBlock(f, service, "raw", raw); err != nil {
			return nil, err
		}
	}
	if len(pref) > 0 {
		if err := mergeEnvBlock(f, service, "prefixed", pref); err != nil {
			return nil, err
		}
	}
	return []byte(f.String()), nil
}

// mergeEnvBlock merges kv into $.services.<svc>.env.<block> (block = raw |
// prefixed), creating the env / block mapping nodes when absent. The merge
// preserves existing keys' comments and only adds/overwrites the supplied keys.
func mergeEnvBlock(f *ast.File, service, block string, kv map[string]string) error {
	body, err := marshalSortedMap(kv)
	if err != nil {
		return err
	}
	blockPath := fmt.Sprintf("$.services.%s.env.%s", yamlKey(service), block)
	envPath := fmt.Sprintf("$.services.%s.env", yamlKey(service))
	svcPath := fmt.Sprintf("$.services.%s", yamlKey(service))

	switch {
	case pathExists(f, blockPath):
		return mergeAt(f, blockPath, body)
	case pathExists(f, envPath):
		return mergeAt(f, envPath, indentBlock(block, body))
	case pathExists(f, svcPath):
		return mergeAt(f, svcPath, indentBlock("env", indentBlock(block, body)))
	default:
		return fmt.Errorf("service %q not found in devstack.yaml", service)
	}
}

// mergeAt merges the YAML fragment body into the node at path in f.
func mergeAt(f *ast.File, path, body string) error {
	p, err := yaml.PathString(path)
	if err != nil {
		return fmt.Errorf("path %q: %w", path, err)
	}
	if err := p.MergeFromReader(f, strings.NewReader(body)); err != nil {
		return fmt.Errorf("merge into %q: %w", path, err)
	}
	return nil
}

// scaffoldProvider appends a secrets provider entry (name/kind[/env]) to
// workspace.yaml bytes when no provider with that name is declared. Returns the
// (possibly unchanged) bytes and whether a change was made.
func scaffoldProvider(src []byte, name, kind, env string) ([]byte, bool, error) {
	f, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, false, fmt.Errorf("parse workspace.yaml: %w", err)
	}

	entry := fmt.Sprintf("- name: %s\n  kind: %s\n", yamlScalar(name), yamlScalar(kind))
	if env != "" {
		entry += fmt.Sprintf("  env: %s\n", yamlScalar(env))
	}

	switch {
	case pathExists(f, "$.secrets.providers"):
		// Append by merging the existing list with the new entry.
		existing, err := nodeYAML(f, "$.secrets.providers")
		if err != nil {
			return nil, false, err
		}
		merged := strings.TrimRight(dedent(existing), "\n") + "\n" + entry
		if err := replaceAt(f, "$.secrets.providers", merged); err != nil {
			return nil, false, err
		}
	case pathExists(f, "$.secrets"):
		if err := mergeAt(f, "$.secrets", "providers:\n"+entry); err != nil {
			return nil, false, err
		}
	default:
		if err := mergeRoot(f, "secrets:\n  providers:\n"+indentLines(entry, "  ")); err != nil {
			return nil, false, err
		}
	}
	return []byte(f.String()), true, nil
}

// replaceAt replaces the node at path with the YAML fragment body.
func replaceAt(f *ast.File, path, body string) error {
	p, err := yaml.PathString(path)
	if err != nil {
		return fmt.Errorf("path %q: %w", path, err)
	}
	if err := p.ReplaceWithReader(f, strings.NewReader(body)); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}

// mergeRoot merges a top-level YAML fragment into the document root.
func mergeRoot(f *ast.File, body string) error {
	p, err := yaml.PathString("$")
	if err != nil {
		return err
	}
	if err := p.MergeFromReader(f, strings.NewReader(body)); err != nil {
		return fmt.Errorf("merge at root: %w", err)
	}
	return nil
}

// nodeYAML returns the YAML text of the node at path.
func nodeYAML(f *ast.File, path string) (string, error) {
	p, err := yaml.PathString(path)
	if err != nil {
		return "", err
	}
	n, err := p.FilterFile(f)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

// pathExists reports whether path resolves to a node in f.
func pathExists(f *ast.File, path string) bool {
	p, err := yaml.PathString(path)
	if err != nil {
		return false
	}
	n, err := p.FilterFile(f)
	return err == nil && n != nil
}

// marshalSortedMap renders kv as a deterministic, sorted YAML mapping using an
// ordered goccy MapSlice (never a Go map, which randomizes order).
func marshalSortedMap(kv map[string]string) (string, error) {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ms := make(yaml.MapSlice, 0, len(keys))
	for _, k := range keys {
		ms = append(ms, yaml.MapItem{Key: k, Value: kv[k]})
	}
	b, err := yaml.Marshal(ms)
	if err != nil {
		return "", fmt.Errorf("marshal env map: %w", err)
	}
	return string(b), nil
}

// indentBlock wraps body under a `key:` mapping, indenting body by two spaces.
func indentBlock(key, body string) string {
	return key + ":\n" + indentLines(body, "  ")
}

// dedent removes the minimal common leading-whitespace prefix from every
// non-empty line, normalizing a goccy-filtered node back to column 0 so it can be
// re-rendered at a new indentation.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if min == -1 || n < min {
			min = n
		}
	}
	if min <= 0 {
		return s
	}
	for i, l := range lines {
		if len(l) >= min {
			lines[i] = l[min:]
		}
	}
	return strings.Join(lines, "\n")
}

// indentLines prefixes every non-empty line of s with indent.
func indentLines(s, indent string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// yamlKey quotes a YAML path segment when needed (path syntax uses '.').
func yamlKey(s string) string { return s }

// yamlScalar renders a scalar value, quoting when needed for safety.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ":#{}[],&*!|>'\"%@` ") {
		return strconv.Quote(s)
	}
	return s
}
