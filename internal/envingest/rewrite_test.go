package envingest

import (
	"strings"
	"testing"
)

func TestRewriteCreatesEnvBlockWhenAbsent(t *testing.T) {
	src := `apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: node.vite
`
	decisions := []Decision{
		{Key: "APP_ENV", Class: "config", Ref: "local", value: "local"},
		{Key: "DB_PASSWORD", Class: "secret", Ref: "secret://sops/secrets.enc.yaml#DB_PASSWORD", value: "x"},
	}
	out, err := rewriteProjectEnv([]byte(src), "api", decisions, false)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"env:", "raw:", "APP_ENV: local", "DB_PASSWORD:"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

func TestRewritePrefixedRoutesSecrets(t *testing.T) {
	src := `apiVersion: devstack/v1
kind: Project
name: api
services:
  api:
    template: node.vite
    env:
      raw:
        EXISTING: keep
`
	decisions := []Decision{
		{Key: "APP_ENV", Class: "config", Ref: "local", value: "local"},
		{Key: "DB_PASSWORD", Class: "secret", Ref: "secret://sops/secrets.enc.yaml#DB_PASSWORD", value: "x"},
	}
	out, err := rewriteProjectEnv([]byte(src), "api", decisions, true)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "prefixed:") {
		t.Errorf("secrets not routed to prefixed block:\n%s", s)
	}
	// Config key stays in raw; secret moved to prefixed.
	rawIdx := strings.Index(s, "raw:")
	prefIdx := strings.Index(s, "prefixed:")
	appIdx := strings.Index(s, "APP_ENV")
	dbIdx := strings.Index(s, "DB_PASSWORD")
	if !(rawIdx < appIdx) {
		t.Errorf("APP_ENV not under raw")
	}
	if !(prefIdx < dbIdx) {
		t.Errorf("DB_PASSWORD not under prefixed")
	}
}

func TestScaffoldProviderAppends(t *testing.T) {
	src := `apiVersion: devstack/v1
kind: Workspace
name: demo
secrets:
  providers:
    - name: existing
      kind: aws-sm
shared: {}
`
	out, changed, err := scaffoldProvider([]byte(src), "sops", "sops", "/home/dev/.devstack/age/keys.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	s := string(out)
	if !strings.Contains(s, "name: existing") || !strings.Contains(s, "name: sops") {
		t.Errorf("scaffold lost or skipped a provider:\n%s", s)
	}
}

func TestScaffoldProviderCreatesSecretsBlock(t *testing.T) {
	src := `apiVersion: devstack/v1
kind: Workspace
name: demo
shared: {}
`
	out, changed, err := scaffoldProvider([]byte(src), "sops", "sops", "/key")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected change")
	}
	s := string(out)
	if !strings.Contains(s, "secrets:") || !strings.Contains(s, "providers:") || !strings.Contains(s, "kind: sops") {
		t.Errorf("secrets block not created:\n%s", s)
	}
}
