package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/selfupdate"
	"github.com/open-source-cloud/devstack/internal/version"
)

// newSelfCmd wires `self update|check` — version checking and self-update
// (spec 14). Checksum-verified, install-method-aware, atomic in-place replace.
func newSelfCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "self",
		Short: "Manage this binary (check for and install updates)",
	}
	cmd.AddCommand(newSelfCheckCmd(g), newSelfUpdateCmd(g))
	return cmd
}

func newSelfCheckCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check whether a newer release is available",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := selfupdate.Check(cmd.Context(), version.Version)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, res)
			}
			w := cmd.OutOrStdout()
			switch {
			case res.Available:
				fmt.Fprintf(w, "a new release is available: %s → %s\n", res.Current, res.Latest)
				fmt.Fprintf(w, "run `%s self update` to install it\n", rootName(cmd))
			case res.Dev:
				fmt.Fprintf(w, "running a development build (%s); the latest release is %s\n", res.Current, res.Latest)
				fmt.Fprintf(w, "run `%s self update` to install the release build\n", rootName(cmd))
			default:
				fmt.Fprintf(w, "up to date (%s is the latest release)\n", res.Current)
			}
			return nil
		},
	}
}

func newSelfUpdateCmd(g *GlobalOpts) *cobra.Command {
	var (
		check      bool
		pin        string
		force      bool
		skipVerify bool
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download and install the latest release (refuses on package-managed installs)",
		Long: "update replaces this binary with the latest GitHub release for your OS/arch,\n" +
			"verifying its cosign keyless signature and SHA-256 checksum, then replacing it\n" +
			"atomically. Signature verification requires the `cosign` binary and is ON by\n" +
			"default; pass --insecure-skip-verify to bypass it (the SHA-256 checksum is still\n" +
			"enforced). Homebrew/dpkg/rpm installs are refused with the right upgrade command.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if check {
				return newSelfCheckCmd(g).RunE(cmd, nil)
			}
			res, err := selfupdate.Update(cmd.Context(), version.Version, selfupdate.Options{Version: pin, Force: force, SkipVerify: skipVerify})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if !res.Install.CanSelfReplace() {
				// A package-managed (or non-writable) install: print the exact
				// remediation and exit 0 — this is a correct outcome, not an error.
				fmt.Fprintf(w, "%s\n", res.Install.Hint)
				if g.JSON {
					return writeJSON(cmd, res)
				}
				return nil
			}
			if res.UpToDate {
				if g.JSON {
					return writeJSON(cmd, res)
				}
				fmt.Fprintf(w, "already up to date (%s)\n", res.From)
				return nil
			}
			if g.JSON {
				return writeJSON(cmd, res)
			}
			fmt.Fprintf(w, "updated %s → %s\n", res.From, res.To)
			if res.Verified {
				fmt.Fprintln(w, "(verified by cosign keyless signature + SHA-256 checksum)")
			} else {
				fmt.Fprintf(w, "(%s)\n", res.VerifyNote)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check for a newer version; do not install")
	cmd.Flags().StringVar(&pin, "version", "", "install a specific release tag (e.g. v0.2.0)")
	cmd.Flags().BoolVar(&force, "force", false, "re-install even when already up to date (still refuses package-managed installs)")
	cmd.Flags().BoolVar(&skipVerify, "insecure-skip-verify", false, "bypass cosign release-signature verification (SHA-256 checksum is still enforced)")
	return cmd
}
