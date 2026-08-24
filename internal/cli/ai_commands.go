package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/ai"
	"github.com/open-source-cloud/devstack/internal/version"
)

// newAiCommandsCmd wires `ai commands` — the whole command surface as data.
//
// It is derived from the live cobra tree at call time, so it can never describe a
// verb this binary does not have. That is deliberately the opposite of
// docs/guide/command-reference.md, which is hand-maintained; a drift test keeps
// the two honest against each other.
func newAiCommandsCmd(g *GlobalOpts) *cobra.Command {
	var runnableOnly bool
	cmd := &cobra.Command{
		Use:   "commands",
		Short: "List every command as machine-readable data",
		Long: "commands walks this binary's command tree and prints it as data: every path,\n" +
			"its summary, argument spec and local flags, plus the global flags once.\n\n" +
			"Derived from the live tree, so it always matches the binary you are running —\n" +
			"use it instead of scraping --help.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cat := ai.Catalog(cmd.Root(), version.Version)
			if runnableOnly {
				kept := cat.Commands[:0:0]
				for _, c := range cat.Commands {
					if c.Runnable {
						kept = append(kept, c)
					}
				}
				cat.Commands = kept
			}
			if g.JSON {
				return writeJSON(cmd, cat)
			}
			if g.Quiet {
				for _, c := range cat.Commands {
					fmt.Fprintln(cmd.OutOrStdout(), c.Path)
				}
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, c := range cat.Commands {
				name := c.Path
				if c.Args != "" {
					name += " " + c.Args
				}
				fmt.Fprintf(w, "  %s\t%s\n", name, truncate(c.Short, 70))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d commands. Full detail: `%s --json ai commands`.\n",
				len(cat.Commands), rootName(cmd))
			return nil
		},
	}
	cmd.Flags().BoolVar(&runnableOnly, "runnable", false, "omit group commands that only hold subcommands")
	return cmd
}

// aiCommandPaths returns the set of invocable command paths in a tree, including
// alias spellings, so a documentation drift test can check names in both
// directions without re-walking cobra itself.
func aiCommandPaths(root *cobra.Command, runnableOnly bool) map[string]bool {
	out := map[string]bool{}
	for _, c := range ai.Catalog(root, "").Commands {
		if runnableOnly && !c.Runnable {
			continue
		}
		out[c.Path] = true
		// An alias is invocable too, so a doc naming one is not drift.
		segs := strings.Split(c.Path, " ")
		for _, a := range c.Aliases {
			aliased := append([]string{}, segs...)
			aliased[len(aliased)-1] = a
			out[strings.Join(aliased, " ")] = true
		}
	}
	return out
}
