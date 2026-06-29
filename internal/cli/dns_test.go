package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDnsRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, sub := range []string{"setup", "status", "remove"} {
		c, _, err := root.Find([]string{"dns", sub})
		if err != nil || c.Name() != sub || c.RunE == nil {
			t.Errorf("dns %s not registered as a real command: %v", sub, err)
		}
	}
}

func TestDnsStatusReportsRoutes(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "workspace.yaml"),
		"apiVersion: devstack/v1\nkind: Workspace\nname: shop\nnetwork: { proxy: { engine: caddy } }\nprojects:\n  - { name: api, path: api }\n")
	mustWrite(t, filepath.Join(dir, "api", "devstack.yaml"),
		"apiVersion: devstack/v1\nkind: Project\nname: api\nservices:\n  web: { template: node.vite, ports: { http: 8080 } }\n")
	t.Chdir(dir)

	var out strings.Builder
	root := NewRootCmd(Options{})
	root.SetArgs([]string{"dns", "status"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("dns status: %v\n%s", err, out.String())
	}
	// The proxy route host should appear (present or missing — /etc/hosts is read
	// only here and won't contain it, so it lands in missing).
	if !strings.Contains(out.String(), "web.api.localhost") {
		t.Errorf("dns status should mention the route host:\n%s", out.String())
	}
}
