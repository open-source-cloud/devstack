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

// addStubCommands reserves the post-1.0 command surface from spec 07 as
// milestone-tagged placeholders so `--help`/completions stay consistent (exit 0,
// clear notice). `shell` has GRADUATED to a real command (spec 26); `logs` and
// `dashboard` have GRADUATED to real commands (spec 16, see logs.go/dashboard.go).
// `db` has GRADUATED to a real command group (spec 29, see db.go).
func addStubCommands(root *cobra.Command, _ *GlobalOpts) {
	root.AddCommand(
		stub("ide", "Generate devcontainer/.code-workspace/launch configs", "v2 (spec 17)"),
		stub("telemetry", "Opt-in usage telemetry (default OFF)", "a later release (spec 20)"),
	)
}
