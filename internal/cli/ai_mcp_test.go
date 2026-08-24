package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAiMcpIsRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	cmd, _, err := root.Find([]string{"ai", "mcp"})
	if err != nil {
		t.Fatalf("ai mcp not registered: %v", err)
	}
	if cmd.RunE == nil {
		t.Fatal("ai mcp has no RunE")
	}
	for _, flag := range []string{"read-only", "allow-destructive"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("ai mcp is missing --%s", flag)
		}
	}
}

// TestRunDevstackCapturedIsIsolated pins the property the whole MCP design rests
// on: every tool call runs a FRESH command tree whose output is captured, so
// nothing leaks between calls and nothing reaches the real stdout.
func TestRunDevstackCapturedIsIsolated(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVSTACK_WORKSPACE", "")
	t.Setenv("DEVSTACK_HOME", dir)

	out, err := runDevstackCaptured(context.Background(), []string{"--json", "ai", "commands"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var cat struct {
		Commands []struct {
			Path string `json:"path"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(out, &cat); err != nil {
		t.Fatalf("captured output is not the JSON the tool promised: %v\n%s", err, out)
	}
	if len(cat.Commands) == 0 {
		t.Fatal("captured output is empty")
	}

	// A second call must not inherit the first call's flag state.
	plain, err := runDevstackCaptured(context.Background(), []string{"ai", "docs", "--section", "guide"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if json.Valid(plain) {
		t.Error("the second call rendered JSON; --json leaked from the previous command tree")
	}
}

// TestRunDevstackCapturedReportsFailures: a failing command must surface its
// message, since that message is what the model shows the user.
func TestRunDevstackCapturedReportsFailures(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVSTACK_WORKSPACE", "")

	_, err := runDevstackCaptured(context.Background(), []string{"--json", "ai", "docs", "no/such/doc"})
	if err == nil {
		t.Fatal("expected an error for an unknown document")
	}
	if !strings.Contains(err.Error(), "no such doc") {
		t.Errorf("the error should carry the command's message, got: %v", err)
	}
}

// TestMcpDepsWire checks the adapters actually resolve against the real embedded
// assets, not just compile.
func TestMcpDepsWire(t *testing.T) {
	root := NewRootCmd(Options{})
	cmd, _, err := root.Find([]string{"ai", "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	deps, err := mcpDeps(cmd)
	if err != nil {
		t.Fatalf("mcpDeps: %v", err)
	}

	list, err := deps.Docs.List()
	if err != nil {
		t.Fatalf("Docs.List: %v", err)
	}
	if len(list) < 60 {
		t.Errorf("expected the whole corpus, got %d documents", len(list))
	}

	if _, body, err := deps.Docs.Read("guide/templates"); err != nil || len(body) == 0 {
		t.Errorf("Docs.Read(guide/templates) = %d bytes, %v", len(body), err)
	}

	kinds := deps.SchemaKinds()
	if len(kinds) == 0 {
		t.Fatal("no schema kinds")
	}
	for _, k := range kinds {
		doc, err := deps.Schema(k)
		if err != nil || !json.Valid(doc) {
			t.Errorf("Schema(%s) = %d bytes, %v", k, len(doc), err)
		}
	}

	// The resource template must reach a real, shipping template.
	f, err := deps.Templates().Open("postgres/template.yaml")
	if err != nil {
		t.Fatalf("the built-in postgres template is not reachable: %v", err)
	}
	_ = f.Close()
}
