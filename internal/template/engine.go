// Package template is the text-template engine and template-resolution layer for
// the generation pipeline (spec 02, ARCHITECTURE §6). It does TWO jobs and keeps
// them separate:
//
//   - renderText  — render an unstructured artifact (Dockerfile, entrypoint,
//     proxy conf) verbatim. Used for the files under a template's build/ tree.
//   - renderYAML  — render a YAML *template fragment* (the service: block in a
//     template.yaml) then decode it into a map[string]any. Used ONLY for template
//     fragments, never for the user's hand-authored config.
//
// Custom delimiters `[[ ]]` are used so template syntax never collides with the
// shell `${VAR}` / Dockerfile `$TAG` / compose `${VAR:-default}` that legitimately
// appear, untouched, inside the rendered text (spec 02 acceptance #2).
//
// Engine choice (OPEN-QUESTIONS Q-T → Option A): stdlib text/template plus a small,
// deliberately DETERMINISTIC FuncMap — no clock/random/uuid helpers, because
// byte-identical output for identical input is a hard requirement (spec 02
// acceptance #3). Missing keys are an error (StrictUndefined), so a missing
// required param fails fast rather than rendering an empty string (acceptance #5).
package template

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/goccy/go-yaml"
)

// LeftDelim and RightDelim are the custom action delimiters.
const (
	LeftDelim  = "[["
	RightDelim = "]]"
)

// RenderText renders src as a text template with the given data, returning the
// rendered bytes. name is used only in error messages. A reference to a missing
// map key (e.g. an undeclared param) is a hard error.
func RenderText(name string, src []byte, data any) ([]byte, error) {
	t, err := template.New(name).
		Delims(LeftDelim, RightDelim).
		Funcs(funcMap()).
		Option("missingkey=error").
		Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("template %s: parse: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template %s: render: %w", name, err)
	}
	return buf.Bytes(), nil
}

// ParseCheck parses src as a template with the production delimiters + FuncMap but
// does NOT execute it, returning any parse error. The authoring lints (spec 23) use
// it to detect delimiter collisions: a literal "[[" or "]]" in a build/ file that is
// not a valid template action makes Parse fail.
func ParseCheck(name string, src []byte) error {
	if _, err := template.New(name).
		Delims(LeftDelim, RightDelim).
		Funcs(funcMap()).
		Option("missingkey=error").
		Parse(string(src)); err != nil {
		return fmt.Errorf("template %s: parse: %w", name, err)
	}
	return nil
}

// RenderYAML renders src as a text template (so params are substituted) and then
// decodes the result into a map[string]any. It is for template *fragments* only.
// An empty render decodes to an empty (non-nil) map.
func RenderYAML(name string, src []byte, data any) (map[string]any, error) {
	rendered, err := RenderText(name, src, data)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(rendered)) == 0 {
		return out, nil
	}
	if err := yaml.Unmarshal(rendered, &out); err != nil {
		return nil, fmt.Errorf("template %s: rendered output is not a YAML mapping: %w", name, err)
	}
	return out, nil
}
