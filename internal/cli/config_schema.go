package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
)

// newConfigSchemaCmd wires `config schema` — it prints the published JSON Schema
// for a config file kind (spec 01, DECISIONS D16). The schemas are hand-authored
// and embedded, so what this prints is byte-identical to what the $schema URL in
// the generated editor configs resolves to.
//
// It takes no workspace: the schema describes the file format, not any particular
// workspace, so `config schema` works in an empty directory — which is exactly
// when someone (or an agent) is about to author their first devstack.yaml.
func newConfigSchemaCmd(_ *GlobalOpts) *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema for a config file kind",
		Long: "schema prints the published draft-2020-12 JSON Schema for devstack.yaml or\n" +
			"workspace.yaml. Point an editor at it for completion and inline validation\n" +
			"(the generated .vscode/settings.json already does), or feed it to a tool that\n" +
			"needs the exact config contract.\n\n" +
			"The Go validator remains the source of truth; a CI round-trip test keeps the\n" +
			"schema and the structs aligned.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := config.ParseSchemaKind(kind)
			if err != nil {
				return err
			}
			doc, err := config.Schema(k)
			if err != nil {
				return err
			}
			// The document is already indented JSON with a trailing newline; write
			// it verbatim rather than through writeJSON so the bytes match the
			// committed file (and the published URL) exactly.
			_, err = cmd.OutOrStdout().Write(doc)
			return err
		},
	}
	cmd.Flags().StringVar(&kind, "kind", string(config.SchemaProject),
		fmt.Sprintf("config file kind to describe (%s)", strings.Join(schemaKindNames(), "|")))
	return cmd
}

func schemaKindNames() []string {
	kinds := config.SchemaKinds()
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}
