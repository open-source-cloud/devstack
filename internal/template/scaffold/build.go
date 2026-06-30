package scaffold

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Build turns a Spec into a deterministic Bundle. It is pure: the same Spec always
// yields byte-identical files — there is no clock/uuid/random anywhere (the engine
// FuncMap omits them by design). App-vs-engine is a compile-time branch, so an
// engine can never acquire a build/ tree and an app can never emit provides:.
//
// Build does NOT emit the golden fixture (golden.yaml) — that is the rendered
// compose, produced by the caller through the real template.Resolve +
// generate.LintResolved path and added to the Bundle when Spec.Golden is set.
func Build(s Spec) (Bundle, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	b := Bundle{}
	switch s.Kind {
	case KindApp:
		b["template.yaml"] = appManifest(s)
		b["build/Dockerfile"] = appDockerfile(s)
		if s.Entrypoint {
			b["build/entrypoint.sh"] = entrypointScript()
		}
		for _, name := range s.ExtraBuild {
			b["build/"+path.Clean(name)] = extraBuildFile(name)
		}
	case KindEngine:
		b["template.yaml"] = engineManifest(s)
	}
	return b, nil
}

// Validate enforces the app/engine split structurally so the wizard and the flag
// path cannot author an inconsistent bundle.
func (s Spec) Validate() error {
	switch s.Kind {
	case KindApp:
		if s.BaseImage == "" {
			return fmt.Errorf("app template %q: --base-image is required (it becomes the Dockerfile FROM)", s.Name)
		}
		if s.Provides != "" || len(s.Exports) > 0 {
			return fmt.Errorf("app template %q: provides/exports are engine-only (an app is buildable, not a shared service)", s.Name)
		}
		for _, name := range s.ExtraBuild {
			if !safeRel(name) {
				return fmt.Errorf("app template %q: invalid extra build file %q (must be a single relative path, no slash/..)", s.Name, name)
			}
		}
	case KindEngine:
		if s.BaseImage == "" {
			return fmt.Errorf("engine template %q: --base-image is required (it becomes image:)", s.Name)
		}
		if s.Entrypoint || len(s.ExtraBuild) > 0 {
			return fmt.Errorf("engine template %q: a build/ tree is app-only (shared services are image-based)", s.Name)
		}
	default:
		return fmt.Errorf("template %q: kind must be %q or %q", s.Name, KindApp, KindEngine)
	}
	for _, p := range s.Params {
		if p.Name == "" {
			return fmt.Errorf("template %q: a param has an empty name", s.Name)
		}
	}
	return nil
}

// appManifest emits an app template.yaml: meta as literal YAML, a service.build
// block, and one build arg per STRING-typed param (the version-style params that
// Docker build args naturally carry). Int/bool params stay declared but are not
// wired as build args (they are runtime config). Actions appear ONLY under
// service: — never in a meta key.
func appManifest(s Spec) []byte {
	doc := metaHead(s)
	doc = appendParams(doc, s.Params)

	build := yaml.MapSlice{
		{Key: "context", Value: "build"},
		{Key: "dockerfile", Value: "Dockerfile"},
	}
	if args := buildArgs(s.Params); len(args) > 0 {
		build = append(build, yaml.MapItem{Key: "args", Value: args})
	}
	service := yaml.MapSlice{
		{Key: "build", Value: build},
		{Key: "restart", Value: "unless-stopped"},
	}
	doc = append(doc, yaml.MapItem{Key: "service", Value: service})
	return marshalDoc(doc)
}

// engineManifest emits an image-based shared-engine template.yaml: image: +
// provides/exports/defaultPort + an optional top-level volumes: block. No build/
// tree is ever produced.
func engineManifest(s Spec) []byte {
	doc := metaHead(s)
	if s.Provides != "" {
		doc = append(doc, yaml.MapItem{Key: "provides", Value: s.Provides})
	}
	if len(s.Exports) > 0 {
		doc = append(doc, yaml.MapItem{Key: "exports", Value: append([]string(nil), s.Exports...)})
	}
	if s.DefaultPort > 0 {
		doc = append(doc, yaml.MapItem{Key: "defaultPort", Value: s.DefaultPort})
	}
	doc = appendParams(doc, s.Params)

	service := yaml.MapSlice{
		{Key: "image", Value: s.BaseImage},
		{Key: "restart", Value: "unless-stopped"},
	}
	if len(s.Volumes) > 0 {
		vols := append([]string(nil), s.Volumes...)
		sort.Strings(vols)
		mounts := make([]string, 0, len(vols))
		for _, v := range vols {
			mounts = append(mounts, v+":/var/lib/"+v)
		}
		service = append(service, yaml.MapItem{Key: "volumes", Value: mounts})
	}
	doc = append(doc, yaml.MapItem{Key: "service", Value: service})

	if len(s.Volumes) > 0 {
		vols := append([]string(nil), s.Volumes...)
		sort.Strings(vols)
		top := yaml.MapSlice{}
		for _, v := range vols {
			top = append(top, yaml.MapItem{Key: v, Value: yaml.MapSlice{}})
		}
		doc = append(doc, yaml.MapItem{Key: "volumes", Value: top})
	}
	return marshalDoc(doc)
}

// metaHead emits the leading meta keys common to both kinds: schemaVersion, then
// optional extends/description. All literal YAML — never templated.
func metaHead(s Spec) yaml.MapSlice {
	doc := yaml.MapSlice{{Key: "schemaVersion", Value: 1}}
	if s.Extends != "" {
		doc = append(doc, yaml.MapItem{Key: "extends", Value: s.Extends})
	}
	if s.Description != "" {
		doc = append(doc, yaml.MapItem{Key: "description", Value: s.Description})
	}
	return doc
}

// appendParams appends a sorted params: block. Each entry carries its declared
// type/default/description/required as literal scalars.
func appendParams(doc yaml.MapSlice, params []Param) yaml.MapSlice {
	if len(params) == 0 {
		return doc
	}
	byName := map[string]Param{}
	names := make([]string, 0, len(params))
	for _, p := range params {
		byName[p.Name] = p
		names = append(names, p.Name)
	}
	sort.Strings(names)
	block := yaml.MapSlice{}
	for _, n := range names {
		p := byName[n]
		entry := yaml.MapSlice{}
		if p.Type != "" {
			entry = append(entry, yaml.MapItem{Key: "type", Value: p.Type})
		}
		if p.Default != "" {
			entry = append(entry, yaml.MapItem{Key: "default", Value: p.Default})
		}
		if p.Description != "" {
			entry = append(entry, yaml.MapItem{Key: "description", Value: p.Description})
		}
		if p.Required {
			entry = append(entry, yaml.MapItem{Key: "required", Value: true})
		}
		block = append(block, yaml.MapItem{Key: n, Value: entry})
	}
	return append(doc, yaml.MapItem{Key: "params", Value: block})
}

// buildArgs maps each string-typed param to a build arg (UPPER_SNAKE name →
// "[[ .params.<name> ]]"), sorted by arg name for deterministic output.
func buildArgs(params []Param) yaml.MapSlice {
	type kv struct{ arg, param string }
	var pairs []kv
	for _, p := range params {
		if p.Type != "" && p.Type != "string" {
			continue
		}
		pairs = append(pairs, kv{arg: upperSnake(p.Name), param: p.Name})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].arg < pairs[j].arg })
	out := yaml.MapSlice{}
	for _, kv := range pairs {
		out = append(out, yaml.MapItem{Key: kv.arg, Value: "[[ .params." + kv.param + " ]]"})
	}
	return out
}

// appDockerfile seeds a build/Dockerfile: a `# syntax` header, one `ARG NAME=
// [[ .params.x ]]` line per string param (the only template actions), a literal
// FROM, and an optional entrypoint wiring. Author $-syntax stays literal.
func appDockerfile(s Spec) []byte {
	var b strings.Builder
	b.WriteString("# syntax=docker/dockerfile:1\n")
	b.WriteString("# Template actions use double-square-bracket delimiters; $-syntax stays literal.\n")
	args := buildArgs(s.Params)
	for _, a := range args {
		b.WriteString(fmt.Sprintf("ARG %s=%s\n", a.Key, a.Value))
	}
	b.WriteString("FROM " + s.BaseImage + "\n")
	b.WriteString("WORKDIR /app\n")
	if s.Entrypoint {
		b.WriteString("COPY build/entrypoint.sh /entrypoint.sh\n")
		b.WriteString("RUN chmod +x /entrypoint.sh\n")
		b.WriteString("ENTRYPOINT [\"/entrypoint.sh\"]\n")
	}
	return []byte(b.String())
}

// entrypointScript seeds a build/entrypoint.sh. Author shell stays literal —
// ${PORT:-3000} is NOT a template action (spec 02 delimiter non-collision).
func entrypointScript() []byte {
	return []byte("#!/bin/sh\n" +
		"# Authored by `devstack template new`. Author shell stays literal —\n" +
		"# ${PORT:-3000} is NOT a template action.\n" +
		"set -e\n" +
		"\n" +
		"exec \"$@\"\n")
}

// extraBuildFile seeds an arbitrary build/<name> config file with a placeholder.
func extraBuildFile(name string) []byte {
	return []byte("# " + path.Base(name) + " — authored by `devstack template new`; edit freely.\n")
}

// marshalDoc renders an ordered MapSlice to YAML (never a Go map: goccy randomizes
// map order and renders 16 as 16.0). Output is normalized to a single trailing LF.
func marshalDoc(doc yaml.MapSlice) []byte {
	out, err := yaml.Marshal(doc)
	if err != nil {
		// MapSlice of scalars/strings cannot fail to marshal; treat as programmer error.
		panic(fmt.Sprintf("scaffold: marshal template.yaml: %v", err))
	}
	return out
}

// upperSnake converts a camelCase/dotted/dashed param name to UPPER_SNAKE_CASE
// (bunVersion → BUN_VERSION, php.version → PHP_VERSION).
func upperSnake(s string) string {
	var b strings.Builder
	prevLower := false
	for i, r := range s {
		switch {
		case r == '.' || r == '-' || r == '_' || r == ' ':
			b.WriteByte('_')
			prevLower = false
		case r >= 'A' && r <= 'Z':
			if prevLower && i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			prevLower = false
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
			prevLower = true
		default:
			b.WriteRune(r)
			prevLower = (r >= '0' && r <= '9')
		}
	}
	return collapseUnderscore(b.String())
}

func collapseUnderscore(s string) string {
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	return strings.Trim(s, "_")
}

// safeRel reports whether name is a single safe relative build-file path (no
// absolute, no "..", cleans to itself).
func safeRel(name string) bool {
	if name == "" || filepath.IsAbs(name) {
		return false
	}
	clean := path.Clean(name)
	if clean != name || clean == "." || strings.HasPrefix(clean, "..") {
		return false
	}
	return !strings.Contains(clean, "../")
}
