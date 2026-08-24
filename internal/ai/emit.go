package ai

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Targets selects which families of agent-integration files to emit. A zero
// Targets emits nothing; the CLI defaults an empty selection to All.
type Targets struct {
	Skills   bool // .claude/skills/** — Claude Code skills
	AgentsMD bool // AGENTS.md + CLAUDE.md — the cross-tool instruction files
	MCP      bool // .mcp.json — the MCP server registration
}

// All is the target set produced by `ai install` with no target flag.
func All() Targets { return Targets{Skills: true, AgentsMD: true, MCP: true} }

// Any reports whether any target is selected.
func (t Targets) Any() bool { return t.Skills || t.AgentsMD || t.MCP }

// Generator authors the agent-integration files for one repository.
//
// It deliberately takes NO *config.Model, which is where it parts company with
// internal/ide. These files get committed, so encoding a snapshot of the current
// projects and shared services would go stale the moment someone adds a project,
// with no CI anywhere to catch it. Live facts belong to `devstack status --json`,
// which the agent runs. The happy consequence is that the emitted content is
// useful BEFORE a workspace exists — exactly when an agent is learning
// `devstack init` — and that the golden tests are trivially hermetic.
type Generator struct {
	root    string
	binary  string
	version string
}

// Option configures a Generator.
type Option func(*Generator)

// WithRoot sets the directory the files are written under (the workspace root,
// or the current directory when there is no workspace).
func WithRoot(dir string) Option {
	return func(g *Generator) {
		if dir != "" {
			g.root = dir
		}
	}
}

// WithBinary sets the command name written into the emitted files, so an
// installation invoked through an argv[0] alias (rq, uranus) documents itself
// correctly.
func WithBinary(name string) Option {
	return func(g *Generator) {
		if name != "" {
			g.binary = name
		}
	}
}

// WithVersion pins the version stamp recorded in the skill frontmatter. Tests
// inject a fixed value so goldens do not drift with the build stamp.
func WithVersion(v string) Option {
	return func(g *Generator) {
		if v != "" {
			g.version = v
		}
	}
}

// New builds a Generator. Defaults: the current directory, the binary name
// "devstack", and an unset version.
func New(opts ...Option) *Generator {
	g := &Generator{root: ".", binary: "devstack", version: "dev"}
	for _, o := range opts {
		o(g)
	}
	return g
}

// Build assembles every selected artifact. It never touches the disk — the two
// merge modes that preserve user content compose against the file at Write time.
func (g *Generator) Build(t Targets) ([]Artifact, error) {
	var arts []Artifact
	if t.Skills {
		skills, err := g.buildSkills()
		if err != nil {
			return nil, err
		}
		arts = append(arts, skills...)
	}
	if t.AgentsMD {
		md, err := g.buildAgentsMD()
		if err != nil {
			return nil, err
		}
		arts = append(arts, md...)
	}
	if t.MCP {
		mcp, err := g.buildMCPConfig()
		if err != nil {
			return nil, err
		}
		arts = append(arts, mcp...)
	}
	return arts, nil
}

// artifact builds one Artifact with its repo-relative label filled in.
func (g *Generator) artifact(kind string, merge MergeMode, data []byte, parts ...string) Artifact {
	abs := filepath.Join(append([]string{g.root}, parts...)...)
	return Artifact{
		Path:  abs,
		Rel:   strings.Join(parts, "/"),
		Kind:  kind,
		Data:  data,
		Merge: merge,
	}
}

// rewriteBinary substitutes the invoked binary name into authored content. The
// pack is written for "devstack"; an aliased installation gets its own name so
// every command in the emitted files is copy-pasteable.
func (g *Generator) rewriteBinary(body []byte) []byte {
	if g.binary == "" || g.binary == "devstack" {
		return body
	}
	return []byte(strings.ReplaceAll(string(body), "devstack ", g.binary+" "))
}

// errUnknownTarget reports a target name the CLI does not recognize.
func errUnknownTarget(name string) error {
	return fmt.Errorf("unknown target %q (available: skills, agents, mcp)", name)
}

// ParseTargets resolves a comma-separated --target selection.
func ParseTargets(spec string) (Targets, error) {
	var t Targets
	for _, raw := range strings.Split(spec, ",") {
		switch strings.TrimSpace(strings.ToLower(raw)) {
		case "":
			continue
		case "all":
			t = All()
		case "skills", "claude":
			t.Skills = true
		case "agents", "agents.md", "agentsmd":
			t.AgentsMD = true
		case "mcp":
			t.MCP = true
		default:
			return Targets{}, errUnknownTarget(strings.TrimSpace(raw))
		}
	}
	return t, nil
}
