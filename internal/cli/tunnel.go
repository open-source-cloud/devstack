package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/tunnel"
)

// newTunnelCmd wires `tunnel login|create|route` — the account-gated cloudflared
// setup verbs (spec 05). The tunnel is default-down; bringing it up as a managed
// container (ingress rendered from the proxy routes) is wired into the saga (N5).
func newTunnelCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel",
		Short: "Optional public tunnel via cloudflared (account-gated, default down)",
	}
	cmd.AddCommand(newTunnelLoginCmd(g), newTunnelCreateCmd(g), newTunnelRouteCmd(g))
	return cmd
}

func newTunnelLoginCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Authenticate cloudflared with your Cloudflare account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := tunnel.New().Login(cmd.Context()); err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintln(cmd.OutOrStdout(), "cloudflared login ok")
			}
			return nil
		},
	}
}

func newTunnelCreateCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a named tunnel (writes its credentials file)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tunnel.New().Create(cmd.Context(), args[0]); err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "tunnel %q created\n", args[0])
			}
			return nil
		},
	}
}

func newTunnelRouteCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "route <name> <hostname>",
		Short: "Route a (non-wildcard) hostname to the tunnel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tunnel.New().RouteDNS(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "routed %s → tunnel %q\n", args[1], args[0])
			}
			return nil
		},
	}
}
