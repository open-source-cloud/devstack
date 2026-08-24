package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runAi(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd(Options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"ai"}, args...))
	err := root.Execute()
	return out.String(), err
}

// TestAiDocsIsRegistered is the house registration check: the verb must be a real
// RunE command, not a stub.
func TestAiDocsIsRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	cmd, _, err := root.Find([]string{"ai", "docs"})
	if err != nil {
		t.Fatalf("ai docs not registered: %v", err)
	}
	if cmd.RunE == nil {
		t.Fatal("ai docs has no RunE")
	}
}

func TestAiDocsListsTheCorpus(t *testing.T) {
	out, err := runAi(t, "docs")
	if err != nil {
		t.Fatalf("ai docs: %v\n%s", err, out)
	}
	for _, want := range []string{"GUIDE", "SPECS", "guide/templates", "documents"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q:\n%s", want, out)
		}
	}
}

func TestAiDocsListJSON(t *testing.T) {
	root := NewRootCmd(Options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--json", "ai", "docs"})
	if err := root.Execute(); err != nil {
		t.Fatalf("ai docs --json: %v\n%s", err, out.String())
	}
	var got struct {
		Docs []struct {
			Slug    string `json:"slug"`
			Path    string `json:"path"`
			Title   string `json:"title"`
			Section string `json:"section"`
			Lines   int    `json:"lines"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if len(got.Docs) < 60 {
		t.Fatalf("expected the whole corpus, got %d docs", len(got.Docs))
	}
	if got.Docs[0].Section != "guide" {
		t.Errorf("first doc section = %q, want guide", got.Docs[0].Section)
	}
}

func TestAiDocsPrintsMarkdown(t *testing.T) {
	out, err := runAi(t, "docs", "guide/templates")
	if err != nil {
		t.Fatalf("ai docs guide/templates: %v", err)
	}
	if !strings.HasPrefix(out, "# Templates") {
		t.Errorf("expected raw markdown starting with the H1, got:\n%s", truncate(out, 200))
	}
	if !strings.Contains(out, "[[") {
		t.Error("the templates guide should document the [[ ]] delimiters")
	}
}

func TestAiDocsSectionFilter(t *testing.T) {
	out, err := runAi(t, "docs", "--section", "specs")
	if err != nil {
		t.Fatalf("ai docs --section specs: %v\n%s", err, out)
	}
	if strings.Contains(out, "guide/") {
		t.Errorf("--section specs should not list guide pages:\n%s", out)
	}
	if !strings.Contains(out, "specs/01-config-schema") {
		t.Errorf("--section specs should list the specs:\n%s", out)
	}
	if _, err := runAi(t, "docs", "--section", "nope"); err == nil {
		t.Error("expected an error for an unknown section")
	}
}

func TestAiDocsSearch(t *testing.T) {
	out, err := runAi(t, "docs", "--search", "shared network", "--limit", "3")
	if err != nil {
		t.Fatalf("ai docs --search: %v\n%s", err, out)
	}
	if strings.Contains(out, "no documents match") {
		t.Errorf("expected hits for a core concept:\n%s", out)
	}
	if !strings.Contains(out, "ai docs <slug>") {
		t.Errorf("search output should tell the caller how to read a hit:\n%s", out)
	}
}

func TestAiDocsUnknownSlugSuggests(t *testing.T) {
	out, err := runAi(t, "docs", "guide/template")
	if err == nil {
		t.Fatalf("expected an error for an unknown slug, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "guide/templates") {
		t.Errorf("error should suggest the near miss, got: %v", err)
	}
}

// TestAiDocsNeedsNoWorkspace matters because an agent often reaches for the docs
// BEFORE a workspace exists — that is exactly when it is learning `devstack init`.
func TestAiDocsNeedsNoWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVSTACK_WORKSPACE", "")
	if _, err := runAi(t, "docs"); err != nil {
		t.Fatalf("ai docs must not require a workspace: %v", err)
	}
	if _, err := runAi(t, "docs", "guide/templates"); err != nil {
		t.Fatalf("ai docs <slug> must not require a workspace: %v", err)
	}
}
