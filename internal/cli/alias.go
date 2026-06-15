package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devdock-go/internal/alias"
)

func newAliasCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias",
		Short: "Manage alternate command names for this binary (e.g. rq, uranus)",
		Long: "One binary can be invoked under multiple names via argv[0] dispatch. Each alias\n" +
			"symlinks this binary into XDG_BIN_HOME and runs the identical command tree with\n" +
			"per-alias branding.",
	}
	cmd.AddCommand(newAliasAddCmd(g), newAliasRemoveCmd(g), newAliasListCmd(g))
	return cmd
}

func newAliasAddCmd(_ *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "add <name>",
		Short: "Install an alias symlink in XDG_BIN_HOME",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			link, err := alias.Add(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed alias %q → %s\n", args[0], link)
			return nil
		},
	}
}

func newAliasRemoveCmd(_ *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove an alias symlink and registry entry",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := alias.Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed alias %q\n", args[0])
			return nil
		},
	}
}

func newAliasListCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed aliases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := alias.Load()
			if err != nil {
				return err
			}
			if len(reg.Aliases) == 0 {
				if !g.Quiet {
					fmt.Fprintln(cmd.OutOrStdout(), "no aliases installed")
				}
				return nil
			}
			for _, a := range reg.Aliases {
				fmt.Fprintln(cmd.OutOrStdout(), a)
			}
			return nil
		},
	}
}
