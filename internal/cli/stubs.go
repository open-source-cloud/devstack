package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// stub builds a not-yet-implemented command that documents its target milestone.
// It returns a clear notice (exit 0) so the surface is explorable during M0.
func stub(use, short, milestone string, children ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
	}
	if len(children) == 0 {
		cmd.RunE = func(c *cobra.Command, _ []string) error {
			fmt.Fprintf(c.ErrOrStderr(), "`%s %s` is planned for %s and is not implemented yet.\n", rootName(c), c.Name(), milestone)
			return nil
		}
	} else {
		cmd.AddCommand(children...)
	}
	return cmd
}

func rootName(c *cobra.Command) string { return c.Root().Name() }

// addStubCommands wires the remaining command surface from spec 07. doctor,
// alias and version are real; everything else is a milestone-tagged placeholder.
func addStubCommands(root *cobra.Command, _ *GlobalOpts) {
	root.AddCommand(
		stub("shell", "Open a shell in a service container", "M2"),
		stub("logs", "Stream service logs", "M2"),
	)
}
