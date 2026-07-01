package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestShellInitRegistered(t *testing.T) {
	if !findCmd(t, "shell-init") {
		t.Fatal("shell-init must be a real RunE command")
	}
}

func TestShellInitScript(t *testing.T) {
	cases := map[string][]string{
		"zsh": {
			"devstack() {",
			"--print --shell zsh",
			"completion zsh",
			"devstack_prompt()",
			`export PATH="/opt/bin:$PATH"`,
		},
		"bash": {"devstack() {", "--print --shell bash", "completion bash"},
		"fish": {
			"function devstack",
			"--print --shell fish | source",
			"completion fish | source",
			"function devstack_prompt",
			"set -gx PATH /opt/bin",
		},
	}
	for shell, wants := range cases {
		got, err := shellInitScript("devstack", shell, "/opt/bin")
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s script missing %q\n---\n%s", shell, w, got)
			}
		}
	}
	if _, err := shellInitScript("devstack", "tcsh", "/opt/bin"); err == nil {
		t.Error("unsupported shell should error")
	}
}

// TestShellInitUsesInvokedName verifies the wrapper is named after the binary, so
// an alias (rq) gets an rq() wrapper.
func TestShellInitUsesInvokedName(t *testing.T) {
	got, err := shellInitScript("rq", "zsh", "/opt/bin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "rq() {") || !strings.Contains(got, "command rq") {
		t.Errorf("alias wrapper not named rq:\n%s", got)
	}
}

func TestContextPromptSegment(t *testing.T) {
	root := writeWS(t,
		"apiVersion: devstack/v1\nkind: Workspace\nname: smoke\n"+
			"projects:\n  - { name: api, path: api }\n",
		map[string]string{"api": "apiVersion: devstack/v1\nkind: Project\nname: api\nservices:\n  app: { template: node.vite }\n"},
	)
	t.Chdir(root)

	run := func() string {
		c := NewRootCmd(Options{})
		var buf bytes.Buffer
		c.SetOut(&buf)
		c.SetErr(&buf)
		c.SetArgs([]string{"context", "--prompt"})
		if err := c.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
		return strings.TrimSpace(buf.String())
	}

	if got := run(); got != "smoke" {
		t.Errorf("prompt without project = %q, want smoke", got)
	}
	t.Setenv("DEVSTACK_PROJECT", "api")
	if got := run(); got != "smoke:api" {
		t.Errorf("prompt with project = %q, want smoke:api", got)
	}
}
