package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/store"
	"github.com/open-source-cloud/devstack/internal/template"
)

// newTemplateCmd wires `devstack template list|lint|test|init` — the M1 template
// authoring surface (spec 02). Built-ins are embedded; lint/test/init operate on
// a template directory on disk.
func newTemplateCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Author, list and validate service templates",
	}
	cmd.AddCommand(
		newTemplateListCmd(g),
		newTemplateLintCmd(g),
		newTemplateTestCmd(g),
		newTemplateInitCmd(g),
	)
	return cmd
}

func newTemplateListCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the built-in service templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src := builtinSource() // store templates (if any) + embedded built-ins
			names := src.List()
			type item struct {
				Name        string `json:"name"`
				Extends     string `json:"extends,omitempty"`
				Provides    string `json:"provides,omitempty"`
				Description string `json:"description,omitempty"`
			}
			items := make([]item, 0, len(names))
			for _, n := range names {
				d, err := template.Describe(src, n)
				if err != nil {
					return err
				}
				items = append(items, item{n, d.Extends, d.Provides, d.Description})
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"templates": items})
			}
			w := cmd.OutOrStdout()
			for _, it := range items {
				suffix := ""
				if it.Extends != "" {
					suffix = fmt.Sprintf("  (extends %s)", it.Extends)
				}
				fmt.Fprintf(w, "%-20s %s%s\n", it.Name, it.Description, suffix)
			}
			return nil
		},
	}
}

func newTemplateLintCmd(g *GlobalOpts) *cobra.Command {
	var show bool
	cmd := &cobra.Command{
		Use:   "lint <dir>",
		Short: "Render a template with defaults and validate it through compose-go",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			compose, name, err := lintTemplateDir(args[0])
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"ok": true, "template": name})
			}
			if show {
				fmt.Fprintf(cmd.OutOrStdout(), "%s", compose)
				return nil
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "ok: template %q renders and validates\n", name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&show, "show", false, "print the rendered single-service compose document")
	return cmd
}

func newTemplateTestCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "test <dir>",
		Short: "Render a template with defaults and assert it validates (and matches golden, if present)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			compose, name, err := lintTemplateDir(args[0])
			if err != nil {
				return err
			}
			// Optional golden: <dir>/golden.yaml is compared byte-for-byte when present.
			golden := filepath.Join(args[0], "golden.yaml")
			if want, err := os.ReadFile(golden); err == nil {
				if string(want) != string(compose) {
					return fmt.Errorf("template %q: output does not match %s", name, golden)
				}
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "ok: template %q passed\n", name)
			}
			return nil
		},
	}
}

func newTemplateInitCmd(g *GlobalOpts) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Scaffold a new service template directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			target := filepath.Join(dir, name)
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("refusing to overwrite existing %s", target)
			}
			if err := scaffoldTemplate(target, name); err != nil {
				return err
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "scaffolded template %q at %s\n", name, target)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", store.TemplatesPath(), "parent directory to create the template in (default: the store templates dir)")
	return cmd
}

// lintTemplateDir resolves and validates a template directory, returning the
// rendered single-service compose and the template name.
func lintTemplateDir(dir string) ([]byte, string, error) {
	src, name, err := template.NewDirSource(dir)
	if err != nil {
		return nil, "", err
	}
	res, err := template.Resolve(src, name, nil)
	if err != nil {
		return nil, name, err
	}
	compose, err := generate.LintResolved(name, res)
	if err != nil {
		return nil, name, err
	}
	return compose, name, nil
}

// scaffoldTemplate writes a minimal template.yaml + build/Dockerfile skeleton.
func scaffoldTemplate(target, name string) error {
	if err := os.MkdirAll(filepath.Join(target, "build"), 0o755); err != nil {
		return err
	}
	manifest := "schemaVersion: 1\n" +
		"description: \"" + name + " service.\"\n" +
		"params:\n" +
		"  version:\n" +
		"    type: string\n" +
		"    default: \"latest\"\n\n" +
		"service:\n" +
		"  build:\n" +
		"    context: build\n" +
		"    dockerfile: Dockerfile\n" +
		"  restart: unless-stopped\n"
	dockerfile := "# syntax=docker/dockerfile:1\n" +
		"# Template actions use double-square-bracket delimiters; $-syntax is literal.\n" +
		"FROM alpine:[[ .params.version ]]\n" +
		"WORKDIR /app\n"
	files := map[string]string{
		filepath.Join(target, "template.yaml"):       manifest,
		filepath.Join(target, "build", "Dockerfile"): dockerfile,
	}
	for p, content := range files {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
