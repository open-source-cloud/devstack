package envingest

import (
	"fmt"

	"github.com/goccy/go-yaml/parser"
)

// SetEnv merges kv into a service's env.<block> (block = "raw" | "prefixed") in
// the raw devstack.yaml bytes, PRESERVING comments and key order (the env/block
// mapping nodes are created if absent). This is the comment-safe editor the
// `devstack env set` command uses. Returns the rewritten bytes.
func SetEnv(src []byte, service, block string, kv map[string]string) ([]byte, error) {
	f, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse devstack.yaml: %w", err)
	}
	if err := mergeEnvBlock(f, service, block, kv); err != nil {
		return nil, err
	}
	return []byte(f.String()), nil
}

// ReplaceEnvBlock replaces a service's entire env.<block> with kv (sorted). Used
// to remove keys (`devstack env unset`); comments on that block are not preserved,
// but the rest of the file is untouched. A no-op if the block does not exist.
func ReplaceEnvBlock(src []byte, service, block string, kv map[string]string) ([]byte, error) {
	f, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse devstack.yaml: %w", err)
	}
	path := fmt.Sprintf("$.services.%s.env.%s", yamlKey(service), block)
	if !pathExists(f, path) {
		return src, nil
	}
	body := "{}\n"
	if len(kv) > 0 {
		b, err := marshalSortedMap(kv)
		if err != nil {
			return nil, err
		}
		body = b
	}
	if err := replaceAt(f, path, body); err != nil {
		return nil, err
	}
	return []byte(f.String()), nil
}

// AppendProjectRef appends a project entry to workspace.yaml's projects: list,
// creating the list if absent. No-op-safe on formatting: it merges via the AST so
// the rest of the file (comments, order) is preserved.
func AppendProjectRef(src []byte, name, path, git string) ([]byte, error) {
	f, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse workspace.yaml: %w", err)
	}
	entry := fmt.Sprintf("- name: %s\n  path: %s\n", yamlScalar(name), yamlScalar(path))
	if git != "" {
		entry += fmt.Sprintf("  git: %s\n", yamlScalar(git))
	}
	if pathExists(f, "$.projects") {
		existing, err := nodeYAML(f, "$.projects")
		if err != nil {
			return nil, err
		}
		merged := trimRightNL(dedent(existing)) + "\n" + entry
		if err := replaceAt(f, "$.projects", merged); err != nil {
			return nil, err
		}
	} else if err := mergeRoot(f, "projects:\n"+entry); err != nil {
		return nil, err
	}
	return []byte(f.String()), nil
}

func trimRightNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
