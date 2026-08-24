package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/templates"
)

// docRowRE pulls the leading backticked name out of a markdown table row.
var docRowRE = regexp.MustCompile("^\\|\\s*`([^`]+)`")

// repoFile reads a path relative to the repository root.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// documentedTemplates returns the template names a markdown file tabulates,
// limited to names that are actually built-in (the same files carry unrelated
// tables for template functions, flags and registry commands).
func documentedTemplates(doc string, builtin map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(doc, "\n") {
		m := docRowRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[1])
		if builtin[name] {
			out[name] = true
		}
	}
	return out
}

// TestBuiltinTemplatesAreDocumented keeps the two hand-maintained template tables
// honest against the embedded set.
//
// Both tables had silently gone stale before this test existed: templates/README.md
// was missing every spec-28 engine (kafka, nats, rabbitmq, localstack, ministack)
// and docs/guide/templates.md was missing the mysql/mariadb/mongodb/cassandra/
// arangodb batch. Adding a template without a row is now a test failure rather
// than something noticed months later.
func TestBuiltinTemplatesAreDocumented(t *testing.T) {
	src := template.NewFSSource(templates.FS)
	names := src.List()
	if len(names) < 40 {
		t.Fatalf("expected the full built-in set, got %d", len(names))
	}
	builtin := make(map[string]bool, len(names))
	for _, n := range names {
		builtin[n] = true
	}

	for _, f := range []string{"templates/README.md", "docs/guide/templates.md"} {
		documented := documentedTemplates(repoFile(t, f), builtin)
		var missing []string
		for _, n := range names {
			if !documented[n] {
				missing = append(missing, n)
			}
		}
		sort.Strings(missing)
		for _, n := range missing {
			t.Errorf("%s has no row for the built-in template %q", f, n)
		}
	}
}

// TestEveryBuiltinTemplateIsEmbedded catches the other half of the same mistake:
// a template directory that exists on disk but was never added to the go:embed
// allow-list in templates/embed.go is invisible to the binary, so it lints from a
// directory path and then does not exist for any real workspace.
func TestEveryBuiltinTemplateIsEmbedded(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}
	embedded := map[string]bool{}
	for _, n := range template.NewFSSource(templates.FS).List() {
		embedded[n] = true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !embedded[e.Name()] {
			t.Errorf("templates/%s exists on disk but is not in the go:embed list in "+
				"templates/embed.go, so the binary cannot see it", e.Name())
		}
	}
}
