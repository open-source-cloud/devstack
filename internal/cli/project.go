package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/envingest"
)

// newProjectCmd wires the `project` group (spec 30): list workspace projects and
// scaffold a new one. `new` authors a minimal, valid devstack.yaml and registers
// the project in workspace.yaml via a comment-preserving AST merge.
func newProjectCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "List and scaffold projects in the workspace",
	}
	cmd.AddCommand(newProjectListCmd(g), newProjectNewCmd(g))
	return cmd
}

func newProjectListCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the workspace's projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			names := sortedProjectNames(mgr.Model)
			active := resolveActiveProject(mgr.Model, mgr.DB)
			if g.JSON {
				return writeJSON(cmd, map[string]any{"projects": names, "active": active})
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, n := range names {
				marker := " "
				if n == active {
					marker = "*"
				}
				p := mgr.Model.Projects[n]
				svcs := make([]string, 0, len(p.Services))
				for s := range p.Services {
					svcs = append(svcs, s)
				}
				sort.Strings(svcs)
				fmt.Fprintf(tw, "%s %s\t%s\n", marker, n, strings.Join(svcs, ", "))
			}
			return tw.Flush()
		},
	}
}

func newProjectNewCmd(g *GlobalOpts) *cobra.Command {
	var path, template, git string
	var uses []string
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a devstack.yaml and register the project in the workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			if _, exists := mgr.Model.Projects[name]; exists {
				return fmt.Errorf("project %q already exists", name)
			}
			if path == "" {
				path = name
			}
			if template == "" {
				template = "node.vite"
			}
			projDir := filepath.Join(mgr.Model.Root, path)
			dsPath := filepath.Join(projDir, "devstack.yaml")
			if _, err := os.Stat(dsPath); err == nil {
				return fmt.Errorf("%s already exists", dsPath)
			}
			if err := os.MkdirAll(projDir, 0o755); err != nil {
				return err
			}
			// Write the project devstack.yaml.
			if err := os.WriteFile(dsPath, []byte(renderProjectYAML(name, "app", template, uses)), 0o644); err != nil {
				return err
			}
			// Register it in workspace.yaml (comment-preserving AST merge).
			wsPath := filepath.Join(mgr.Model.Root, "workspace.yaml")
			src, err := os.ReadFile(wsPath)
			if err != nil {
				return err
			}
			out, err := envingest.AppendProjectRef(src, name, path, git)
			if err != nil {
				return err
			}
			if err := backupAndWrite(wsPath, out); err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "created %s and registered project %q\n", dsPath, name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "project directory relative to the workspace root (default: the name)")
	cmd.Flags().StringVar(&template, "template", "node.vite", "service template for the first service")
	cmd.Flags().StringArrayVar(&uses, "uses", nil, "shared services the project uses (e.g. workspace.shared.postgres)")
	cmd.Flags().StringVar(&git, "git", "", "git URL to record for `ws clone`")
	return cmd
}

// renderProjectYAML builds a minimal, ordered devstack.yaml.
func renderProjectYAML(name, service, template string, uses []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: devstack/v1\nkind: Project\nname: %s\nservices:\n", name)
	fmt.Fprintf(&b, "  %s:\n    template: %s\n", service, template)
	if len(uses) > 0 {
		b.WriteString("    uses:\n")
		for _, u := range uses {
			fmt.Fprintf(&b, "      - %s\n", u)
		}
	}
	return b.String()
}
