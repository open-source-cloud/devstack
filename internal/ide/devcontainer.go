package ide

import (
	"fmt"
	"path/filepath"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/generate"
)

// devcontainer is the typed devcontainer.json model (spec 17 "Devcontainer
// model"). Fields are emitted in declaration order for byte-stable output. It uses
// the dockerComposeFile+service+workspaceFolder ATTACH form — not image/build — so
// the IDE joins the exact container devstack runs (same shared infra, provisioned
// DB, secret env). It never carries a resolved secret value (spec 17 gotcha /
// ARCHITECTURE §7.5): secrets reach the container only via the compose it points at.
type devcontainer struct {
	Schema            string   `json:"$schema"`
	Name              string   `json:"name"`
	DockerComposeFile []string `json:"dockerComposeFile"`
	Service           string   `json:"service"`
	RunServices       []string `json:"runServices"`
	WorkspaceFolder   string   `json:"workspaceFolder"`
	ForwardPorts      []int    `json:"forwardPorts,omitempty"`
	// OverrideCommand:false and ShutdownAction:"none" keep the IDE from hijacking
	// the entrypoint or tearing down the shared stack (spec 17).
	OverrideCommand   bool   `json:"overrideCommand"`
	ShutdownAction    string `json:"shutdownAction"`
	PostCreateCommand string `json:"postCreateCommand"`
}

// devcontainerSchema is the well-known Dev Containers metadata schema URL (the
// authoring aid for devcontainer.json itself, distinct from the devstack config
// schema modeline).
const devcontainerSchema = "https://raw.githubusercontent.com/devcontainers/spec/main/schemas/devContainer.base.schema.json"

// buildDevcontainer authors <repo>/.devcontainer/devcontainer.json for one project.
func (g *Generator) buildDevcontainer(name string, p config.Project, dir string) (Artifact, error) {
	// dockerComposeFile is relative to the .devcontainer/ directory and points at
	// the generated project compose (which carries `name: devstack-<name>` and the
	// external devstack_shared network — the devcontainer inherits both).
	composeRel := filepath.ToSlash(filepath.Join("..", generate.GenDir, generate.ComposeFile))

	dc := devcontainer{
		Schema:            devcontainerSchema,
		Name:              generate.ProjectStackName(name),
		DockerComposeFile: []string{composeRel},
		Service:           primaryService(name, p),
		RunServices:       sortedServiceNames(p),
		WorkspaceFolder:   workspaceFolder,
		ForwardPorts:      forwardPorts(p),
		OverrideCommand:   false,
		ShutdownAction:    "none",
		// Opt-in reconcile if the folder was opened cold (spec 17): devstack still
		// owns network-ensure + shared services; this only re-registers refs.
		PostCreateCommand: fmt.Sprintf("devstack up %s --skip-clone --no-hooks", name),
	}
	data, err := marshalJSON(dc)
	if err != nil {
		return Artifact{}, err
	}
	abs := filepath.Join(dir, ".devcontainer", "devcontainer.json")
	return Artifact{Path: abs, Rel: g.rel(abs), Kind: "devcontainer", Data: data}, nil
}

// workspaceFolder is the in-container mount the IDE opens. devstack's project
// templates bind the repo to /workspace; a template that mounts elsewhere is a
// future refinement (spec 17: derive from the typed mount, not a default).
const workspaceFolder = "/workspace"
