// Package ide is the editor/IDE generation sink (spec 17). It rides the existing
// deterministic generation pipeline (spec 02) and the workspace service graph
// (spec 01/03) to author, from the same resolved config, the artifacts that point
// editors at devstack's already-generated compose stacks:
//
//   - <repo>/.devcontainer/devcontainer.json — the dockerComposeFile+service+
//     workspaceFolder attach form, so the IDE lands in the SAME tool-owned compose
//     project (devstack-<name>) and shared external network as `devstack up`.
//   - <workspace-root>/<name>.code-workspace — the VS Code multi-root file listing
//     every repo folder (declared order) plus the generated .devstack/ tree.
//   - <repo>/.vscode/launch.json + settings.json — per-repo editor stubs
//     (schema-map + a debugger-attach scaffold).
//
// It is purely a generation sink: no Docker, no ledger, no flock — just typed
// struct → stable JSON marshal → atomic writeIfChanged, exactly like
// internal/generate. Byte-identical config yields byte-identical artifacts
// (spec 17 acceptance #4); a golden + idempotence test asserts it.
package ide

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/version"
)

// Targets selects which editor artifact families to emit. A zero Targets emits
// nothing; the CLI defaults an empty selection to All (both families).
type Targets struct {
	Devcontainer bool // per-repo .devcontainer/devcontainer.json
	VSCode       bool // <name>.code-workspace + per-repo .vscode/{launch,settings}.json
}

// All is the target set produced by `ide --all` (and the no-flag default).
func All() Targets { return Targets{Devcontainer: true, VSCode: true} }

// Generator authors IDE artifacts for one loaded workspace. It is created once and
// asked to Build the selected targets; it never touches Docker/the ledger.
type Generator struct {
	model         *config.Model
	schemaVersion string
}

// Option configures a Generator.
type Option func(*Generator)

// WithSchemaVersion pins the $schema modeline URL to a specific schema version
// (defaults to the running binary's version). Tests inject a fixed value so the
// golden files do not drift with the build stamp.
func WithSchemaVersion(v string) Option {
	return func(g *Generator) {
		if v != "" {
			g.schemaVersion = v
		}
	}
}

// New builds a Generator over the loaded workspace model.
func New(m *config.Model, opts ...Option) *Generator {
	g := &Generator{model: m, schemaVersion: version.Version}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Artifact is one file the generator would author. Data is the exact bytes; Rel is
// the workspace-root-relative slash path used for stable JSON manifests + logging.
type Artifact struct {
	// Path is the absolute filesystem destination.
	Path string `json:"-"`
	// Rel is Path relative to the workspace root, slash-separated (stable output).
	Rel string `json:"path"`
	// Kind is the artifact family: "devcontainer" | "code-workspace" |
	// "launch" | "settings".
	Kind string `json:"kind"`
	// Data is the fully-marshaled file content (byte-deterministic).
	Data []byte `json:"-"`
}

// Build assembles every selected artifact in deterministic order WITHOUT touching
// disk. The caller writes them via Write (or inspects Data for --check/golden).
func (g *Generator) Build(t Targets) ([]Artifact, error) {
	var arts []Artifact

	// Per-repo artifacts, in declared project order (stable diffs).
	for _, pr := range g.model.Workspace.Projects {
		p, ok := g.model.Projects[pr.Name]
		if !ok {
			continue
		}
		dir := g.model.ProjectDir(pr.Name)
		if dir == "" {
			return nil, fmt.Errorf("project %q has no resolved directory", pr.Name)
		}
		if t.Devcontainer {
			a, err := g.buildDevcontainer(pr.Name, p, dir)
			if err != nil {
				return nil, err
			}
			arts = append(arts, a)
		}
		if t.VSCode {
			launch, err := g.buildLaunch(pr.Name, p, dir)
			if err != nil {
				return nil, err
			}
			settings, err := g.buildSettings(dir)
			if err != nil {
				return nil, err
			}
			arts = append(arts, launch, settings)
		}
	}

	// One workspace-root artifact: the multi-root file.
	if t.VSCode {
		a, err := g.buildCodeWorkspace()
		if err != nil {
			return nil, err
		}
		arts = append(arts, a)
	}
	return arts, nil
}

// rel makes an absolute path workspace-root-relative + slash-separated.
func (g *Generator) rel(abs string) string {
	r, err := filepath.Rel(g.model.Root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

// schemaURL is the published JSON-Schema URL pinned to the binary's schema
// version, used as the editor authoring aid (yaml-language-server / yaml.schemas).
// The Go validator remains the source of truth (spec 17, DECISIONS D16).
func (g *Generator) schemaURL() string {
	return fmt.Sprintf(
		"https://raw.githubusercontent.com/open-source-cloud/devstack/v%s/schemas/devstack.schema.json",
		g.schemaVersion,
	)
}

// primaryService picks the service the devcontainer attaches to: the service whose
// name matches the project (the app container) when present, else the first service
// alphabetically. Deterministic and rename-safe (name index, not string guess).
func primaryService(name string, p config.Project) string {
	if _, ok := p.Services[name]; ok {
		return name
	}
	names := sortedServiceNames(p)
	if len(names) == 0 {
		return name
	}
	return names[0]
}

// sortedServiceNames returns a project's service names sorted.
func sortedServiceNames(p config.Project) []string {
	out := make([]string, 0, len(p.Services))
	for s := range p.Services {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// forwardPorts is the sorted, de-duplicated set of declared container ports across
// a project's services — the host ports an editor should forward.
func forwardPorts(p config.Project) []int {
	seen := map[int]bool{}
	var out []int
	for _, s := range sortedServiceNames(p) {
		svc := p.Services[s]
		for _, port := range svc.Ports {
			if port > 0 && !seen[port] {
				seen[port] = true
				out = append(out, port)
			}
		}
	}
	sort.Ints(out)
	return out
}
