package cli

import (
	"strings"
	"testing"
)

// sudoSelfCmd must render an ABSOLUTE binary path. sudo resets PATH to its
// secure_path, which typically excludes ~/.local/bin, so `sudo devstack …` would
// fail with "command not found" for a user-local install. The absolute path from
// os.Executable() is what makes the remediation actually runnable.
func TestSudoSelfCmdUsesAbsolutePath(t *testing.T) {
	got := sudoSelfCmd("dns setup")
	if !strings.HasPrefix(got, "sudo ") {
		t.Fatalf("want a sudo prefix, got %q", got)
	}
	if !strings.HasSuffix(got, " dns setup") {
		t.Fatalf("want the subcommand suffix, got %q", got)
	}
	fields := strings.Fields(got)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "/") {
		t.Errorf("binary path should be absolute (sudo can't use secure_path for ~/.local/bin), got %q", got)
	}
}
