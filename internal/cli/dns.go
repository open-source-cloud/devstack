package cli

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/dns"
	"github.com/open-source-cloud/devstack/internal/proxy"
)

// newDnsCmd wires `dns setup|status|remove` — the marker-fenced /etc/hosts block
// for <service>.<project>.localhost (spec 05). Hostnames come from the proxy
// route table (network.proxy.engine: caddy). Writing /etc/hosts needs root; the
// command surfaces a clear sudo remediation.
func newDnsCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Manage the /etc/hosts entries for *.localhost service URLs",
	}
	cmd.AddCommand(newDnsSetupCmd(g), newDnsStatusCmd(g), newDnsRemoveCmd(g))
	return cmd
}

// dnsHosts returns the local hostnames the workspace's proxy routes resolve to.
func dnsHosts() ([]string, error) {
	m, err := loadWorkspace()
	if err != nil {
		return nil, err
	}
	routes := proxy.BuildRoutes(m)
	hosts := make([]string, 0, len(routes))
	for _, r := range routes {
		hosts = append(hosts, r.Host)
	}
	return hosts, nil
}

func newDnsSetupCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Write the devstack-managed /etc/hosts block (needs sudo)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := dnsHosts()
			if err != nil {
				return err
			}
			if len(hosts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no proxy routes (set network.proxy.engine: caddy and add service ports)")
				return nil
			}
			changed, err := dns.Apply(dns.DefaultHostsPath, hosts)
			if err != nil {
				return hostsPermError(err)
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"hosts": hosts, "changed": changed})
			}
			w := cmd.OutOrStdout()
			if changed {
				fmt.Fprintf(w, "updated %s with %d host(s)\n", dns.DefaultHostsPath, len(hosts))
			} else {
				fmt.Fprintln(w, "/etc/hosts already up to date")
			}
			return nil
		},
	}
}

func newDnsStatusCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which *.localhost entries are present/missing in /etc/hosts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := dnsHosts()
			if err != nil {
				return err
			}
			present, err := dns.Present(dns.DefaultHostsPath)
			if err != nil {
				return err
			}
			missing, err := dns.Missing(dns.DefaultHostsPath, hosts)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"present": present, "missing": missing})
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "present: %v\n", present)
			if len(missing) > 0 {
				fmt.Fprintf(w, "missing: %v  (run `devstack dns setup`)\n", missing)
			}
			return nil
		},
	}
}

func newDnsRemoveCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Remove the devstack-managed /etc/hosts block (needs sudo)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			changed, err := dns.Remove(dns.DefaultHostsPath)
			if err != nil {
				return hostsPermError(err)
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"changed": changed})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed devstack /etc/hosts block: changed=%v\n", changed)
			return nil
		},
	}
}

// hostsPermError maps a permission failure to the sudo remediation.
func hostsPermError(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w\nhint: editing %s needs root — re-run with `sudo`", err, dns.DefaultHostsPath)
	}
	return err
}
