package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
)

// newConfigCmd wires `config validate|show` — the M1 DX surface for the
// two-file schema (spec 01). It discovers the workspace by walking up from the
// CWD, loads + validates workspace.yaml and every project's devstack.yaml, and
// reports problems as file:line:col.
func newConfigCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate the workspace configuration",
		Long: "config loads workspace.yaml (found by walking up from the current directory)\n" +
			"and every referenced project's devstack.yaml, validates structure and\n" +
			"cross-references against the workspace graph, and reports errors as file:line:col.",
	}
	cmd.AddCommand(newConfigValidateCmd(g), newConfigShowCmd(g))
	return cmd
}

func newConfigValidateCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the workspace + project configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := loadWorkspace()
			if err != nil {
				return err
			}
			s := summarize(m)
			if g.JSON {
				return writeJSON(cmd, map[string]any{"ok": true, "config": s})
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "ok: workspace %q — %d project(s), %d shared service(s)\n",
					s.Workspace, len(s.Projects), len(s.Shared))
			}
			return nil
		},
	}
}

func newConfigShowCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print a summary of the resolved workspace configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := loadWorkspace()
			if err != nil {
				return err
			}
			s := summarize(m)
			if g.JSON {
				return writeJSON(cmd, s)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "workspace: %s\n", s.Workspace)
			fmt.Fprintf(w, "root:      %s\n", s.Root)
			if len(s.Shared) > 0 {
				fmt.Fprintf(w, "shared:    %v\n", s.Shared)
			}
			for _, p := range s.Projects {
				fmt.Fprintf(w, "project %s:\n", p.Name)
				for _, svc := range p.Services {
					if len(svc.Uses) > 0 {
						fmt.Fprintf(w, "  - %s  (uses: %v)\n", svc.Name, svc.Uses)
					} else {
						fmt.Fprintf(w, "  - %s\n", svc.Name)
					}
				}
			}
			return nil
		},
	}
}

// loadWorkspace discovers and loads the workspace from the current directory.
func loadWorkspace() (*config.Model, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return config.Load(cwd)
}

// configSummary is the stable JSON/human shape for `config` output.
type configSummary struct {
	Workspace string           `json:"workspace"`
	Root      string           `json:"root"`
	Shared    []string         `json:"shared"`
	Projects  []projectSummary `json:"projects"`
}

type projectSummary struct {
	Name     string           `json:"name"`
	Services []serviceSummary `json:"services"`
}

type serviceSummary struct {
	Name     string   `json:"name"`
	Template string   `json:"template"`
	Uses     []string `json:"uses,omitempty"`
}

func summarize(m *config.Model) configSummary {
	s := configSummary{Workspace: m.Workspace.Name, Root: m.Root, Shared: m.SharedNames()}
	sort.Strings(s.Shared)
	for _, pname := range sortedProjectNames(m) {
		p := m.Projects[pname]
		ps := projectSummary{Name: pname}
		for _, sname := range sortedServiceNames(p.Services) {
			svc := p.Services[sname]
			ps.Services = append(ps.Services, serviceSummary{Name: sname, Template: svc.Template, Uses: svc.Uses})
		}
		s.Projects = append(s.Projects, ps)
	}
	return s
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func sortedProjectNames(m *config.Model) []string {
	out := make([]string, 0, len(m.Projects))
	for k := range m.Projects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedServiceNames(svcs map[string]config.Service) []string {
	out := make([]string, 0, len(svcs))
	for k := range svcs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
