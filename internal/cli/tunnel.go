package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/proxy"
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
	cmd.AddCommand(
		newTunnelLoginCmd(g),
		newTunnelCreateCmd(g),
		newTunnelRouteCmd(g),
		newTunnelUpCmd(g),
		newTunnelDownCmd(g),
	)
	return cmd
}

// newTunnelUpCmd wires `tunnel up [name] [--detach] [--allow-secrets]` (spec 05/26):
// bring the managed cloudflared container up against the shared stack, ingress
// rendered from the proxy []Route. Default-DOWN stays the default (this is the
// explicit opt-in). It REFUSES to expose a service whose env carries a non-local
// secret:// value unless --allow-secrets is given, printing the override hint.
func newTunnelUpCmd(g *GlobalOpts) *cobra.Command {
	var (
		detach       bool
		allowSecrets bool
	)
	cmd := &cobra.Command{
		Use:   "up [name]",
		Short: "Bring the managed cloudflared tunnel up (default is down)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			model, err := config.Load(cwd)
			if err != nil {
				return err
			}

			// Spec 05 guard: refuse to tunnel any service carrying a non-local
			// secret:// value (a public tunnel would expose a cloud secret's blast
			// radius) unless the operator explicitly overrides.
			if refs := nonLocalSecretRefs(model); len(refs) > 0 && !allowSecrets {
				return fmt.Errorf("refusing to open a public tunnel: these services carry non-local secret:// values: %v\n"+
					"a public tunnel would widen their exposure; pass --allow-secrets to override once you have reviewed them", refs)
			}

			routes := proxy.BuildRoutes(model)
			hostnames := make([]string, 0, len(routes))
			for _, r := range routes {
				hostnames = append(hostnames, r.Host)
			}

			name := model.Workspace.Name
			if len(args) == 1 {
				name = args[0]
			}

			// Render the ingress config (public reuses local routing — same []Route).
			tunnelDir := filepath.Join(model.Root, generate.GenDir, "tunnel")
			if err := os.MkdirAll(tunnelDir, 0o755); err != nil {
				return err
			}
			configPath := filepath.Join(tunnelDir, "config.yml")
			creds := ""
			if home, err := os.UserHomeDir(); err == nil {
				creds = filepath.Join(home, ".cloudflared")
			}
			ingress := tunnel.IngressConfig(name, filepath.Join("/home/nonroot/.cloudflared", name+".json"), caddyUpstream, hostnames)
			if err := os.WriteFile(configPath, []byte(ingress), 0o644); err != nil {
				return err
			}

			if err := tunnel.New().Up(cmd.Context(), tunnel.UpOptions{
				Name:       name,
				ConfigPath: configPath,
				CredsDir:   creds,
				Network:    generate.SharedNetwork,
				Detach:     detach,
			}); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"tunnel": name, "state": "up"})
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "tunnel %q up (%d route(s))\n", name, len(hostnames))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&detach, "detach", true, "run the tunnel container detached")
	cmd.Flags().BoolVar(&allowSecrets, "allow-secrets", false, "override the refusal to tunnel services carrying non-local secret:// values")
	return cmd
}

// newTunnelDownCmd wires `tunnel down` (spec 05/26): stop the managed tunnel
// container, leaving credentials and DNS routes intact (reversible).
func newTunnelDownCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop the managed cloudflared tunnel (credentials/routes preserved)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := tunnel.New().Down(cmd.Context()); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"tunnel": tunnel.ContainerName, "state": "down"})
			}
			if !g.Quiet {
				fmt.Fprintln(cmd.OutOrStdout(), "tunnel down")
			}
			return nil
		},
	}
}

// caddyUpstream is where cloudflared forwards public requests: the shared reverse
// proxy on the shared network, so public traffic reuses the exact local []Route
// (spec 05, no drift). The concrete proxy container wiring is spec-05 territory.
const caddyUpstream = "http://shared-caddy:80"

// nonLocalSecretRefs collects every non-local secret:// value referenced by any
// service env across the workspace — the refs a public tunnel must refuse to
// expose (spec 05). Locality is classified from the workspace's declared
// providers (an offline provider like sops+age is local; aws/infisical are not).
func nonLocalSecretRefs(m *config.Model) []string {
	var envValues []string
	for _, p := range m.Projects {
		for _, svc := range p.Services {
			for _, v := range svc.Env.Raw {
				envValues = append(envValues, v)
			}
			for _, v := range svc.Env.Prefixed {
				envValues = append(envValues, v)
			}
		}
	}
	return tunnel.SecretBearing(envValues, localProviderClassifier(m))
}

// localProviderClassifier returns a predicate telling whether a provider NAME (as
// used in a secret:// ref) resolves to an OFFLINE/local backend. It reads the
// workspace's declared providers: kind `sops` is local (offline age decryption);
// cloud kinds (aws-sm/aws-ssm/infisical) are not. An unknown provider is treated
// as non-local (fail safe — err on refusing).
func localProviderClassifier(m *config.Model) func(string) bool {
	kindByName := map[string]string{}
	for _, pr := range m.Workspace.Secrets.Providers {
		kindByName[pr.Name] = pr.Kind
	}
	localKinds := map[string]bool{"sops": true}
	return func(provider string) bool {
		kind, ok := kindByName[provider]
		if !ok {
			return false
		}
		return localKinds[kind]
	}
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
