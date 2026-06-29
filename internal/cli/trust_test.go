package cli

import (
	"strings"
	"testing"
)

func TestTrustRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, sub := range []string{"install", "uninstall", "status"} {
		c, _, err := root.Find([]string{"trust", sub})
		if err != nil || c.Name() != sub || c.RunE == nil {
			t.Errorf("trust %s not registered as a real command: %v", sub, err)
		}
	}
}

func TestTrustStatusRuns(t *testing.T) {
	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"trust", "status"})
	root.SetOut(&out)
	root.SetErr(&out)
	// status is read-only and must not error even when mkcert is absent.
	if err := root.Execute(); err != nil {
		t.Fatalf("trust status: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "mkcert:") {
		t.Errorf("trust status output missing the mkcert line:\n%s", out.String())
	}
}
