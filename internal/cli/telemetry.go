package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/store"
	"github.com/open-source-cloud/devstack/internal/telemetry"
	"github.com/open-source-cloud/devstack/internal/version"
)

// newTelemetryCmd wires `telemetry status|enable|disable|show` — the opt-in,
// default-OFF usage-telemetry scaffold (spec 20). Consent is persisted in the
// global $DEVSTACK_HOME config.yaml (never committed, never in the ledger). This
// build is SHIP-EMPTY: no real endpoint is wired, so even when enabled the only
// sink is the no-op sink and nothing leaves the machine.
func newTelemetryCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Opt-in anonymous usage telemetry (default OFF)",
		Long: "Opt-in anonymous usage telemetry (spec 20).\n\n" +
			"Telemetry is DEFAULT OFF and strictly opt-in: nothing is collected or sent\n" +
			"until you run `telemetry enable`. This build is ship-empty — no network\n" +
			"endpoint is wired, so even when enabled nothing leaves your machine. Only\n" +
			"coarse, non-identifying counters would ever be sent (see `telemetry show`):\n" +
			"command name, flag names, ok/error outcome, an error category enum, a\n" +
			"bucketed duration, os/arch, whether WSL2, the tool version, and a random\n" +
			"install id. Never paths, repo names, secrets, env values, or error strings.",
	}
	cmd.AddCommand(
		newTelemetryStatusCmd(g),
		newTelemetryEnableCmd(g),
		newTelemetryDisableCmd(g),
		newTelemetryShowCmd(g),
	)
	return cmd
}

func newTelemetryStatusCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether telemetry is enabled and where it would send",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			consent, err := store.TelemetryConsent()
			if err != nil {
				return err
			}
			endpoint := telemetry.DefaultEndpoint
			if g.JSON {
				return writeJSON(cmd, map[string]any{
					"enabled":    consent.Enabled,
					"installId":  consent.InstallID,
					"consentAt":  consent.ConsentAt,
					"endpoint":   endpoint,
					"shipEmpty":  endpoint == "",
					"configPath": store.ConfigPath(),
				})
			}
			if g.Quiet {
				return nil
			}
			w := cmd.OutOrStdout()
			state := "disabled"
			if consent.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(w, "telemetry: %s (default OFF)\n", state)
			if endpoint == "" {
				fmt.Fprintf(w, "  endpoint: (none — ship-empty; nothing is ever sent)\n")
			} else {
				fmt.Fprintf(w, "  endpoint: %s\n", endpoint)
			}
			if consent.InstallID != "" {
				fmt.Fprintf(w, "  install id: %s\n", consent.InstallID)
			}
			fmt.Fprintf(w, "  config: %s\n", store.ConfigPath())
			fmt.Fprintf(w, "run `%s telemetry show` to see the exact event that would be sent\n", rootName(cmd))
			return nil
		},
	}
}

func newTelemetryEnableCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Opt in to anonymous usage telemetry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			consent, err := store.SetTelemetry(true)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"enabled": true, "installId": consent.InstallID})
			}
			if g.Quiet {
				return nil
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "telemetry enabled. install id: %s\n", consent.InstallID)
			if telemetry.DefaultEndpoint == "" {
				fmt.Fprintf(w, "note: this build is ship-empty — no endpoint is wired, so nothing is actually sent yet.\n")
			}
			fmt.Fprintf(w, "disable any time with `%s telemetry disable`.\n", rootName(cmd))
			return nil
		},
	}
}

func newTelemetryDisableCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Opt out of usage telemetry (honored on the next invocation)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := store.SetTelemetry(false); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"enabled": false})
			}
			if g.Quiet {
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "telemetry disabled.")
			return nil
		},
	}
}

func newTelemetryShowCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the exact event that would be sent (performs no network I/O)",
		Long: "Print, byte-for-byte, the exact event devstack would send for a synthetic\n" +
			"invocation. This is the trust primitive: it performs ZERO network I/O and\n" +
			"lets you verify nothing sensitive is collected. It is the exhaustive\n" +
			"allowlist — there is no free-form field that could carry a path or secret.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			consent, err := store.TelemetryConsent()
			if err != nil {
				return err
			}
			// A synthetic sample so `show` works whether or not telemetry is on.
			sample := telemetry.NewEvent(telemetry.EventInput{
				Command:     "up",
				Flags:       []string{"--build"},
				Err:         nil,
				Duration:    1840 * time.Millisecond,
				ToolVersion: version.Version,
				InstallID:   consent.InstallID,
			})
			if g.JSON {
				return writeJSON(cmd, map[string]any{
					"enabled":  consent.Enabled,
					"endpoint": telemetry.DefaultEndpoint,
					"event":    sample,
				})
			}
			w := cmd.OutOrStdout()
			endpoint := telemetry.DefaultEndpoint
			if endpoint == "" {
				endpoint = "(none — ship-empty; nothing is ever sent)"
			}
			fmt.Fprintf(w, "endpoint: %s\n", endpoint)
			fmt.Fprintf(w, "enabled:  %v\n", consent.Enabled)
			fmt.Fprintln(w, "the exact event that would be sent (allowlist — no paths/secrets/PII):")
			return writeJSON(cmd, sample)
		},
	}
}
