package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithTokenShim(t *testing.T) {
	g := testGit(t)
	tg, cleanup, err := g.WithToken("secret-token-123")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// GIT_ASKPASS points at an existing executable shim.
	var askpass string
	for _, e := range tg.env {
		if v, ok := strings.CutPrefix(e, "GIT_ASKPASS="); ok {
			askpass = v
		}
	}
	if askpass == "" {
		t.Fatal("GIT_ASKPASS not set on the token handle")
	}
	if _, err := os.Stat(askpass); err != nil {
		t.Fatalf("shim missing: %v", err)
	}

	// The token must NOT be embedded in the shim script (it lives in a sibling
	// 0600 file the shim cats) — so it never appears as a process argument.
	script, _ := os.ReadFile(askpass)
	if strings.Contains(string(script), "secret-token-123") {
		t.Error("token leaked into the shim script")
	}
	tokenFile := filepath.Join(filepath.Dir(askpass), "token")
	fi, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatalf("token file missing: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file perms = %v, want 0600", perm)
	}

	// cleanup removes the whole shim dir.
	cleanup()
	if _, err := os.Stat(askpass); !os.IsNotExist(err) {
		t.Error("cleanup should remove the shim")
	}
}

func TestWithTokenEmptyIsNoop(t *testing.T) {
	g := testGit(t)
	g2, cleanup, err := g.WithToken("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if g2 != g {
		t.Error("empty token should return the same handle unchanged")
	}
}
