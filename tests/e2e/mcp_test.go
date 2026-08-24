//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// mcpSession drives the real binary as an MCP stdio server, writing the given
// JSON-RPC lines and returning every response.
//
// This is the only place the protocol is exercised against the actual process,
// which is what makes the stdout-purity assertion meaningful: the in-process
// tests can prove the handlers behave, but only a real process can prove that
// nothing else — a banner, a log line, the self-update notifier — reaches stdout
// and corrupts the stream.
func mcpSession(t *testing.T, args []string, requests []string) ([]map[string]any, string) {
	t.Helper()
	s := newSandbox(t, nil)
	cmd := exec.Command(bin, args...)
	cmd.Env = s.env
	cmd.Dir = s.ws

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, r := range requests {
		if _, err := io.WriteString(stdin, r+"\n"); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	// Give the server time to answer before closing the stream.
	time.Sleep(2 * time.Second)
	_ = stdin.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// A client closing stdin is a normal shutdown, so this must exit 0.
		if err != nil {
			t.Errorf("ai mcp exited with %v; a client disconnect is not a failure\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("ai mcp did not exit after stdin closed")
	}

	var msgs []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout carried a non-JSON line, which corrupts the protocol stream:\n%.200q", line)
		}
		if m["jsonrpc"] != "2.0" {
			t.Fatalf("stdout carried a non-JSON-RPC message: %.200q", line)
		}
		msgs = append(msgs, m)
	}
	return msgs, stderr.String()
}

const (
	mcpInit        = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}`
	mcpInitialized = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
)

// TestMcpStdoutIsPureProtocol is the anti-footgun test. A Go MCP server most
// commonly ships broken because something else writes to stdout; here the whole
// session is parsed as JSON-RPC and any stray byte fails the test.
func TestMcpStdoutIsPureProtocol(t *testing.T) {
	msgs, _ := mcpSession(t, []string{"ai", "mcp"}, []string{
		mcpInit,
		mcpInitialized,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
	})
	if len(msgs) < 4 {
		t.Fatalf("expected a response per request, got %d", len(msgs))
	}
	byID := map[float64]map[string]any{}
	for _, m := range msgs {
		if id, ok := m["id"].(float64); ok {
			byID[id] = m
		}
	}

	info := digMap(t, byID[1], "result", "serverInfo")
	if info["name"] != "devstack" {
		t.Errorf("serverInfo.name = %v, want devstack", info["name"])
	}

	tools := digSlice(t, byID[2], "result", "tools")
	if len(tools) < 20 {
		t.Errorf("expected the full tool set, got %d", len(tools))
	}
	prompts := digSlice(t, byID[3], "result", "prompts")
	if len(prompts) != 5 {
		t.Errorf("expected 5 prompts, got %d", len(prompts))
	}
	resources := digSlice(t, byID[4], "result", "resources")
	if len(resources) < 60 {
		t.Errorf("expected the documentation corpus to be published, got %d resources", len(resources))
	}
}

// TestMcpReadOnlyOmitsMutatingTools proves --read-only actually narrows the
// surface in the shipped binary, not just in the unit tests.
func TestMcpReadOnlyOmitsMutatingTools(t *testing.T) {
	msgs, _ := mcpSession(t, []string{"ai", "mcp", "--read-only"}, []string{
		mcpInit, mcpInitialized,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	})
	var names []string
	for _, m := range msgs {
		if id, _ := m["id"].(float64); id != 2 {
			continue
		}
		for _, raw := range digSlice(t, m, "result", "tools") {
			tool, _ := raw.(map[string]any)
			name, _ := tool["name"].(string)
			names = append(names, name)
			if ann, ok := tool["annotations"].(map[string]any); ok {
				if ro, _ := ann["readOnlyHint"].(bool); !ro {
					t.Errorf("--read-only exposed %s without readOnlyHint", name)
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("no tools listed")
	}
	for _, n := range names {
		switch n {
		case "devstack_up", "devstack_down", "devstack_generate",
			"devstack_db_create", "devstack_workspace_destroy", "devstack_db_drop":
			t.Errorf("--read-only exposed the mutating tool %s", n)
		}
		if strings.Contains(n, "secret") {
			t.Errorf("the secrets group must never be exposed, found %s", n)
		}
	}
}

// TestMcpToolCallReturnsRealOutput closes the loop: a tool call must return the
// same content the CLI would print.
func TestMcpToolCallReturnsRealOutput(t *testing.T) {
	msgs, _ := mcpSession(t, []string{"ai", "mcp"}, []string{
		mcpInit, mcpInitialized,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"devstack_docs","arguments":{"slug":"guide/templates"}}}`,
	})
	for _, m := range msgs {
		if id, _ := m["id"].(float64); id != 2 {
			continue
		}
		result, _ := m["result"].(map[string]any)
		structured, _ := result["structuredContent"].(map[string]any)
		out, _ := structured["output"].(string)
		if !strings.Contains(out, "Templates") {
			t.Errorf("devstack_docs did not return the templates guide, got %.200q", out)
		}
		cmdLine, _ := structured["command"].(string)
		if !strings.Contains(cmdLine, "ai docs guide/templates") {
			t.Errorf("the tool should report the command it ran, got %q", cmdLine)
		}
		return
	}
	t.Fatal("no response to the tool call")
}

func digMap(t *testing.T, m map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := m
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("no object at %v in %v", path, m)
		}
		cur = next
	}
	return cur
}

func digSlice(t *testing.T, m map[string]any, path ...string) []any {
	t.Helper()
	cur := m
	for i, p := range path {
		if i == len(path)-1 {
			s, ok := cur[p].([]any)
			if !ok {
				t.Fatalf("no array at %v", path)
			}
			return s
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("no object at %v", path)
		}
		cur = next
	}
	return nil
}
