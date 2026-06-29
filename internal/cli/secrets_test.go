package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
