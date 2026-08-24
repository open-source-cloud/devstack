package cli

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/aidocs"
)

func TestAiCommandsCatalogMatchesTheTree(t *testing.T) {
	root := NewRootCmd(Options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--json", "ai", "commands"})
	if err := root.Execute(); err != nil {
		t.Fatalf("ai commands --json: %v\n%s", err, out.String())
	}
	var cat struct {
		Binary      string `json:"binary"`
		GlobalFlags []struct {
			Name string `json:"name"`
		} `json:"globalFlags"`
		Commands []struct {
			Path     string `json:"path"`
			Short    string `json:"short"`
			Group    bool   `json:"group"`
			Runnable bool   `json:"runnable"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(out.Bytes(), &cat); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if cat.Binary != "devstack" {
		t.Errorf("binary = %q, want devstack", cat.Binary)
	}
	// The four documented global flags must be reported once, at the top level,
	// rather than repeated on all 100+ commands.
	global := map[string]bool{}
	for _, f := range cat.GlobalFlags {
		global[f.Name] = true
	}
	for _, want := range []string{"json", "quiet", "debug", "verbose"} {
		if !global[want] {
			t.Errorf("global flag --%s missing from the catalog", want)
		}
	}
	paths := map[string]bool{}
	for _, c := range cat.Commands {
		paths[c.Path] = true
		if c.Short == "" {
			t.Errorf("%s has no Short summary", c.Path)
		}
	}
	// Spot-check breadth and depth, including a three-level path.
	for _, want := range []string{"up", "db", "db user create", "template lint", "ai docs", "config schema"} {
		if !paths[want] {
			t.Errorf("catalog is missing %q", want)
		}
	}
	if len(cat.Commands) < 80 {
		t.Errorf("expected the full tree (39 top-level plus subcommands), got %d", len(cat.Commands))
	}
}

func TestAiCommandsIsDeterministic(t *testing.T) {
	run := func() string {
		root := NewRootCmd(Options{})
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{"--json", "ai", "commands"})
		if err := root.Execute(); err != nil {
			t.Fatalf("ai commands: %v", err)
		}
		return out.String()
	}
	if a, b := run(), run(); a != b {
		t.Error("ai commands --json is not byte-deterministic")
	}
}

// docCommandRE pulls every backticked span out of a line.
var docCommandRE = regexp.MustCompile("`([^`]+)`")

// argTokenRE matches the argument/flag furniture that follows a command name.
var argTokenRE = regexp.MustCompile(`^([<\[]|--|-\w)`)

// TestCommandReferenceMatchesTheTree retires docs/guide/command-reference.md as a
// standing drift source. It is hand-maintained, so nothing previously stopped it
// naming a verb that no longer exists, or omitting one that does. Renaming a
// command now fails here — in the PR that renamed it.
//
// The parser follows the conventions the reference actually uses: a table row
// names commands in its first cell; a row may abbreviate siblings as
// `dns setup` / `status` / `remove`, where the bare names inherit the preceding
// group; and a prose or blockquote line naming a command counts as documenting it
// (that is how the top-level expose/ports aliases are covered).
func TestCommandReferenceMatchesTheTree(t *testing.T) {
	_, body, err := aidocs.Read("guide/command-reference")
	if err != nil {
		t.Fatalf("read command-reference: %v", err)
	}
	root := NewRootCmd(Options{})
	real := aiCommandPaths(root, false)
	documented := map[string]bool{}

	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var group string
		for _, m := range docCommandRE.FindAllStringSubmatch(cellOf(line), -1) {
			path, prefix := commandPathFrom(m[1], group, real)
			if path == "" {
				continue
			}
			documented[path] = true
			if prefix != "" {
				group = prefix
			}
		}
	}

	if len(documented) < 60 {
		t.Fatalf("only parsed %d commands out of the reference; the matcher is broken", len(documented))
	}

	// Forward: every invocable command must appear somewhere in the reference.
	var undocumented []string
	for c := range aiCommandPaths(root, true) {
		if !documented[c] && !exemptFromReference(c) {
			undocumented = append(undocumented, c)
		}
	}
	sort.Strings(undocumented)
	for _, c := range undocumented {
		t.Errorf("command %q is not documented in docs/guide/command-reference.md", c)
	}

	// Reverse: the reference must not name a command that no longer exists.
	// commandPathFrom only ever yields real paths, so a stale name simply fails to
	// resolve; assert that every command-shaped row resolves at least one.
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cell := cellOf(line)
		spans := docCommandRE.FindAllStringSubmatch(cell, -1)
		if len(spans) == 0 {
			continue
		}
		var group string
		resolved := false
		for _, m := range spans {
			if p, prefix := commandPathFrom(m[1], group, real); p != "" {
				resolved = true
				if prefix != "" {
					group = prefix
				}
			}
		}
		if !resolved && !rowIsFangBuiltins(spans) {
			t.Errorf("command-reference row names a command that does not exist: %s", strings.TrimSpace(cell))
		}
	}
}

// cellOf returns the first table cell of a row, or the whole line when it is not
// a table row (so prose and blockquotes are scanned too).
func cellOf(line string) string {
	if !strings.HasPrefix(line, "|") {
		return line
	}
	// A cell may contain an escaped pipe, as in `s3 versioning <bucket> on\|off`.
	// Protect those before splitting on the real column separators.
	const esc = "\x00PIPE\x00"
	protected := strings.ReplaceAll(line, `\|`, esc)
	cells := strings.Split(protected, "|")
	if len(cells) < 3 {
		return ""
	}
	return strings.ReplaceAll(cells[1], esc, "|")
}

// commandPathFrom turns a backticked span into the longest prefix that is a real
// command path. group carries the last resolved group so a bare sibling name
// resolves: in `dns setup` / `status` / `remove`, "status" must read as
// "dns status" and NOT as the unrelated top-level "status" — which is why the
// group-qualified reading is tried first.
func commandPathFrom(span, group string, real map[string]bool) (path, prefix string) {
	fields := strings.Fields(strings.TrimSpace(span))
	if len(fields) == 0 {
		return "", ""
	}
	// Both readings can resolve: for `project new <name>` under group "project",
	// the qualified reading degrades to the bare group "project" while the
	// standalone reading gives the better "project new". Take whichever names
	// more segments, so the most specific command always wins.
	standalone := longestRealPrefix(fields, real)
	qualified := ""
	if group != "" {
		qualified = longestRealPrefix(append(strings.Fields(group), fields...), real)
	}
	if segments(qualified) > segments(standalone) {
		return qualified, group
	}
	if standalone == "" {
		return "", ""
	}
	// Only a multi-segment path establishes a group; a top-level command has no
	// siblings that could inherit it.
	if i := strings.LastIndex(standalone, " "); i > 0 {
		return standalone, standalone[:i]
	}
	return standalone, ""
}

// segments counts the words in a command path; "" counts as zero.
func segments(path string) int {
	if path == "" {
		return 0
	}
	return len(strings.Fields(path))
}

// longestRealPrefix returns the longest leading run of fields that names a real
// command, stopping at the first argument or flag token.
func longestRealPrefix(fields []string, real map[string]bool) string {
	best := ""
	for i := range fields {
		if argTokenRE.MatchString(fields[i]) {
			break
		}
		if candidate := strings.Join(fields[:i+1], " "); real[candidate] {
			best = candidate
		}
	}
	return best
}

// exemptFromReference lists commands the reference does not tabulate as their own
// row.
func exemptFromReference(path string) bool {
	return fangBuiltin(path)
}

// fangBuiltin reports whether a name is one of the commands fang adds to every
// binary. They are documented as a pair and Catalog skips them.
func fangBuiltin(name string) bool {
	return name == "man" || name == "completion" || strings.HasPrefix(name, "completion ")
}

// rowIsFangBuiltins reports whether a row documents only fang's own commands.
func rowIsFangBuiltins(spans [][]string) bool {
	for _, m := range spans {
		if !fangBuiltin(strings.TrimSpace(m[1])) {
			return false
		}
	}
	return len(spans) > 0
}
