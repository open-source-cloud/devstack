package ide

import (
	"path/filepath"

	"github.com/open-source-cloud/devstack/internal/config"
)

// codeWorkspace is the typed VS Code multi-root .code-workspace model (spec 17).
// It lists each repo folder in the workspace's DECLARED order (stable diffs) plus a
// virtual entry for the generated .devstack/ tree, and carries workspace-level
// yaml.schemas mappings + the Dev Containers extension recommendation.
type codeWorkspace struct {
	Folders    []cwFolder   `json:"folders"`
	Settings   cwSettings   `json:"settings"`
	Extensions cwExtensions `json:"extensions"`
}

type cwFolder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type cwSettings struct {
	// YAMLSchemas maps the published schema URL to the config globs it validates.
	YAMLSchemas map[string][]string `json:"yaml.schemas"`
}

type cwExtensions struct {
	Recommendations []string `json:"recommendations"`
}

// devContainersExtension is the VS Code Dev Containers extension id.
const devContainersExtension = "ms-vscode-remote.remote-containers"

// buildCodeWorkspace authors <workspace-root>/<name>.code-workspace.
func (g *Generator) buildCodeWorkspace() (Artifact, error) {
	folders := make([]cwFolder, 0, len(g.model.Workspace.Projects)+1)
	for _, pr := range g.model.Workspace.Projects {
		folders = append(folders, cwFolder{Name: pr.Name, Path: filepath.ToSlash(pr.Path)})
	}
	// The generated artifacts / shared logs tree, always last for a stable order.
	folders = append(folders, cwFolder{Name: "devstack (generated)", Path: ".devstack"})

	cw := codeWorkspace{
		Folders: folders,
		Settings: cwSettings{
			YAMLSchemas: map[string][]string{
				g.schemaURL(): {"workspace.yaml", "**/devstack.yaml"},
			},
		},
		Extensions: cwExtensions{Recommendations: []string{devContainersExtension}},
	}
	data, err := marshalJSON(cw)
	if err != nil {
		return Artifact{}, err
	}
	abs := filepath.Join(g.model.Root, g.model.Workspace.Name+".code-workspace")
	return Artifact{Path: abs, Rel: g.rel(abs), Kind: "code-workspace", Data: data}, nil
}

// launchConfig is the VS Code launch.json model (spec 17 debugger attach). This
// scoped build emits a valid, empty-configurations stub: debug-port allocation runs
// through the flock-guarded workspace allocator, which is out of this generation
// sink's scope (no ledger/flock here) — so per-service attach configs are a
// follow-up, and we never invent a wrong port.
type launchConfig struct {
	Version        string           `json:"version"`
	Configurations []map[string]any `json:"configurations"`
}

// buildLaunch authors <repo>/.vscode/launch.json.
func (g *Generator) buildLaunch(name string, p config.Project, dir string) (Artifact, error) {
	_ = name
	_ = p
	lc := launchConfig{Version: "0.2.0", Configurations: []map[string]any{}}
	data, err := marshalJSON(lc)
	if err != nil {
		return Artifact{}, err
	}
	abs := filepath.Join(dir, ".vscode", "launch.json")
	return Artifact{Path: abs, Rel: g.rel(abs), Kind: "launch", Data: data}, nil
}

// vscodeSettings is the per-repo .vscode/settings.json model (spec 17). It wires
// yaml-language-server to the published schema for this repo's devstack.yaml — an
// authoring aid only; the Go validator stays the source of truth (DECISIONS D16).
type vscodeSettings struct {
	YAMLSchemas map[string][]string `json:"yaml.schemas"`
}

// buildSettings authors <repo>/.vscode/settings.json.
func (g *Generator) buildSettings(dir string) (Artifact, error) {
	vs := vscodeSettings{
		YAMLSchemas: map[string][]string{
			g.schemaURL(): {"devstack.yaml"},
		},
	}
	data, err := marshalJSON(vs)
	if err != nil {
		return Artifact{}, err
	}
	abs := filepath.Join(dir, ".vscode", "settings.json")
	return Artifact{Path: abs, Rel: g.rel(abs), Kind: "settings", Data: data}, nil
}
