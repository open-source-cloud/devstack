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
// clear notice). `shell` has GRADUATED to a real command (spec 26); `logs` stays a
// stub, re-tagged to v2 (its full read-only-SDK design is owned by spec 16). `db`
// has GRADUATED to a real command group (spec 29, see db.go).
func addStubCommands(root *cobra.Command, _ *GlobalOpts) {
	root.AddCommand(
		stub("logs", "Stream service logs", "v2 (spec 16)"),
		stub("dashboard", "Live TUI cockpit", "v2 (spec 16)"),
		stub("ide", "Generate devcontainer/.code-workspace/launch configs", "v2 (spec 17)"),
		stub("telemetry", "Opt-in usage telemetry (default OFF)", "a later release (spec 20)"),
	)
}
