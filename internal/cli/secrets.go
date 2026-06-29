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
		stub("login", "Authenticate a secrets provider (keyring) — S5", "M4"),
	)
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
