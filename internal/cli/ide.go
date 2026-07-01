package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/ide"
)

// newIdeCmd wires `devstack ide` — the spec-17 editor/IDE generation sink. It loads
// the workspace and authors, deterministically, the artifacts that point editors at
// devstack's already-generated compose stacks: per-repo .devcontainer/devcontainer.json,
// a multi-root <name>.code-workspace, and per-repo .vscode/{launch,settings}.json.
//
// It is pure file authorship (no Docker, no ledger, no flock), so it mirrors the
// generate command's --check/--json contract. Targets are selected with
// --devcontainer / --vscode; --all (the no-flag default) emits both.
func newIdeCmd(g *GlobalOpts) *cobra.Command {
	var (
		devcontainer bool
		vscode       bool
		all          bool
		check        bool
	)
	cmd := &cobra.Command{
		Use:   "ide",
		Short: "Generate devcontainer/.code-workspace/launch editor configs",
		Long: "ide authors, from the same resolved config as `generate`, the editor artifacts\n" +
			"that point at devstack's already-generated compose stacks:\n\n" +
			"  * <repo>/.devcontainer/devcontainer.json  (attach the IDE to the SAME\n" +
			"    devstack-<name> compose project + shared network devstack up runs)\n" +
			"  * <workspace-root>/<name>.code-workspace   (VS Code multi-root)\n" +
			"  * <repo>/.vscode/{launch,settings}.json    (debugger + schema-map stubs)\n\n" +
			"Select targets with --devcontainer / --vscode; --all (the default when no target\n" +
			"flag is given) emits both. Output is byte-deterministic (writeIfChanged); --check\n" +
			"reports drift without writing (CI-friendly).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := loadWorkspace()
			if err != nil {
				return err
			}
			targets := ide.Targets{Devcontainer: devcontainer, VSCode: vscode}
			if all || (!devcontainer && !vscode) {
				targets = ide.All()
			}
			gen := ide.New(m)
			arts, err := gen.Build(targets)
			if err != nil {
				return err
			}
			if check {
				return reportIdeCheck(cmd, g, arts)
			}
			results, err := ide.Write(arts)
			if err != nil {
				return err
			}
			return reportIdeWrite(cmd, g, results)
		},
	}
	cmd.Flags().BoolVar(&devcontainer, "devcontainer", false, "emit per-repo .devcontainer/devcontainer.json only")
	cmd.Flags().BoolVar(&vscode, "vscode", false, "emit the .code-workspace + per-repo .vscode/ configs only")
	cmd.Flags().BoolVar(&all, "all", false, "emit every target (default when no target flag is given)")
	cmd.Flags().BoolVar(&check, "check", false, "report drift without writing (CI)")
	return cmd
}

func reportIdeWrite(cmd *cobra.Command, g *GlobalOpts, results []ide.WriteResult) error {
	if g.JSON {
		return writeJSON(cmd, map[string]any{"ok": true, "artifacts": results})
	}
	if g.Quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	for _, r := range results {
		state := "unchanged"
		if r.Changed {
			state = "updated"
		}
		fmt.Fprintf(w, "%s → %s  (%s)\n", r.Kind, r.Path, state)
	}
	return nil
}

func reportIdeCheck(cmd *cobra.Command, g *GlobalOpts, arts []ide.Artifact) error {
	upToDate := ide.UpToDate(arts)
	type entry struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	paths := make([]entry, 0, len(arts))
	for _, a := range arts {
		paths = append(paths, entry{Path: a.Rel, Kind: a.Kind})
	}
	if g.JSON {
		if err := writeJSON(cmd, map[string]any{"ok": upToDate, "artifacts": paths}); err != nil {
			return err
		}
	} else if !g.Quiet {
		w := cmd.OutOrStdout()
		for _, p := range paths {
			fmt.Fprintf(w, "%s: %s\n", p.Kind, p.Path)
		}
	}
	if !upToDate {
		return fmt.Errorf("IDE artifacts are stale; run `%s ide`", rootName(cmd))
	}
	return nil
}
