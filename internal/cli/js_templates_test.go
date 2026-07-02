package cli

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/template"
)

// TestJSTemplatesRenderAndValidate guards the built-in JS/monorepo app templates:
// each resolves through the embedded source, validates via compose-go, and emits
// the dev-mode hot-reload shape (a /app mount + a dev command).
func TestJSTemplatesRenderAndValidate(t *testing.T) {
	src := builtinSource()
	for _, name := range []string{
		"node.express", "node.nestjs", "node.next", "react.vite", "bun.app", "turborepo",
	} {
		res, err := template.Resolve(src, name, nil)
		if err != nil {
			t.Errorf("%s resolve: %v", name, err)
			continue
		}
		compose, err := generate.LintResolved(name, res)
		if err != nil {
			t.Errorf("%s lint: %v", name, err)
			continue
		}
		doc := string(compose)
		if !strings.Contains(doc, "/app") {
			t.Errorf("%s: missing /app mount for hot reload:\n%s", name, doc)
		}
		if !strings.Contains(doc, "dev") {
			t.Errorf("%s: missing a dev command", name)
		}
	}
}
