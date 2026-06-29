package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/trust"
)

// newTrustCmd wires `trust install|uninstall|status` — the local CA via mkcert
// (spec 05). install/uninstall need privileges (sudo); status is read-only and
// prints the exact remediation for whatever is missing.
func newTrustCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage the local HTTPS CA (mkcert) for *.localhost",
	}
	cmd.AddCommand(
		newTrustStatusCmd(g),
		newTrustInstallCmd(g, true),
		newTrustInstallCmd(g, false),
	)
	return cmd
}

func newTrustStatusCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Diagnose local-CA trust readiness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := trust.New().Status(cmd.Context())
			if g.JSON {
				return writeJSON(cmd, s)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "mkcert:   %s\n", okmark(s.MkcertFound))
			fmt.Fprintf(w, "CA:       %s\n", okmark(s.CAInstalled))
			fmt.Fprintf(w, "Firefox:  %s (certutil)\n", okmark(s.FirefoxTrust))
			if s.CARoot != "" {
				fmt.Fprintf(w, "CAROOT:   %s\n", s.CARoot)
			}
			if s.Remediation != "" {
				fmt.Fprintf(w, "\n→ %s\n", s.Remediation)
			} else {
				fmt.Fprintln(w, "\nlocal HTTPS trust is ready")
			}
			return nil
		},
	}
}

// newTrustInstallCmd builds either `install` (install=true) or `uninstall`.
func newTrustInstallCmd(g *GlobalOpts, install bool) *cobra.Command {
	use, short := "uninstall", "Remove the local root CA from trust stores (needs sudo)"
	if install {
		use, short = "install", "Create + trust the local root CA in system/NSS stores (needs sudo)"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tr := trust.New()
			var err error
			if install {
				err = tr.Install(cmd.Context())
			} else {
				err = tr.Uninstall(cmd.Context())
			}
			if err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "trust %s: ok\n", use)
			}
			return nil
		},
	}
}

func okmark(ok bool) string {
	if ok {
		return "ok"
	}
	return "MISSING"
}
