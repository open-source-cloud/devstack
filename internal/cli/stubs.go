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
// clear notice). Every verb that once lived here has GRADUATED to a real command:
// `shell` (spec 26), `logs`/`dashboard` (spec 16), `telemetry` (spec 20), `ide`
// (spec 17), and the `db` group (spec 15/29). None remain in this root list; the
// `stub` helper still backs the `db reset`/`db pull` and template-registry
// (spec 19) placeholders. Kept as a no-op seam for the next reserved verb.
func addStubCommands(root *cobra.Command, _ *GlobalOpts) {
	root.AddCommand()
}
