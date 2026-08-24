package cli

import (
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/template"
)

// TestAppTemplatesRenderAndValidate guards the per-language app templates the way
// TestJSTemplatesRenderAndValidate guards the JS ones: each must resolve, validate
// through compose-go, bind-mount the source, and carry a real reload command.
func TestAppTemplatesRenderAndValidate(t *testing.T) {
	src := builtinSource()
	for _, name := range []string{
		"go.app", "rust.app",
		"python.app", "python.fastapi", "python.django", "python.flask",
		"elixir.phoenix", "ruby.rails", "java.spring", "dotnet.aspnet", "deno.app",
		"vue.vite", "svelte.kit", "nuxt", "astro",
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
			t.Errorf("%s: missing the /app source mount that hot reload depends on:\n%s", name, doc)
		}
		// An app template must never declare provides: — that is what marks a
		// template usable as shared infrastructure.
		if res.Provides != "" {
			t.Errorf("%s declares provides:%q, but app templates are per-project", name, res.Provides)
		}
	}
}

// TestAppTemplatesBindAllInterfaces is the single highest-value assertion here.
//
// A dev server bound to 127.0.0.1 inside a container is unreachable from the
// shared network AND from a published host port, and it fails in the most
// confusing way possible: the container is healthy, the port is published, and
// every connection is refused. Each of these frameworks defaults to loopback, so
// the template has to override it explicitly.
func TestAppTemplatesBindAllInterfaces(t *testing.T) {
	src := builtinSource()
	// name -> a literal proving the template forces an all-interfaces bind.
	cases := map[string]string{
		"go.app":         "0.0.0.0",        // HOST/ADDR env the user's code reads
		"python.fastapi": "--host 0.0.0.0", // uvicorn defaults to 127.0.0.1
		"python.django":  "0.0.0.0:",       // bare runserver binds 127.0.0.1
		"python.flask":   "--host 0.0.0.0", // flask run defaults to 127.0.0.1
		"ruby.rails":     "-b 0.0.0.0",     // rails server binds localhost in dev
		"dotnet.aspnet":  "http://+:",      // ASPNETCORE_URLS, plus --no-launch-profile
		"vue.vite":       "--host 0.0.0.0", // vite server.host defaults to localhost
		"svelte.kit":     "--host 0.0.0.0", //
		"nuxt":           "--host 0.0.0.0", //
		"astro":          "--host 0.0.0.0", // astro server.host defaults to false
		"deno.app":       "0.0.0.0",        // Deno.serve already defaults to 0.0.0.0
	}
	for name, want := range cases {
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
		if !strings.Contains(string(compose), want) {
			t.Errorf("%s: expected %q so the dev server binds all interfaces; without it the "+
				"container looks healthy but refuses every connection:\n%s", name, want, compose)
		}
	}
}

// TestAppTemplatesSurviveBindMountShadowing checks the dependency directories that
// a `..:/app` bind mount would otherwise hide. Getting this wrong means the image
// built dependencies that the running container then cannot see.
func TestAppTemplatesSurviveBindMountShadowing(t *testing.T) {
	src := builtinSource()
	cases := map[string][]string{
		// The bind mount clobbers whatever the build put at these paths, so each
		// needs its own volume.
		"elixir.phoenix": {"/app/deps", "/app/_build"},
		"dotnet.aspnet":  {"/app/obj", "/app/bin"},
		"vue.vite":       {"/app/node_modules"},
		"svelte.kit":     {"/app/node_modules"},
		"nuxt":           {"/app/node_modules"},
		"astro":          {"/app/node_modules"},
	}
	for name, paths := range cases {
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
		for _, p := range paths {
			if !strings.Contains(string(compose), p) {
				t.Errorf("%s: %s is not shielded from the bind mount, so the container "+
					"cannot see what the image build installed there", name, p)
			}
		}
	}
}
