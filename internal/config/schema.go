package config

import (
	"fmt"
	"sort"

	"github.com/open-source-cloud/devstack/schemas"
)

// SchemaKind names one published JSON Schema document. The values double as the
// `devstack config schema --kind` flag vocabulary and as the MCP resource slugs.
type SchemaKind string

// The schema kinds devstack publishes. Store config (~/.devstack/config.yaml) is
// deliberately absent: it is machine-global tool state, not a file users author
// by hand, and `store show` is its inspection surface.
const (
	SchemaProject   SchemaKind = "project"   // devstack.yaml
	SchemaWorkspace SchemaKind = "workspace" // workspace.yaml
)

// schemaFiles maps each kind to its embedded document.
var schemaFiles = map[SchemaKind]string{
	SchemaProject:   "devstack.schema.json",
	SchemaWorkspace: "workspace.schema.json",
}

// SchemaKinds returns the published kinds in stable order.
func SchemaKinds() []SchemaKind {
	out := make([]SchemaKind, 0, len(schemaFiles))
	for k := range schemaFiles {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// SchemaFilename returns the published file name for a kind (the basename under
// schemas/ in the repo, and the last path segment of the published URL).
func SchemaFilename(kind SchemaKind) (string, bool) {
	name, ok := schemaFiles[kind]
	return name, ok
}

// Schema returns the raw JSON Schema document for a kind. The bytes are the
// committed file verbatim, so the printed output and the published URL can never
// disagree.
func Schema(kind SchemaKind) ([]byte, error) {
	name, ok := schemaFiles[kind]
	if !ok {
		return nil, fmt.Errorf("unknown schema kind %q (available: %s)", kind, joinKinds(SchemaKinds()))
	}
	b, err := schemas.FS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read embedded schema %s: %w", name, err)
	}
	return b, nil
}

// ParseSchemaKind resolves a user-supplied --kind value, accepting the file kind
// as well as the file name it is stored under so `--kind devstack.yaml` works.
func ParseSchemaKind(s string) (SchemaKind, error) {
	switch s {
	case "project", "devstack", "devstack.yaml":
		return SchemaProject, nil
	case "workspace", "workspace.yaml":
		return SchemaWorkspace, nil
	}
	return "", fmt.Errorf("unknown schema kind %q (available: %s)", s, joinKinds(SchemaKinds()))
}

func joinKinds(kinds []SchemaKind) string {
	out := ""
	for i, k := range kinds {
		if i > 0 {
			out += ", "
		}
		out += string(k)
	}
	return out
}
