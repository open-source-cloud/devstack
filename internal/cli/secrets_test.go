package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/secrets"
)

func TestSecretsKeygenRegistered(t *testing.T) {
	if !findCmd(t, "secrets") {
		// secrets is a group; verify the keygen child is real.
	}
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"secrets", "keygen"})
	if err != nil || c.RunE == nil {
		t.Fatalf("secrets keygen not registered as a real command: %v", err)
	}
}

func TestSecretsKeygenWritesFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "age", "keys.txt")
	root := NewRootCmd(Options{})
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"secrets", "keygen", "--output", out})
	if err := root.Execute(); err != nil {
		t.Fatalf("keygen: %v\n%s", err, buf.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "AGE-SECRET-KEY-1") || !strings.Contains(body, "# public key: age1") {
		t.Errorf("key file = %q", body)
	}
	// 0600 perms on the secret.
	fi, _ := os.Stat(out)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}
	if !strings.Contains(buf.String(), "SOPS_AGE_KEY_FILE") {
		t.Errorf("output should hint SOPS_AGE_KEY_FILE:\n%s", buf.String())
	}
}

func TestSecretsLoginLogoutStatusRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, sub := range []string{"login", "logout", "status"} {
		c, _, err := root.Find([]string{"secrets", sub})
		if err != nil || c.Name() != sub || c.RunE == nil {
			t.Fatalf("secrets %s not registered as a real command: %v", sub, err)
		}
	}
}

// withKeyring swaps the openKeyring seam for the duration of a test.
func withKeyring(t *testing.T, k secrets.Keyring, persistent bool) {
	t.Helper()
	prev := openKeyring
	openKeyring = func() (secrets.Keyring, bool) { return k, persistent }
	t.Cleanup(func() { openKeyring = prev })
}

func runSecrets(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}

func TestSecretsLoginPersists(t *testing.T) {
	k := secrets.NewMemKeyring()
	withKeyring(t, k, true)

	if _, err := runSecrets(t, "secrets", "login", "infisical", "--token", "tok-123"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if v, _ := k.Get(secrets.ProviderCredKey("infisical")); v != "tok-123" {
		t.Errorf("stored token = %q, want tok-123", v)
	}

	out, err := runSecrets(t, "secrets", "status", "infisical")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "available") || !strings.Contains(out, "keyring") {
		t.Errorf("status should show available + keyring source:\n%s", out)
	}

	if _, err := runSecrets(t, "secrets", "logout", "infisical"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if v, _ := k.Get(secrets.ProviderCredKey("infisical")); v != "" {
		t.Errorf("after logout = %q, want empty", v)
	}
}

func TestSecretsLoginDegradesWithoutKeyring(t *testing.T) {
	// WSL2 path: keyring unavailable → warn, name the env var, exit 0, store nothing.
	k := secrets.NewMemKeyring()
	withKeyring(t, k, false)

	out, err := runSecrets(t, "secrets", "login", "aws", "--token", "x")
	if err != nil {
		t.Fatalf("login must degrade (exit 0), got %v", err)
	}
	if !strings.Contains(out, "DEVSTACK_AWS_TOKEN") {
		t.Errorf("degrade message must name the env var:\n%s", out)
	}
	if v, _ := k.Get(secrets.ProviderCredKey("aws")); v != "" {
		t.Error("nothing should be stored when the keyring is unavailable")
	}
}

func TestSecretsLoginRequiresToken(t *testing.T) {
	withKeyring(t, secrets.NewMemKeyring(), true)
	if _, err := runSecrets(t, "secrets", "login", "aws"); err == nil {
		t.Fatal("login without --token must error")
	}
}
