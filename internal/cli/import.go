package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/migrate"
)

// newImportCmd wires `devstack import <project.yaml>` (spec 14 §import): convert a
// legacy devdock single-file project.yaml into a workspace.yaml + per-repo
// devstack.yaml split, plus a lossless-or-loud conversion report. No-clobber by
// default; `--force` backs up originals first; `--dry-run` writes nothing.
func newImportCmd(g *GlobalOpts) *cobra.Command {
	var (
		dryRun bool
		outDir string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "import <path/to/project.yaml>",
		Short: "Convert a legacy devdock project.yaml into workspace.yaml + per-repo devstack.yaml",
		Long: "import reads an old devdock single-file project.yaml and emits the clean-slate\n" +
			"two-file schema — a workspace.yaml (shared layer) plus a devstack.yaml per repo —\n" +
			"and a conversion report listing every field it could not convert (nothing is\n" +
			"dropped silently). It refuses to overwrite existing files without --force.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			if outDir == "" {
				outDir = "."
			}
			abs, _ := filepath.Abs(outDir)
			res, err := migrate.Convert(src, filepath.Base(abs))
			if err != nil {
				return err
			}

			type target struct {
				path string
				body []byte
			}
			targets := []target{{path: filepath.Join(outDir, "workspace.yaml"), body: res.WorkspaceYAML}}
			projNames := make([]string, 0, len(res.Projects))
			for name := range res.Projects {
				projNames = append(projNames, name)
			}
			sort.Strings(projNames)
			for _, name := range projNames {
				targets = append(targets, target{path: filepath.Join(outDir, name, "devstack.yaml"), body: res.Projects[name]})
			}
			reportPath := filepath.Join(outDir, "devstack-import-report.txt")
			report := migrate.RenderReport(res.Report)

			paths := make([]string, 0, len(targets)+1)
			for _, t := range targets {
				paths = append(paths, t.path)
			}
			paths = append(paths, reportPath)

			if dryRun {
				if g.JSON {
					return writeJSON(cmd, importSummary(paths, res.Report, true))
				}
				w := cmd.OutOrStdout()
				for _, t := range targets {
					fmt.Fprintf(w, "\n--- %s ---\n%s", t.path, t.body)
				}
				fmt.Fprintf(w, "\n--- %s ---\n%s", reportPath, report)
				fmt.Fprintln(w, "\n(dry-run: nothing written)")
				return nil
			}

			// No-clobber unless --force (then back up the originals first).
			for _, t := range targets {
				if _, err := os.Stat(t.path); err == nil {
					if !force {
						return fmt.Errorf("%s already exists; pass --force to overwrite (originals are backed up)", t.path)
					}
					if err := os.Rename(t.path, fmt.Sprintf("%s.bak.%d", t.path, time.Now().Unix())); err != nil {
						return fmt.Errorf("back up %s: %w", t.path, err)
					}
				}
			}
			for _, t := range targets {
				if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(t.path, t.body, 0o644); err != nil {
					return err
				}
			}
			if err := os.WriteFile(reportPath, []byte(report), 0o644); err != nil {
				return err
			}

			if g.JSON {
				return writeJSON(cmd, importSummary(paths, res.Report, false))
			}
			w := cmd.OutOrStdout()
			for _, p := range paths {
				fmt.Fprintf(w, "[ok]      wrote %s\n", p)
			}
			fmt.Fprint(w, "\n"+report)
			if len(res.Report) > 0 {
				fmt.Fprintln(w, "\nReview the report above and the generated files before committing (see docs/MIGRATION.md).")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the converted files + report without writing anything")
	cmd.Flags().StringVar(&outDir, "out", "", "output directory (default: current directory)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files (backs up originals first)")
	return cmd
}

func importSummary(paths []string, report []migrate.ReportEntry, dry bool) map[string]any {
	entries := make([]map[string]string, 0, len(report))
	for _, e := range report {
		entries = append(entries, map[string]string{"path": e.Path, "value": e.Value, "reason": e.Reason})
	}
	return map[string]any{"files": paths, "report": entries, "dryRun": dry}
}
