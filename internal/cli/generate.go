package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/templates"
)

// builtinSource is the template source used by generation and the template
// tooling: custom templates in the store (~/.devstack/templates) override the
// embedded built-ins by name; the embedded set is always the fallback.
func builtinSource() template.TemplateSource {
	embedded := template.NewFSSource(templates.FS)
	if dir := userTemplatesDir(); dir != "" {
		return template.NewChainSource(template.NewFSSource(os.DirFS(dir)), embedded)
	}
	return embedded
}

// newGenerateCmd wires `devstack generate` — the M1 deterministic pipeline entry
// point (spec 02). It loads + validates the workspace, renders the shared stack
// and every project stack from templates, validates each through compose-go, and
// writes byte-deterministic artifacts under each stack's .devstack/ directory.
func newGenerateCmd(g *GlobalOpts) *cobra.Command {
	var (
		project string
		profile string
		check   bool
	)
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Render compose + build artifacts from config and templates",
		Long: "generate renders the shared-services stack and every project stack from\n" +
			"their templates, resolves ${ref}/${env}/${self} and env.import against the\n" +
			"workspace service graph, validates each document through compose-go, and writes\n" +
			"deterministic artifacts under each stack's .devstack/ directory.\n\n" +
			"With --check, nothing is written: the documents are generated and validated and\n" +
			"the command reports whether the on-disk artifacts are up to date (CI-friendly).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := loadWorkspace()
			if err != nil {
				return err
			}
			gen, err := generate.New(m, builtinSource(), generate.WithProfile(profile))
			if err != nil {
				return err
			}

			var stacks []*generate.Stack
			if project != "" {
				st, err := gen.GenerateProject(project)
				if err != nil {
					return err
				}
				stacks = []*generate.Stack{st}
			} else {
				stacks, err = gen.GenerateAll()
				if err != nil {
					return err
				}
			}

			if check {
				return reportCheck(cmd, g, stacks)
			}
			return writeStacks(cmd, g, stacks)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "generate only this project's stack (default: all)")
	cmd.Flags().StringVar(&profile, "profile", "", "env-overlay profile for ${profile} (default: workspace profiles.default)")
	cmd.Flags().BoolVar(&check, "check", false, "validate and report drift without writing (CI)")
	return cmd
}

// writeStacks materializes every stack and reports what changed.
func writeStacks(cmd *cobra.Command, g *GlobalOpts, stacks []*generate.Stack) error {
	results := make([]generate.WriteResult, 0, len(stacks))
	for _, st := range stacks {
		r, err := st.Write()
		if err != nil {
			return err
		}
		results = append(results, r)
	}
	if g.JSON {
		return writeJSON(cmd, map[string]any{"ok": true, "stacks": results})
	}
	if g.Quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	for _, r := range results {
		fmt.Fprintf(w, "%s → %s%s\n", r.Stack, r.ComposePath, changedSuffix(r.ComposeChanged))
		for _, f := range r.FilesChanged {
			fmt.Fprintf(w, "  wrote %s\n", f)
		}
		if len(r.RebuildNeeded) > 0 {
			sort.Strings(r.RebuildNeeded)
			fmt.Fprintf(w, "  rebuild (--no-cache): %v\n", r.RebuildNeeded)
		}
	}
	return nil
}

// reportCheck regenerates and compares against disk WITHOUT writing, exiting
// non-zero when any artifact is stale (so `generate --check` gates CI).
func reportCheck(cmd *cobra.Command, g *GlobalOpts, stacks []*generate.Stack) error {
	type drift struct {
		Stack    string `json:"stack"`
		Path     string `json:"path"`
		UpToDate bool   `json:"upToDate"`
	}
	var drifts []drift
	allOK := true
	for _, st := range stacks {
		ok := st.UpToDate()
		if !ok {
			allOK = false
		}
		drifts = append(drifts, drift{Stack: st.Name, Path: st.OutDir, UpToDate: ok})
	}
	if g.JSON {
		if err := writeJSON(cmd, map[string]any{"ok": allOK, "stacks": drifts}); err != nil {
			return err
		}
	} else if !g.Quiet {
		w := cmd.OutOrStdout()
		for _, d := range drifts {
			state := "up to date"
			if !d.UpToDate {
				state = "STALE (run `generate`)"
			}
			fmt.Fprintf(w, "%s: %s\n", d.Stack, state)
		}
	}
	if !allOK {
		return fmt.Errorf("generated artifacts are stale; run `%s generate`", rootName(cmd))
	}
	return nil
}

func changedSuffix(changed bool) string {
	if changed {
		return "  (updated)"
	}
	return "  (unchanged)"
}
