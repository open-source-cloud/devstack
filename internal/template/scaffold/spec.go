// Package scaffold is the deterministic builder behind `devstack template new`
// (spec 23): it turns a typed authoring Spec into an on-disk template bundle
// (template.yaml + an optional build/ tree + an optional golden fixture). It is the
// shared, UI-agnostic core — both the flag path and the Bubble Tea wizard populate
// a Spec, call Build, then WriteBundle.
//
// It is strictly additive over internal/template: it produces nothing the engine
// cannot already consume, introduces no new template feature, and emits byte-stable
// bytes for a given Spec (sorted keys, ordered goccy MapSlice — never marshal a Go
// map). It takes no lock, touches no ledger, starts no Docker, and resolves no
// secret:// ref. NOTE: this is distinct from internal/scaffold (the workspace
// builder behind `devstack init`).
package scaffold

// Kind is the template shape. An app is buildable (service.build + a build/
// Dockerfile, never provides:); an engine is image-based (image: + provides/
// exports/defaultPort/volumes, never a build/ tree — generate.buildSharedService
// rejects build: on a shared service). App-vs-engine is a compile-time branch in
// Build, so an engine can never structurally acquire a build context.
type Kind string

const (
	KindApp    Kind = "app"
	KindEngine Kind = "engine"
)

// Param is one typed, defaulted parameter declaration (mirrors
// template.ParamSpec). Type is advisory in v1 (string|int|bool).
type Param struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Description string `yaml:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
}

// Spec is the complete authoring input for one template. It contains no maps, so
// yaml.Marshal of a Spec is deterministic (struct field order) — that is what makes
// the `--print-spec` ⇒ `--from` round-trip byte-stable.
type Spec struct {
	Kind        Kind    `yaml:"kind"`
	Name        string  `yaml:"name"`
	Extends     string  `yaml:"extends,omitempty"`
	Description string  `yaml:"description,omitempty"`
	BaseImage   string  `yaml:"baseImage,omitempty"`
	Params      []Param `yaml:"params,omitempty"`

	// engine-only:
	Provides    string   `yaml:"provides,omitempty"`
	Exports     []string `yaml:"exports,omitempty"`
	DefaultPort int      `yaml:"defaultPort,omitempty"`
	Volumes     []string `yaml:"volumes,omitempty"`

	// app-only:
	Entrypoint bool     `yaml:"entrypoint,omitempty"`
	ExtraBuild []string `yaml:"extraBuild,omitempty"`

	Golden bool `yaml:"golden,omitempty"`
}

// Bundle is a relpath ("template.yaml", "build/Dockerfile", …) → bytes map, emitted
// byte-stably.
type Bundle map[string][]byte
