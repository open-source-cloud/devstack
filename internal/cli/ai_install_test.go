package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aiScratch gives each test an isolated repo with no workspace, which is also the
// case `ai install` has to support: an agent installing the guidance before
// `devstack init` has ever run.
func aiScratch(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVSTACK_WORKSPACE", "")
	t.Setenv("DEVSTACK_HOME", filepath.Join(dir, ".devstack-home"))
	return dir
}

func TestAiInstallIsRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, path := range [][]string{{"ai", "install"}, {"ai", "check"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v not registered: %v", path, err)
		}
		if cmd.RunE == nil {
			t.Errorf("%v has no RunE", path)
		}
	}
}

func TestAiInstallWritesAndIsIdempotent(t *testing.T) {
	dir := aiScratch(t)

	out, err := runAi(t, "install")
	if err != nil {
		t.Fatalf("ai install: %v\n%s", err, out)
	}
	for _, want := range []string{
		".claude/skills/devstack/SKILL.md",
		".claude/skills/devstack-templates/SKILL.md",
		".claude/skills/devstack-troubleshooting/SKILL.md",
		"AGENTS.md", "CLAUDE.md", ".mcp.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s was not written: %v", want, err)
		}
	}
	if !strings.Contains(out, "7 of 7 artifact(s) changed") {
		t.Errorf("unexpected summary:\n%s", out)
	}

	out, err = runAi(t, "install")
	if err != nil {
		t.Fatalf("second ai install: %v", err)
	}
	if !strings.Contains(out, "0 of 7 artifact(s) changed") {
		t.Errorf("second run should change nothing:\n%s", out)
	}
}

func TestAiCheckReportsDriftAndExitsNonZero(t *testing.T) {
	dir := aiScratch(t)

	// Nothing installed yet: check must fail and say so.
	out, err := runAi(t, "check")
	if err == nil {
		t.Fatalf("ai check should fail before install; got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "ai install") {
		t.Errorf("the error should name the fix, got: %v", err)
	}

	if _, err := runAi(t, "install"); err != nil {
		t.Fatalf("ai install: %v", err)
	}
	if out, err := runAi(t, "check"); err != nil {
		t.Fatalf("ai check should pass after install: %v\n%s", err, out)
	}

	// Corrupt one generated file; check must notice.
	skill := filepath.Join(dir, ".claude", "skills", "devstack", "SKILL.md")
	if err := os.WriteFile(skill, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runAi(t, "check")
	if err == nil {
		t.Fatalf("ai check should detect a tampered artifact; got:\n%s", out)
	}
	if !strings.Contains(out, "SKILL.md") {
		t.Errorf("check should name the stale file:\n%s", out)
	}
}

func TestAiCheckJSON(t *testing.T) {
	aiScratch(t)
	if _, err := runAi(t, "install"); err != nil {
		t.Fatalf("ai install: %v", err)
	}
	root := NewRootCmd(Options{})
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--json", "ai", "check"})
	if err := root.Execute(); err != nil {
		t.Fatalf("ai check --json: %v\n%s", err, buf.String())
	}
	var got struct {
		OK    bool     `json:"ok"`
		Stale []string `json:"stale"`
	}
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if !got.OK || len(got.Stale) != 0 {
		t.Errorf("expected ok with no stale entries, got %+v", got)
	}
}

func TestAiInstallTargetSubset(t *testing.T) {
	dir := aiScratch(t)
	if _, err := runAi(t, "install", "--target", "mcp"); err != nil {
		t.Fatalf("ai install --target mcp: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Errorf(".mcp.json was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("--target mcp should not have written AGENTS.md")
	}
	if _, err := runAi(t, "install", "--target", "nope"); err == nil {
		t.Error("expected an error for an unknown target")
	}
}

// TestAiInstallPreservesUserContent is the property a user actually cares about:
// running install again must not eat their notes or their other MCP servers.
func TestAiInstallPreservesUserContent(t *testing.T) {
	dir := aiScratch(t)
	agents := filepath.Join(dir, "AGENTS.md")
	mcp := filepath.Join(dir, ".mcp.json")

	if err := os.WriteFile(agents, []byte("# Mine\n\nHand-written.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, []byte(`{"mcpServers":{"other":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runAi(t, "install"); err != nil {
		t.Fatalf("ai install: %v", err)
	}

	body, err := os.ReadFile(agents)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "# Mine\n\nHand-written.\n") {
		t.Errorf("hand-written AGENTS.md content was lost:\n%s", body)
	}

	raw, err := os.ReadFile(mcp)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if _, ok := cfg.Servers["other"]; !ok {
		t.Error("an existing MCP server was dropped")
	}
	if _, ok := cfg.Servers["devstack"]; !ok {
		t.Error("the devstack MCP server was not registered")
	}
}

// TestAiInstallUsesTheInvokedName covers argv[0] aliasing end to end: an
// installation run as `rq` must emit files that say rq.
func TestAiInstallUsesTheInvokedName(t *testing.T) {
	dir := aiScratch(t)
	root := NewRootCmd(Options{InvokedAs: "rq"})
	var buf strings.Builder
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"ai", "install"})
	if err := root.Execute(); err != nil {
		t.Fatalf("rq ai install: %v\n%s", err, buf.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"command": "rq"`) {
		t.Errorf(".mcp.json should invoke rq:\n%s", raw)
	}
}

// TestAiInstallRefusesHomeDirectory is a regression test for a real incident:
// workspace discovery walks UP from the current directory, so a single stray
// workspace.yaml sitting in a home directory silently redirected the whole
// install there, scattering AGENTS.md, CLAUDE.md and .mcp.json across $HOME and
// installing skills machine-wide. Writing files is not `generate`; the discovered
// root has to be sanity-checked.
func TestAiInstallRefusesHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows
	t.Chdir(home)
	t.Setenv("DEVSTACK_WORKSPACE", "")

	out, err := runAi(t, "install")
	if err == nil {
		t.Fatalf("expected ai install to refuse the home directory, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Errorf("the error should explain why, got: %v", err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".mcp.json"} {
		if _, statErr := os.Stat(filepath.Join(home, name)); !os.IsNotExist(statErr) {
			t.Errorf("%s was written into the home directory anyway", name)
		}
	}
}

// TestAiInstallIgnoresAWorkspaceAtHome covers the exact discovered-root path: a
// project directory whose ancestor happens to contain a workspace.yaml must
// install into the project, not into that ancestor.
func TestAiInstallIgnoresAWorkspaceAtHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// A stray workspace.yaml in the home directory, exactly as `devstack init`
	// run from $HOME would leave behind.
	if err := os.WriteFile(filepath.Join(home, "workspace.yaml"), []byte(
		"apiVersion: devstack/v1\nkind: Workspace\nname: stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, "code", "myrepo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("DEVSTACK_WORKSPACE", "")
	t.Setenv("DEVSTACK_HOME", filepath.Join(home, ".devstack-home"))

	if out, err := runAi(t, "install"); err != nil {
		t.Fatalf("ai install: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md was not written into the project: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "AGENTS.md")); !os.IsNotExist(err) {
		t.Error("AGENTS.md leaked into the home directory")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Error("skills leaked into the home directory")
	}
}
