package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestTunnelRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, sub := range []string{"login", "create", "route", "up", "down"} {
		c, _, err := root.Find([]string{"tunnel", sub})
		if err != nil || c.Name() != sub || c.RunE == nil {
			t.Errorf("tunnel %s not registered as a real command: %v", sub, err)
		}
	}
}

func TestNonLocalSecretRefs(t *testing.T) {
	root := writeWS(t,
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\n"+
			"secrets:\n  providers:\n    - { name: aws, kind: aws-sm }\n    - { name: vault, kind: sops }\n"+
			"projects:\n  - { name: app, path: app }\n",
		map[string]string{
			"app": "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n" +
				"  web:\n    template: node.vite\n    env:\n      raw:\n" +
				"        CLOUD: secret://aws/db\n        LOCAL: secret://vault/key\n        PLAIN: hello\n",
		})
	m := mustLoad(t, root)
	refs := nonLocalSecretRefs(m)
	// The aws (cloud) ref is non-local and must be reported; the sops one is local
	// and must NOT; the plain value is not a secret.
	if !slices.ContainsFunc(refs, func(r string) bool { return strings.Contains(r, "aws") }) {
		t.Errorf("non-local refs %v should include the aws ref", refs)
	}
	if slices.ContainsFunc(refs, func(r string) bool { return strings.Contains(r, "vault") }) {
		t.Errorf("non-local refs %v must NOT include the local sops ref", refs)
	}
}

func TestTunnelUpRefusesNonLocalSecret(t *testing.T) {
	root := writeWS(t,
		"apiVersion: devstack/v1\nkind: Workspace\nname: demo\n"+
			"projects:\n  - { name: app, path: app }\n",
		map[string]string{
			// Provider "cloud" is undeclared → classified non-local (fail safe).
			"app": "apiVersion: devstack/v1\nkind: Project\nname: app\nservices:\n" +
				"  web:\n    template: node.vite\n    env:\n      raw: { KEY: secret://cloud/db }\n",
		})
	t.Chdir(root)

	rootCmd := NewRootCmd(Options{})
	var out strings.Builder
	rootCmd.SetArgs([]string{"tunnel", "up"})
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// The secret guard fires before any docker/cloudflared call, so this is safe
	// without a daemon.
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("tunnel up must refuse a service carrying a non-local secret://")
	}
	if !strings.Contains(err.Error(), "allow-secrets") {
		t.Errorf("refusal should mention the --allow-secrets override: %v", err)
	}
}
