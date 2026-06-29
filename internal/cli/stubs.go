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
		stub("secrets", "Secrets providers (login, keygen)", "M4",
			stub("login", "Authenticate a secrets provider", "M4"),
			stub("keygen", "Generate an age/SOPS key", "M4"),
		),
		stub("trust", "Local CA trust (install/uninstall/status)", "M5",
			stub("install", "Install the local root CA into trust stores", "M5"),
			stub("uninstall", "Remove the local root CA from trust stores", "M5"),
			stub("status", "Show local CA trust status", "M5"),
		),
		stub("dns", "Local DNS helpers (*.localhost)", "M5",
			stub("setup", "Configure local DNS resolution", "M5"),
		),
		stub("tunnel", "Optional public tunnel via cloudflared", "M5",
			stub("login", "Authenticate cloudflared", "M5"),
			stub("create", "Create a named tunnel", "M5"),
			stub("route", "Route DNS to the tunnel", "M5"),
			stub("up", "Bring the tunnel up", "M5"),
			stub("down", "Bring the tunnel down", "M5"),
		),
		stub("import", "Import an old devdock project.yaml into workspace.yaml + devstack.yaml", "M1"),
		stub("workspace", "Workspace-level lifecycle", "M6",
			stub("destroy", "Reverse ALL machine-global artifacts for this workspace", "M6"),
		),
	)
}
