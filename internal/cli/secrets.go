package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/secrets"
)

// newSecretsCmd wires `secrets keygen` (real) and `secrets login` (stub until the
// keyring lands, S5). keygen generates the offline age identity that the SOPS+age
// provider (S2) decrypts with — no account, fully local.
func newSecretsCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Secrets providers (age keygen, provider login)",
	}
	cmd.AddCommand(
		newSecretsKeygenCmd(g),
		newSecretsLoginCmd(g),
		newSecretsLogoutCmd(g),
		newSecretsStatusCmd(g),
	)
	return cmd
}

// openKeyring builds the credential store: the OS keyring when usable, otherwise
// an ephemeral in-memory fallback (WSL2 without D-Bus). The bool reports whether
// the store persists across invocations. It's a package var so tests inject a
// deterministic store.
var openKeyring = func() (secrets.Keyring, bool) {
	k := secrets.OSKeyring{}
	if secrets.KeyringAvailable(k) {
		return k, true
	}
	return secrets.NewMemKeyring(), false
}

// newSecretsLoginCmd wires `secrets login <provider>` (spec 04 §S5): store a
// provider credential in the OS keyring. On a keyring-less host (WSL2 without
// D-Bus) it degrades with a one-line warning naming the env var to set instead —
// the tool keeps working in env-var mode.
func newSecretsLoginCmd(g *GlobalOpts) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "login <provider>",
		Short: "Store a secrets-provider credential in the OS keyring",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			if token == "" {
				return fmt.Errorf("provide the credential via --token (avoid shell history: read it from a file or a pipe)")
			}
			k, persistent := openKeyring()
			if !persistent {
				// Degrade, never fail: tell the user the env-var path and exit 0.
				msg := fmt.Sprintf("os keyring unavailable (no Secret Service / D-Bus — common on WSL2); not stored.\n"+
					"set %s in your environment instead.", secrets.CredEnvVar(provider))
				if g.JSON {
					return writeJSON(cmd, map[string]any{"provider": provider, "stored": false, "envVar": secrets.CredEnvVar(provider), "warning": msg})
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "[warn]    %s\n", msg)
				return nil
			}
			if err := k.Set(secrets.ProviderCredKey(provider), token); err != nil {
				return fmt.Errorf("store credential for %q: %w", provider, err)
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"provider": provider, "stored": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored credential for %q in the OS keyring\n", provider)
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "the credential value (required)")
	return cmd
}

// newSecretsLogoutCmd wires `secrets logout <provider>` — remove a stored
// credential. A no-op (success) when none exists or the keyring is unavailable.
func newSecretsLogoutCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout <provider>",
		Short: "Remove a stored secrets-provider credential from the OS keyring",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			k, persistent := openKeyring()
			if !persistent {
				if g.JSON {
					return writeJSON(cmd, map[string]any{"provider": provider, "removed": false, "reason": "keyring unavailable"})
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "[warn]    os keyring unavailable; nothing to remove\n")
				return nil
			}
			if err := k.Delete(secrets.ProviderCredKey(provider)); err != nil {
				return fmt.Errorf("remove credential for %q: %w", provider, err)
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"provider": provider, "removed": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed credential for %q\n", provider)
			return nil
		},
	}
	return cmd
}

// newSecretsStatusCmd wires `secrets status` — report keyring availability and,
// for any providers named as args, whether a credential resolves (env or keyring).
func newSecretsStatusCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [provider...]",
		Short: "Report keyring availability and per-provider credential source",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, persistent := openKeyring()
			type provStatus struct {
				Provider string `json:"provider"`
				Source   string `json:"source"` // env | keyring | none
			}
			var provs []provStatus
			for _, p := range args {
				src := "none"
				if os.Getenv(secrets.CredEnvVar(p)) != "" {
					src = "env"
				} else if persistent {
					if v, _ := k.Get(secrets.ProviderCredKey(p)); v != "" {
						src = "keyring"
					}
				}
				provs = append(provs, provStatus{Provider: p, Source: src})
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"keyringPersistent": persistent, "providers": provs})
			}
			w := cmd.OutOrStdout()
			if persistent {
				fmt.Fprintln(w, "os keyring: available (credentials persist across invocations)")
			} else {
				fmt.Fprintln(w, "os keyring: UNAVAILABLE (no Secret Service / D-Bus); use env vars (DEVSTACK_<PROVIDER>_TOKEN)")
			}
			for _, p := range provs {
				fmt.Fprintf(w, "  %-20s %s\n", p.Provider, p.Source)
			}
			return nil
		},
	}
	return cmd
}

func newSecretsKeygenCmd(g *GlobalOpts) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate an age keypair for SOPS+age (offline, no account)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := secrets.GenerateAgeKey()
			if err != nil {
				return err
			}
			if output == "" {
				// No file: print the key body to stdout (caller redirects) and the
				// public recipient to stderr so a pipe captures only the key.
				if g.JSON {
					return writeJSON(cmd, map[string]string{"recipient": k.Recipient, "identity": k.Identity})
				}
				fmt.Fprint(cmd.OutOrStdout(), k.AgeKeyFileContents())
				fmt.Fprintf(cmd.ErrOrStderr(), "public recipient: %s\n", k.Recipient)
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(output, []byte(k.AgeKeyFileContents()), 0o600); err != nil {
				return fmt.Errorf("write age key %s: %w", output, err)
			}
			if g.JSON {
				return writeJSON(cmd, map[string]string{"path": output, "recipient": k.Recipient})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote age key to %s\npublic recipient: %s\n", output, k.Recipient)
			fmt.Fprintf(cmd.OutOrStdout(), "→ point SOPS at it: export SOPS_AGE_KEY_FILE=%s\n", output)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the key to this file (0600) instead of stdout")
	return cmd
}
