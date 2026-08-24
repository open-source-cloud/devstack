package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/aidocs"
	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/mcpserve"
	"github.com/open-source-cloud/devstack/internal/version"
	"github.com/open-source-cloud/devstack/templates"
)

// newAiMcpCmd wires `ai mcp` — devstack as a Model Context Protocol server over
// stdio.
//
// Two properties are load-bearing:
//
// stdout purity. An MCP stdio server must write nothing to stdout but framed
// JSON-RPC. Every tool therefore runs its devstack command with output captured
// into a buffer, never the process's real stdout, and the command forces quiet
// mode so the self-update notifier and any human chatter stay silent. A test
// asserts that a session's stdout carries only protocol bytes.
//
// No daemon semantics. Each tool call builds a FRESH command tree and runs it to
// completion, exactly as a shell invocation would. The cross-process flock is
// taken and released inside that call, so this long-lived process never holds it
// and devstack stays the stateless CLI its architecture describes.
func newAiMcpCmd(g *GlobalOpts) *cobra.Command {
	var (
		readOnly         bool
		allowDestructive bool
	)
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve devstack to AI agents over the Model Context Protocol",
		Long: "mcp runs devstack as an MCP server on stdin/stdout, so an MCP-capable agent can\n" +
			"inspect and drive this workspace directly.\n\n" +
			"It exposes:\n" +
			"  tools      the devstack commands, run exactly as the CLI runs them\n" +
			"  resources  the documentation corpus, the built-in template sources, the config\n" +
			"             JSON Schemas, and the command catalog\n" +
			"  prompts    guided workflows (onboard-repo, add-service, write-template,\n" +
			"             debug-up-failure, migrate-from-compose)\n\n" +
			"Read and write tools are registered by default. Irreversible verbs — workspace\n" +
			"destroy, db drop, db reset — are ABSENT unless --allow-destructive, because MCP\n" +
			"has no terminal to confirm on. The secrets group is never exposed at any setting.\n\n" +
			"You normally do not run this by hand: `devstack ai install` registers it in\n" +
			".mcp.json and your agent starts it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Anything the process itself prints would corrupt the protocol
			// stream, so silence the human surface for this command.
			g.Quiet = true
			g.JSON = false

			deps, err := mcpDeps(cmd)
			if err != nil {
				return err
			}
			return mcpserve.Serve(cmd.Context(), deps, mcpserve.Options{
				ReadOnly:         readOnly,
				AllowDestructive: allowDestructive,
			})
		},
	}
	cmd.Flags().BoolVar(&readOnly, "read-only", false,
		"expose only tools that cannot change anything")
	cmd.Flags().BoolVar(&allowDestructive, "allow-destructive", false,
		"additionally expose irreversible verbs (workspace destroy, db drop, db reset)")
	return cmd
}

// mcpDeps wires the server against this binary's own command tree and embedded
// assets. It is the single place internal/cli and internal/mcpserve meet, which
// is what keeps the dependency one-directional.
func mcpDeps(cmd *cobra.Command) (mcpserve.Deps, error) {
	binary := rootName(cmd)
	return mcpserve.Deps{
		Version:     version.Version,
		Binary:      binary,
		Run:         runDevstackCaptured,
		Docs:        mcpDocs{},
		Templates:   func() fs.FS { return templates.FS },
		Schema:      mcpSchema,
		SchemaKinds: mcpSchemaKinds,
	}, nil
}

// runDevstackCaptured executes one devstack command line in-process and returns
// its stdout.
//
// A fresh root command is built per call so no flag state leaks between requests,
// which is the same isolation a separate process would give — and the same
// harness the CLI test suite already uses.
func runDevstackCaptured(ctx context.Context, argv []string) ([]byte, error) {
	root := NewRootCmd(Options{})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(nopReader{})
	root.SetArgs(argv)
	root.SilenceUsage = true
	root.SilenceErrors = true

	err := root.ExecuteContext(ctx)
	if err != nil {
		// devstack's errors already carry the command, exit code and a
		// remediation; append stderr so the model sees the whole picture.
		if errOut.Len() > 0 {
			return out.Bytes(), fmt.Errorf("%w\n%s", err, errOut.String())
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// nopReader stands in for stdin so a prompt can never block the server waiting
// for input that will never arrive.
type nopReader struct{}

func (nopReader) Read([]byte) (int, error) { return 0, io.EOF }

// mcpDocs adapts internal/aidocs to the narrow interface mcpserve declares, so
// that package does not depend on devstack's document model.
type mcpDocs struct{}

func (mcpDocs) List() ([]mcpserve.DocMeta, error) {
	list, err := aidocs.List()
	if err != nil {
		return nil, err
	}
	out := make([]mcpserve.DocMeta, 0, len(list))
	for _, d := range list {
		out = append(out, mcpserve.DocMeta{
			Slug:    d.Slug,
			Path:    d.Path,
			Title:   d.Title,
			Summary: d.Summary,
			Section: string(d.Section),
			Lines:   d.Lines,
		})
	}
	return out, nil
}

func (mcpDocs) Read(slug string) (mcpserve.DocMeta, []byte, error) {
	d, body, err := aidocs.Read(slug)
	if err != nil {
		return mcpserve.DocMeta{}, nil, err
	}
	return mcpserve.DocMeta{
		Slug: d.Slug, Path: d.Path, Title: d.Title,
		Summary: d.Summary, Section: string(d.Section), Lines: d.Lines,
	}, body, nil
}

func mcpSchema(kind string) ([]byte, error) {
	k, err := config.ParseSchemaKind(kind)
	if err != nil {
		return nil, err
	}
	return config.Schema(k)
}

func mcpSchemaKinds() []string {
	kinds := config.SchemaKinds()
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}
