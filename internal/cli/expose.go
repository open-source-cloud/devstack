package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
)

// newSharedExposeCmd wires `shared expose [services...]` — publish the shared
// engines on stable 127.0.0.1 host ports so GUI clients (DataGrip, a Redis/S3
// browser, the RabbitMQ UI) can connect. Opt-in and loopback-only; it never
// touches the deterministic generated compose (an up-time overlay). `--off`
// removes the publish and returns the stack to DNS-only.
func newSharedExposeCmd(g *GlobalOpts) *cobra.Command {
	var off bool
	cmd := &cobra.Command{
		Use:   "expose [services...]",
		Short: "Publish shared services on stable 127.0.0.1 ports for local GUI clients",
		Long: "Publish the shared engines on stable 127.0.0.1 host ports so host tools and GUI\n" +
			"clients (DataGrip, TablePlus, a Redis/S3 browser, the RabbitMQ management UI)\n" +
			"can reach them. Ports are ledger-allocated (stable across runs) and loopback-only.\n" +
			"With no arguments, every exposable shared service is published; name services to\n" +
			"scope it. `--off` removes the publish. The persist survives up/down.",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			if off {
				if err := orchestrate.UnexposeShared(cmd.Context(), d); err != nil {
					return err
				}
				if g.JSON {
					return writeJSON(cmd, map[string]any{"exposed": []any{}})
				}
				if !g.Quiet {
					fmt.Fprintln(cmd.OutOrStdout(), "shared services are DNS-only again (host ports removed)")
				}
				return nil
			}
			ports, err := orchestrate.ExposeShared(cmd.Context(), d, args)
			if err != nil {
				return err
			}
			return renderExposed(cmd, g, ports)
		},
	}
	cmd.Flags().BoolVar(&off, "off", false, "remove the host-port publish (back to DNS-only)")
	return cmd
}

// newSharedPortsCmd wires `shared ports` — the read-only projection of the
// currently-published host ports + connection strings (lock-free snapshot).
func newSharedPortsCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "ports",
		Short: "Show the published 127.0.0.1 host ports for shared services (and connection strings)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			ports, err := orchestrate.ExposedStatus(cmd.Context(), d)
			if err != nil {
				return err
			}
			if len(ports) == 0 && !g.JSON {
				fmt.Fprintln(cmd.OutOrStdout(), "no shared services exposed — run `devstack shared expose`")
				return nil
			}
			return renderExposed(cmd, g, ports)
		},
	}
}

// renderExposed prints the exposed-port projection as JSON or an aligned table.
func renderExposed(cmd *cobra.Command, g *GlobalOpts, ports []orchestrate.ExposedPort) error {
	if g.JSON {
		return writeJSON(cmd, map[string]any{"exposed": ports})
	}
	if g.Quiet {
		for _, p := range ports {
			if p.URL != "" {
				fmt.Fprintln(cmd.OutOrStdout(), p.URL)
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tPORT\tADDRESS\tCONNECT")
	for _, p := range ports {
		label := p.Alias
		if !p.Primary {
			label = p.Alias + " (" + p.Label + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t127.0.0.1:%d\t%s\n", label, p.Label, p.Port, p.URL)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	// A one-line reminder that per-project Postgres DBs use their own dev creds.
	for _, p := range ports {
		if p.Engine == "postgres" && p.Primary {
			fmt.Fprintf(cmd.OutOrStdout(),
				"\nper-project database: postgres://<project>:<project>@127.0.0.1:%d/<project>?sslmode=disable\n", p.Port)
			break
		}
	}
	return nil
}
