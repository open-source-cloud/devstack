package scaffold

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/open-source-cloud/devstack/internal/template"
)

// LintResult carries the soft findings of the authoring lints. Hard findings are
// returned as an error from Lint.
type LintResult struct {
	Warnings []string
}

// Lint runs the three authoring lints (spec 23) over a template's raw manifest +
// rendered/seeded build files. These are the NET-NEW checks that the compose-go
// pass (generate.LintResolved) does not perform, shared by BOTH `template new`'s
// live preview and `template lint`:
//
//   - meta-templating (HARD error): a `[[ ]]` action in a meta key. parseMeta reads
//     meta keys UNrendered, so a stray action there is silent garbage.
//   - delimiter-collision (warn): a literal `[[`/`]]` in a build/ file that is not a
//     valid template action (it fails to parse with the production delimiters).
//   - param-type (warn): a param default that does not parse as its declared type.
//
// buildFiles is keyed by the file's display path (e.g. "build/Dockerfile").
func Lint(manifest []byte, buildFiles map[string][]byte) (LintResult, error) {
	var res LintResult

	// 1. meta-templating — HARD error. Walk the un-rendered manifest; any meta key
	// (everything except service:/volumes:) whose scalar carries a `[[ ]]` action is
	// rejected, because parseMeta never renders it.
	if err := lintMetaTemplating(manifest); err != nil {
		return res, err
	}

	// 2. param-type — warn on a default that does not parse as its declared type.
	res.Warnings = append(res.Warnings, lintParamTypes(manifest)...)

	// 3. delimiter-collision — warn on a build/ file that fails to parse (a literal
	// `[[`/`]]` that is not a valid action).
	names := make([]string, 0, len(buildFiles))
	for n := range buildFiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := template.ParseCheck(n, buildFiles[n]); err != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("delimiter-collision: %s contains a literal %q/%q that is not a valid template action (%v)",
					n, template.LeftDelim, template.RightDelim, err))
		}
	}
	sort.Strings(res.Warnings)
	return res, nil
}

// metaParams is a minimal projection of the manifest for the param-type check.
type metaParams struct {
	Params map[string]struct {
		Type    string `yaml:"type"`
		Default any    `yaml:"default"`
	} `yaml:"params"`
}

func lintParamTypes(manifest []byte) []string {
	var mp metaParams
	if err := yaml.Unmarshal(manifest, &mp); err != nil {
		return nil // a malformed manifest is caught elsewhere (Resolve/compose-go)
	}
	var warnings []string
	for _, name := range sortedKeys(mp.Params) {
		p := mp.Params[name]
		if p.Default == nil {
			continue
		}
		def := fmt.Sprint(p.Default)
		switch p.Type {
		case "int":
			if _, err := strconv.Atoi(def); err != nil {
				warnings = append(warnings, fmt.Sprintf("param-type: param %q default %q does not parse as int", name, def))
			}
		case "bool":
			if _, err := strconv.ParseBool(def); err != nil {
				warnings = append(warnings, fmt.Sprintf("param-type: param %q default %q does not parse as bool", name, def))
			}
		}
	}
	return warnings
}

// lintMetaTemplating rejects a `[[ ]]` action anywhere under a meta key.
func lintMetaTemplating(manifest []byte) error {
	var doc map[string]any
	if err := yaml.Unmarshal(manifest, &doc); err != nil {
		return nil // malformed YAML is surfaced by Resolve/compose-go with positions
	}
	for _, key := range sortedKeys(doc) {
		if key == "service" || key == "volumes" {
			continue // the only blocks that may carry actions
		}
		if path := findAction(doc[key]); path != "" {
			return fmt.Errorf("meta-templating: meta key %q contains a template action %q…%q (%s) — meta keys are read un-rendered; actions are only valid under service:/volumes:/build/",
				key, template.LeftDelim, template.RightDelim, joinPath(key, path))
		}
	}
	return nil
}

// findAction returns a sub-path (relative) to the first scalar carrying a template
// action under v, or "" if none.
func findAction(v any) string {
	switch t := v.(type) {
	case string:
		if hasAction(t) {
			return "."
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if sub := findAction(t[k]); sub != "" {
				return joinPath(k, sub)
			}
		}
	case []any:
		for i, e := range t {
			if sub := findAction(e); sub != "" {
				return joinPath(fmt.Sprintf("[%d]", i), sub)
			}
		}
	}
	return ""
}

// hasAction reports whether s contains a `[[ … ]]` action.
func hasAction(s string) bool {
	i := strings.Index(s, template.LeftDelim)
	return i >= 0 && strings.Contains(s[i+len(template.LeftDelim):], template.RightDelim)
}

func joinPath(head, tail string) string {
	if tail == "." {
		return head
	}
	if strings.HasPrefix(tail, "[") {
		return head + tail
	}
	return head + "." + tail
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
