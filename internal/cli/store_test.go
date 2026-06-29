package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreInitAndShow(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".devstack")
	t.Setenv("DEVSTACK_HOME", home)

	if out, err := runCmd(t, "store", "init"); err != nil {
		t.Fatalf("store init: %v\n%s", err, out)
	}
	for _, p := range []string{"config.yaml", "templates"} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Errorf("store init did not create %s: %v", p, err)
		}
	}
	// Re-init without --force refuses.
	if _, err := runCmd(t, "store", "init"); err == nil {
		t.Error("second store init should refuse without --force")
	}
	// show lists the seeded shared services.
	out, err := runCmd(t, "store", "show")
	if err != nil {
		t.Fatalf("store show: %v", err)
	}
	for _, want := range []string{"postgres", "redis", "minio"} {
		if !strings.Contains(out, want) {
			t.Errorf("store show missing %q:\n%s", want, out)
		}
	}
	// path prints the home.
	if out, _ := runCmd(t, "store", "path"); !strings.Contains(out, home) {
		t.Errorf("store path = %q, want %q", out, home)
	}
}
