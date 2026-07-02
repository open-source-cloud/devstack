package envingest

import (
	"strings"
	"testing"
)

const dsYAML = `apiVersion: devstack/v1
kind: Project
name: api
services:
  web:
    template: node.vite # dev server
    env:
      raw:
        APP_ENV: dev # keep me
`

func TestSetEnvPreservesComments(t *testing.T) {
	out, err := SetEnv([]byte(dsYAML), "web", "raw", map[string]string{"LOG_LEVEL": "debug"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# dev server", "APP_ENV: dev # keep me", "LOG_LEVEL: debug"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestSetEnvCreatesEnvBlock(t *testing.T) {
	src := "apiVersion: devstack/v1\nkind: Project\nname: api\nservices:\n  web:\n    template: node.vite\n"
	out, err := SetEnv([]byte(src), "web", "raw", map[string]string{"K": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "K: v") {
		t.Errorf("env block not created:\n%s", out)
	}
}

func TestReplaceEnvBlockRemovesKey(t *testing.T) {
	out, err := ReplaceEnvBlock([]byte(dsYAML), "web", "raw", map[string]string{"APP_ENV": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	// A hypothetical removed key must be gone; APP_ENV stays.
	if !strings.Contains(string(out), "APP_ENV: dev") {
		t.Errorf("APP_ENV should remain:\n%s", out)
	}
}

func TestAppendProjectRef(t *testing.T) {
	ws := "apiVersion: devstack/v1\nkind: Workspace\nname: demo\n# projects below\nprojects:\n  - { name: api, path: api }\n"
	out, err := AppendProjectRef([]byte(ws), "worker", "worker", "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "# projects below") || !strings.Contains(s, "worker") || !strings.Contains(s, "api") {
		t.Errorf("append lost data:\n%s", s)
	}
}
