package ai

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// testGen builds a Generator with a pinned version so goldens never drift with
// the build stamp.
func testGen(root string) *Generator {
	return New(WithRoot(root), WithBinary("devstack"), WithVersion("1.2.3"))
}

func buildAll(t *testing.T, root string) []Artifact {
	t.Helper()
	arts, err := testGen(root).Build(All())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return arts
}

func TestBuildEmitsTheExpectedArtifacts(t *testing.T) {
	arts := buildAll(t, "/repo")
	var got []string
	for _, a := range arts {
		got = append(got, a.Rel+" ["+a.Kind+"]")
	}
	want := []string{
		".claude/skills/devstack/SKILL.md [skill]",
		".claude/skills/devstack/reference.md [skill-support]",
		".claude/skills/devstack-templates/SKILL.md [skill]",
		".claude/skills/devstack-troubleshooting/SKILL.md [skill]",
		"AGENTS.md [agents-md]",
		"CLAUDE.md [claude-md]",
		".mcp.json [mcp-config]",
	}
	if len(got) != len(want) {
		t.Fatalf("emitted %d artifacts, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("artifact %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMergeModesAreCorrectlyAssigned pins the safety property that matters most:
// only files devstack fully owns may be written whole. Getting this wrong
// destroys a user's AGENTS.md on every regeneration.
func TestMergeModesAreCorrectlyAssigned(t *testing.T) {
	for _, a := range buildAll(t, "/repo") {
		var want MergeMode
		switch {
		case strings.HasPrefix(a.Rel, ".claude/skills/"):
			want = MergeWhole
		case a.Rel == "AGENTS.md", a.Rel == "CLAUDE.md":
			want = MergeFence
		case a.Rel == ".mcp.json":
			want = MergeJSONKey
		default:
			t.Errorf("unexpected artifact %s", a.Rel)
			continue
		}
		if a.Merge != want {
			t.Errorf("%s has merge mode %d, want %d", a.Rel, a.Merge, want)
		}
	}
}

func TestBuildIsPureAndDeterministic(t *testing.T) {
	// Build must not touch the disk, so a nonexistent root is fine.
	a := buildAll(t, filepath.Join(t.TempDir(), "does-not-exist"))
	b := buildAll(t, filepath.Join(t.TempDir(), "also-missing"))
	if len(a) != len(b) {
		t.Fatalf("artifact count differs between runs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Rel != b[i].Rel {
			t.Fatalf("artifact %d order differs: %q vs %q", i, a[i].Rel, b[i].Rel)
		}
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("%s is not byte-deterministic across runs", a[i].Rel)
		}
	}
}

func TestWriteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	arts := buildAll(t, dir)

	first, err := Write(arts)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, r := range first {
		if !r.Changed {
			t.Errorf("first write reported %s unchanged", r.Path)
		}
	}

	second, err := Write(buildAll(t, dir))
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	for _, r := range second {
		if r.Changed {
			t.Errorf("second write changed %s; emission is not idempotent", r.Path)
		}
	}

	ok, err := UpToDate(buildAll(t, dir))
	if err != nil {
		t.Fatalf("UpToDate: %v", err)
	}
	if !ok {
		t.Error("UpToDate is false immediately after Write")
	}
	stale, err := Stale(buildAll(t, dir))
	if err != nil {
		t.Fatalf("Stale: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("Stale reported %d artifacts right after Write", len(stale))
	}
}

// TestFencePreservesUserContent is the core promise of MergeFence: a user's own
// prose survives regeneration, above and below the block.
func TestFencePreservesUserContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	original := "# My Project\n\nMy own notes.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Write(buildAll(t, dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, original) {
		t.Errorf("user content at the top was not preserved:\n%s", got)
	}
	if !strings.Contains(got, markerBegin) || !strings.Contains(got, markerEnd) {
		t.Error("fence markers are missing")
	}

	// Append below the block, regenerate, and confirm both halves survive.
	trailer := "\n## Added later\n\nStill mine.\n"
	if err := os.WriteFile(path, []byte(got+trailer), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(buildAll(t, dir)); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	got = readFile(t, path)
	if !strings.HasPrefix(got, original) {
		t.Error("leading user content lost on regeneration")
	}
	if !strings.Contains(got, "Still mine.") {
		t.Error("trailing user content lost on regeneration")
	}
	if strings.Count(got, markerBegin) != 1 {
		t.Errorf("expected exactly one fence, found %d", strings.Count(got, markerBegin))
	}
}

// TestFenceReplacesStaleBlock proves regeneration updates the block in place
// rather than appending a second one.
func TestFenceReplacesStaleBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	stale := "# Doc\n\n" + markerBegin + "\nOLD CONTENT\n" + markerEnd + "\n\nAfter.\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(buildAll(t, dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "OLD CONTENT") {
		t.Error("stale block was not replaced")
	}
	if !strings.Contains(got, "After.") {
		t.Error("content after the block was lost")
	}
	if strings.Count(got, markerBegin) != 1 {
		t.Errorf("expected exactly one fence, found %d", strings.Count(got, markerBegin))
	}
}

// TestJSONKeyPreservesOtherServers is the equivalent promise for .mcp.json: a
// teammate's other MCP servers must survive.
func TestJSONKeyPreservesOtherServers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	original := `{
  "mcpServers": {
    "playwright": { "command": "npx", "args": ["@playwright/mcp"] }
  },
  "someOtherTopLevelKey": {"keep": true}
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(buildAll(t, dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &got); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	servers, _ := got["mcpServers"].(map[string]any)
	if _, ok := servers["playwright"]; !ok {
		t.Error("an existing MCP server was dropped")
	}
	if _, ok := got["someOtherTopLevelKey"]; !ok {
		t.Error("an unrelated top-level key was dropped")
	}
	ds, ok := servers["devstack"].(map[string]any)
	if !ok {
		t.Fatal("the devstack server was not registered")
	}
	if ds["command"] != "devstack" {
		t.Errorf("command = %v, want the bare binary name (an absolute path breaks for teammates)", ds["command"])
	}
}

func TestJSONKeyCreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(buildAll(t, dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".mcp.json"))), &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if _, ok := got["mcpServers"]; !ok {
		t.Error("mcpServers was not created")
	}
}

func TestJSONKeyRejectsMalformedExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(buildAll(t, dir)); err == nil {
		t.Fatal("expected an error rather than silently overwriting a malformed file")
	}
}

// TestSkillFrontmatterIsSpecPortable pins the constraint that lets these files be
// uploaded to claude.ai and packaged with the Agent Skills tooling: only the six
// spec fields are allowed, and any other key is a hard error there.
func TestSkillFrontmatterIsSpecPortable(t *testing.T) {
	allowed := map[string]bool{
		"name": true, "description": true, "license": true,
		"compatibility": true, "metadata": true, "allowed-tools": true,
	}
	keyRE := regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9_-]*):`)

	for _, a := range buildAll(t, "/repo") {
		if a.Kind != "skill" {
			continue
		}
		body := string(a.Data)
		if !strings.HasPrefix(body, "---\n") {
			t.Errorf("%s does not start with frontmatter", a.Rel)
			continue
		}
		end := strings.Index(body[4:], "\n---\n")
		if end < 0 {
			t.Errorf("%s has an unterminated frontmatter block", a.Rel)
			continue
		}
		fm := body[4 : 4+end]
		var sawName, sawDesc bool
		for _, line := range strings.Split(fm, "\n") {
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || line == "" {
				continue // continuation of a folded scalar or a nested map
			}
			m := keyRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			key := m[1]
			if !allowed[key] {
				t.Errorf("%s: frontmatter key %q is not in the Agent Skills spec "+
					"(allowed: allowed-tools, compatibility, description, license, metadata, name)", a.Rel, key)
			}
			switch key {
			case "name":
				sawName = true
			case "description":
				sawDesc = true
			}
		}
		if !sawName || !sawDesc {
			t.Errorf("%s must declare both name and description", a.Rel)
		}
	}
}

// TestSkillDescriptionsFitTheRouterBudget: Claude Code truncates the combined
// description text at 1,536 characters in the skill listing, so an over-long
// description silently loses its tail — including the trigger phrases.
func TestSkillDescriptionsFitTheRouterBudget(t *testing.T) {
	for _, s := range skills {
		if n := len(s.Description); n > 1536 {
			t.Errorf("skill %s description is %d chars; the listing truncates at 1536", s.Name, n)
		}
		if len(s.Description) < 80 {
			t.Errorf("skill %s description is only %d chars; too vague to route on", s.Name, len(s.Description))
		}
	}
}

// credentialRE matches literals that are shaped like real credentials. Note it
// does NOT match the string "secret://" on its own: the pack teaches agents about
// the secret:// scheme, and documenting a grammar is not leaking a value. What
// must never appear is a concrete credential or a resolved assignment.
var credentialRE = regexp.MustCompile(
	`BEGIN [A-Z ]*PRIVATE KEY` +
		`|AKIA[0-9A-Z]{16}` +
		`|gh[pousr]_[A-Za-z0-9]{20,}` +
		`|xox[baprs]-[A-Za-z0-9-]{10,}` +
		`|secret://\S+\s*=\s*\S`)

// TestNoSecretsInEmittedFiles extends the project-wide rule that no secret value
// may reach a generated file. These files get committed, so the bar is absolute.
func TestNoSecretsInEmittedFiles(t *testing.T) {
	for _, a := range buildAll(t, "/repo") {
		if m := credentialRE.FindString(string(a.Data)); m != "" {
			t.Errorf("%s contains something credential-shaped: %q", a.Rel, m)
		}
	}
}

// TestSecretGuidanceIsPresent is the positive half: the pack must actually tell
// an agent the rule, since "do not inline a resolved secret" is exactly the
// mistake a model makes when a variable will not resolve.
func TestSecretGuidanceIsPresent(t *testing.T) {
	var mentions int
	for _, a := range buildAll(t, "/repo") {
		if strings.Contains(string(a.Data), "secret://") {
			mentions++
		}
	}
	if mentions == 0 {
		t.Error("no emitted file explains the secret:// rule")
	}
}

// TestBinaryRewriteFollowsTheAlias: an installation invoked through an argv[0]
// alias must document its own name, or every command in the emitted files is
// wrong for that user.
func TestBinaryRewriteFollowsTheAlias(t *testing.T) {
	arts, err := New(WithRoot("/repo"), WithBinary("rq"), WithVersion("1.2.3")).Build(All())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var sawSkill, sawMCP bool
	for _, a := range arts {
		body := string(a.Data)
		switch a.Rel {
		case ".claude/skills/devstack/SKILL.md":
			sawSkill = true
			if !strings.Contains(body, "rq ai docs") {
				t.Error("skill body was not rewritten to the alias")
			}
			if strings.Contains(body, "devstack ai docs") {
				t.Error("skill body still names the default binary")
			}
			if !strings.Contains(body, "allowed-tools: Bash(rq:*)") {
				t.Error("allowed-tools was not rewritten to the alias")
			}
		case ".mcp.json":
			sawMCP = true
			if !strings.Contains(body, `"command": "rq"`) {
				t.Errorf(".mcp.json should invoke the alias, got:\n%s", body)
			}
		}
	}
	if !sawSkill || !sawMCP {
		t.Fatal("expected both a skill and the MCP config")
	}
}

func TestParseTargets(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Targets
	}{
		{"skills", Targets{Skills: true}},
		{"agents", Targets{AgentsMD: true}},
		{"mcp", Targets{MCP: true}},
		{"skills,mcp", Targets{Skills: true, MCP: true}},
		{"all", All()},
		{" Skills , AGENTS ", Targets{Skills: true, AgentsMD: true}},
	} {
		got, err := ParseTargets(tc.in)
		if err != nil {
			t.Errorf("ParseTargets(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseTargets(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
	if _, err := ParseTargets("nope"); err == nil {
		t.Error("expected an error for an unknown target")
	}
}

func TestTargetsSelectSubsets(t *testing.T) {
	only, err := testGen("/repo").Build(Targets{MCP: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(only) != 1 || only[0].Rel != ".mcp.json" {
		t.Errorf("--target mcp should emit only .mcp.json, got %d artifacts", len(only))
	}
	none, err := testGen("/repo").Build(Targets{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a zero Targets should emit nothing, got %d", len(none))
	}
}

// TestPackMentionsOnlyRealTemplateFunctions guards the most detail-dense claim in
// the pack: the FuncMap list. A function removed from internal/template must not
// keep being advertised to agents.
func TestPackMentionsOnlyRealTemplateFunctions(t *testing.T) {
	body, err := packBody(PackTemplates)
	if err != nil {
		t.Fatal(err)
	}
	// The pack lists the functions in one backticked run; collect them.
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-zA-Z]+)`").FindAllStringSubmatch(string(body), -1) {
		listed[m[1]] = true
	}
	for _, want := range []string{"default", "coalesce", "upper", "lower", "title", "trim",
		"trimPrefix", "trimSuffix", "replace", "contains", "hasPrefix", "hasSuffix",
		"join", "split", "quote", "squote", "indent", "nindent", "repeat", "atoi"} {
		if !listed[want] {
			t.Errorf("the templates pack no longer documents the %q template function", want)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestSkillDirsAreNamespaced keeps the /devstack command claim intentional and
// everything else prefixed, so the skills cannot collide with a user's own.
func TestSkillDirsAreNamespaced(t *testing.T) {
	dirs := SkillDirs()
	sort.Strings(dirs)
	for _, d := range dirs {
		if d != "devstack" && !strings.HasPrefix(d, "devstack-") {
			t.Errorf("skill dir %q is neither devstack nor devstack-prefixed", d)
		}
	}
}

// TestOutputDoesNotDependOnVersion is the guard that keeps `ai check` usable as a
// CI gate. These files are committed by users; if their content varied with the
// binary's version stamp, every release would mark every repository's files stale
// and `devstack self update` would produce a diff in everyone's working tree.
func TestOutputDoesNotDependOnVersion(t *testing.T) {
	build := func(version string) []Artifact {
		arts, err := New(WithRoot("/repo"), WithBinary("devstack"), WithVersion(version)).Build(All())
		if err != nil {
			t.Fatalf("Build(%s): %v", version, err)
		}
		return arts
	}
	a, b := build("dev"), build("9.9.9")
	if len(a) != len(b) {
		t.Fatalf("artifact count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Errorf("%s changes with the version stamp; committed files would go stale on every release", a[i].Rel)
		}
	}
}

// TestJSONKeyIgnoresFormatting is a regression test for a real failure: another
// tool reformatted .mcp.json (same JSON, different whitespace) and `ai check`
// reported it stale — which, with ai-check gating CI, would fail the build on a
// purely cosmetic change and make devstack fight the other formatter on every
// commit. devstack owns ONE KEY in that file, not its formatting.
func TestJSONKeyIgnoresFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")

	// Write it once the normal way.
	if _, err := Write(buildAll(t, dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Now reformat the file the way a different tool would: identical JSON,
	// compact array, extra key ordering churn.
	reformatted := `{
  "mcpServers": {
    "devstack": { "command": "devstack", "args": ["ai", "mcp"] }
  }
}
`
	if err := os.WriteFile(path, []byte(reformatted), 0o644); err != nil {
		t.Fatal(err)
	}

	stale, err := Stale(buildAll(t, dir))
	if err != nil {
		t.Fatalf("Stale: %v", err)
	}
	for _, a := range stale {
		if a.Rel == ".mcp.json" {
			t.Error(".mcp.json reported stale after a cosmetic reformat; devstack owns the key, not the formatting")
		}
	}

	// And a write must leave the reformatted file alone.
	results, err := Write(buildAll(t, dir))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, r := range results {
		if r.Path == ".mcp.json" && r.Changed {
			t.Error("devstack rewrote .mcp.json purely to reformat it")
		}
	}
	if got := readFile(t, path); got != reformatted {
		t.Errorf("the user's formatting was not preserved:\n%s", got)
	}
}

// TestJSONKeyDetectsARealChange is the other half: a genuinely wrong value must
// still be corrected.
func TestJSONKeyDetectsARealChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(path, []byte(
		`{"mcpServers":{"devstack":{"command":"WRONG","args":["ai","mcp"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := Stale(buildAll(t, dir))
	if err != nil {
		t.Fatalf("Stale: %v", err)
	}
	var found bool
	for _, a := range stale {
		if a.Rel == ".mcp.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("a wrong command value should be reported stale")
	}
	if _, err := Write(buildAll(t, dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(readFile(t, path), "WRONG") {
		t.Error("the wrong value was not corrected")
	}
}
