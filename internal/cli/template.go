package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/store"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/template/scaffold"
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
		newTemplateNewCmd(g),
		// Reserved remote-registry verbs (spec 19, v2) — tree-only stubs so
		// help/completions stay consistent (spec 26 / spec 07).
		stub("push", "Publish a template to a remote registry", "v2 (spec 19)"),
		stub("add", "Add a remote template source", "v2 (spec 19)"),
		stub("update", "Update cached remote templates", "v2 (spec 19)"),
		stub("diff", "Diff a local template against its remote", "v2 (spec 19)"),
		stub("verify", "Verify a remote template's signature", "v2 (spec 19)"),
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
			compose, name, warnings, err := lintTemplateDir(args[0])
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"ok": true, "template": name, "warnings": warnings})
			}
			for _, w := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
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
			compose, name, _, err := lintTemplateDir(args[0])
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
// rendered single-service compose, the template name, and any authoring-lint
// warnings. The authoring lints (spec 23: meta-templating/delimiter-collision/
// param-type) run first — a meta-templating action is a hard error — then the
// compose-go render+validation.
func lintTemplateDir(dir string) ([]byte, string, []string, error) {
	src, name, err := template.NewDirSource(dir)
	if err != nil {
		return nil, "", nil, err
	}
	manifest, err := os.ReadFile(filepath.Join(dir, template.TemplateFile))
	if err != nil {
		return nil, name, nil, err
	}
	lr, err := scaffold.Lint(manifest, readBuildFiles(dir))
	if err != nil {
		return nil, name, nil, err
	}
	res, err := template.Resolve(src, name, nil)
	if err != nil {
		return nil, name, lr.Warnings, err
	}
	compose, err := generate.LintResolved(name, res)
	if err != nil {
		return nil, name, lr.Warnings, err
	}
	return compose, name, lr.Warnings, nil
}

// readBuildFiles reads a template dir's build/ tree into a relpath→bytes map
// (keys like "build/Dockerfile"), the shape scaffold.Lint expects. A missing
// build/ dir yields an empty map.
func readBuildFiles(dir string) map[string][]byte {
	out := map[string][]byte{}
	buildDir := filepath.Join(dir, template.BuildDir)
	_ = filepath.WalkDir(buildDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		if data, rerr := os.ReadFile(p); rerr == nil {
			out[filepath.ToSlash(rel)] = data
		}
		return nil
	})
	return out
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
