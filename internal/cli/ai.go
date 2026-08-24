package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/aidocs"
)

// newAiCmd wires `devstack ai` — the AI-agent integration surface (spec 32).
//
// The group serves one embedded corpus through the surfaces different agents can
// reach: `ai docs` for an agent that only has a shell, `ai mcp` for MCP clients,
// and `ai install` for the skills/AGENTS.md files a repo commits. Everything here
// is read-only with respect to the ledger and the shared stack; nothing in this
// group takes the flock.
func newAiCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "Teach AI coding agents to use devstack (docs, MCP, skills)",
		Long: "ai exposes devstack to AI coding agents.\n\n" +
			"  docs      read the documentation corpus compiled into this binary\n" +
			"  commands  the whole command surface as machine-readable data\n\n" +
			"The corpus is the same markdown that renders in the repository, so an agent\n" +
			"reads exactly what a human would — there is no separately-authored, separately-\n" +
			"drifting set of \"agent docs\".",
	}
	cmd.AddCommand(newAiInstallCmd(g), newAiCheckCmd(g), newAiMcpCmd(g), newAiDocsCmd(g), newAiCommandsCmd(g))
	return cmd
}

// newAiDocsCmd wires `ai docs` — list the corpus, print one document, or search.
//
// This is the retrieval channel for agents that have a shell but no MCP client,
// which is most of them. It deliberately prints raw markdown: the caller is a
// model, and markdown is what it reads best.
func newAiDocsCmd(g *GlobalOpts) *cobra.Command {
	var (
		search  string
		section string
		limit   int
	)
	cmd := &cobra.Command{
		Use:   "docs [slug]",
		Short: "Read devstack's documentation from inside the binary",
		Long: "docs lists the embedded documentation corpus, prints one document, or searches it.\n\n" +
			"  devstack ai docs                     list every document with a one-line summary\n" +
			"  devstack ai docs guide/templates     print that document as markdown\n" +
			"  devstack ai docs 23                  specs are addressable by number\n" +
			"  devstack ai docs --search \"lint\"     search titles and bodies\n\n" +
			"Slugs mirror the repository layout: guide/<page>, specs/<nn>-<name>, and the\n" +
			"lowercased name of each top-level document (architecture, decisions, roadmap…).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case len(args) == 1:
				return runAiDocsShow(cmd, g, args[0])
			case search != "":
				return runAiDocsSearch(cmd, g, search, limit)
			default:
				return runAiDocsList(cmd, g, section)
			}
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "search titles and bodies instead of listing")
	cmd.Flags().StringVar(&section, "section", "", "limit the listing to one section (guide|root|specs)")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum search results")
	return cmd
}

func runAiDocsShow(cmd *cobra.Command, g *GlobalOpts, slug string) error {
	doc, body, err := aidocs.Read(slug)
	if err != nil {
		return err
	}
	if g.JSON {
		return writeJSON(cmd, map[string]any{"doc": doc, "body": string(body)})
	}
	if g.Quiet {
		return nil
	}
	_, err = cmd.OutOrStdout().Write(body)
	return err
}

func runAiDocsList(cmd *cobra.Command, g *GlobalOpts, section string) error {
	list, err := aidocs.List()
	if err != nil {
		return err
	}
	if section != "" {
		want := aidocs.Section(strings.ToLower(section))
		filtered := list[:0:0]
		for _, d := range list {
			if d.Section == want {
				filtered = append(filtered, d)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("unknown section %q (available: guide, root, specs)", section)
		}
		list = filtered
	}
	if g.JSON {
		return writeJSON(cmd, map[string]any{"docs": list})
	}
	if g.Quiet {
		for _, d := range list {
			fmt.Fprintln(cmd.OutOrStdout(), d.Slug)
		}
		return nil
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	var current aidocs.Section
	for _, d := range list {
		if d.Section != current {
			if current != "" {
				fmt.Fprintln(w)
			}
			current = d.Section
			fmt.Fprintf(w, "%s\n", strings.ToUpper(string(current)))
		}
		fmt.Fprintf(w, "  %s\t%s\n", d.Slug, truncate(d.Title, 60))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n%d documents. Read one with `%s ai docs <slug>`.\n",
		len(list), rootName(cmd))
	return nil
}

func runAiDocsSearch(cmd *cobra.Command, g *GlobalOpts, query string, limit int) error {
	hits, err := aidocs.Search(query, limit)
	if err != nil {
		return err
	}
	if g.JSON {
		return writeJSON(cmd, map[string]any{"query": query, "hits": hits})
	}
	if g.Quiet {
		for _, h := range hits {
			fmt.Fprintln(cmd.OutOrStdout(), h.Doc.Slug)
		}
		return nil
	}
	out := cmd.OutOrStdout()
	if len(hits) == 0 {
		fmt.Fprintf(out, "no documents match %q\n", query)
		return nil
	}
	for _, h := range hits {
		fmt.Fprintf(out, "%s — %s\n", h.Doc.Slug, h.Doc.Title)
		for _, m := range h.Matches {
			fmt.Fprintf(out, "    %s\n", truncate(m, 100))
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "Read one with `%s ai docs <slug>`.\n", rootName(cmd))
	return nil
}

// truncate shortens a string for column output, using a single-rune ellipsis so
// the width math stays predictable.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
