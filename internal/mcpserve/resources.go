package mcpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// URI scheme and prefixes for everything this server publishes.
const (
	uriDocsIndex = "devstack://docs/index"
	uriDocsPre   = "devstack://docs/"
	uriTemplate  = "devstack://template/{name}"
	uriTemplPre  = "devstack://template/"
	uriSchemaPre = "devstack://schema/"
	uriCommands  = "devstack://commands.json"
)

// registerResources publishes the read-only material a client can pull without
// spending a tool call: the documentation corpus, the real built-in templates,
// the config schemas and the command catalog.
func registerResources(s *mcp.Server, d Deps) {
	s.AddResource(&mcp.Resource{
		URI:         uriDocsIndex,
		Name:        "devstack documentation index",
		Description: "Every document in devstack's documentation corpus, with its slug, title and summary.",
		MIMEType:    "application/json",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		list, err := d.Docs.List()
		if err != nil {
			return nil, err
		}
		body, err := json.MarshalIndent(map[string]any{"docs": list}, "", "  ")
		if err != nil {
			return nil, err
		}
		return jsonResource(uriDocsIndex, body), nil
	})

	// One resource per document, so a client can browse rather than guess slugs.
	list, err := d.Docs.List()
	if err == nil {
		for _, doc := range list {
			uri := uriDocsPre + doc.Slug
			s.AddResource(&mcp.Resource{
				URI:         uri,
				Name:        doc.Title,
				Description: doc.Summary,
				MIMEType:    "text/markdown",
			}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				slug := strings.TrimPrefix(req.Params.URI, uriDocsPre)
				_, body, err := d.Docs.Read(slug)
				if err != nil {
					return nil, err
				}
				return textResource(req.Params.URI, "text/markdown", body), nil
			})
		}
	}

	// The single highest-value resource: the source of a REAL, currently-shipping
	// template. "Write me a template like postgres" should return the actual
	// postgres template, not a plausible-looking invention.
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: uriTemplate,
		Name:        "devstack template source",
		Description: "The template.yaml of a built-in devstack template, by name (postgres, redis, node.next, php.laravel.nginx, …). Read one before authoring your own.",
		MIMEType:    "text/yaml",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		name := strings.TrimPrefix(req.Params.URI, uriTemplPre)
		body, err := readTemplate(d.Templates(), name)
		if err != nil {
			return nil, err
		}
		return textResource(req.Params.URI, "text/yaml", body), nil
	})

	for _, kind := range d.SchemaKinds() {
		uri := uriSchemaPre + kind + ".json"
		s.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        "devstack " + kind + " schema",
			Description: "The published JSON Schema for " + schemaFileFor(kind) + ".",
			MIMEType:    "application/schema+json",
		}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			k := strings.TrimSuffix(strings.TrimPrefix(req.Params.URI, uriSchemaPre), ".json")
			body, err := d.Schema(k)
			if err != nil {
				return nil, err
			}
			return textResource(req.Params.URI, "application/schema+json", body), nil
		})
	}

	s.AddResource(&mcp.Resource{
		URI:         uriCommands,
		Name:        "devstack command catalog",
		Description: "Every devstack command as data: path, summary, argument spec and flags, derived from the running binary.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		body, err := d.Run(ctx, []string{"--json", "ai", "commands"})
		if err != nil {
			return nil, err
		}
		return jsonResource(uriCommands, body), nil
	})
}

// readTemplate finds a template's manifest in the built-in source.
func readTemplate(src fs.FS, name string) ([]byte, error) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return nil, fmt.Errorf("invalid template name %q", name)
	}
	body, err := fs.ReadFile(src, path.Join(name, "template.yaml"))
	if err != nil {
		return nil, fmt.Errorf("no built-in template %q (list them with the devstack_template_list tool)", name)
	}
	return body, nil
}

// schemaFileFor names the config file a schema kind describes, for the human
// description shown in a client's resource list.
func schemaFileFor(kind string) string {
	if kind == "workspace" {
		return "workspace.yaml"
	}
	return "devstack.yaml"
}

func textResource(uri, mime string, body []byte) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: mime, Text: string(body)}},
	}
}

func jsonResource(uri string, body []byte) *mcp.ReadResourceResult {
	return textResource(uri, "application/json", body)
}
