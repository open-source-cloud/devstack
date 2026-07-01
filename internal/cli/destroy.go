package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/orchestrate"
)

// newWorkspaceCmd wires `devstack workspace <sub>` — workspace-scoped lifecycle
// (spec 13 teardown). Today it carries `destroy`; `uninstall` (machine-global
// teardown incl. CA removal) is a separate top-level verb.
func newWorkspaceCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Workspace-level lifecycle (teardown)",
	}
	cmd.AddCommand(newWorkspaceDestroyCmd(g), newWorkspaceListCmd(g))
	return cmd
}

// newWorkspaceDestroyCmd wires `devstack workspace destroy` (spec 13): tear down
// THIS workspace's project stacks, release its ref/port ledger rows, warm-stop
// any shared service left at zero refs, and remove generated `.devstack/`
// artifacts. It leaves machine-global state (the shared network, the CA, alias
// symlinks) intact for other workspaces, and — by design in this first cut —
// PRESERVES data: named volumes and provisioned DBs/roles survive (shared
// per-service volume removal is `uninstall`/`db gc` territory).
func newWorkspaceDestroyCmd(g *GlobalOpts) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Tear down THIS workspace's stacks and release its refs/ports (volumes/DBs preserved)",
		Long: "destroy reverses what `up` created for this workspace: it `compose down`s every\n" +
			"project stack, drops the workspace's ref + port ledger rows (under the lock),\n" +
			"warm-stops any shared service now at zero references, and removes the generated\n" +
			"`.devstack/` artifacts.\n\n" +
			"It is data-preserving: named volumes and provisioned databases SURVIVE, and the\n" +
			"shared network / local CA / alias symlinks are left for other workspaces. Full\n" +
			"data + machine-global removal is `uninstall`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Fail fast before touching Docker: a scripted (--json) run can't answer
			// an interactive prompt, so it must pass --yes explicitly.
			if g.JSON && !yes {
				return fmt.Errorf("refusing to destroy without confirmation: pass --yes for --json/non-interactive use")
			}

			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			projects := sortedProjectNames(d.Model)
			if !yes {
				prompt := fmt.Sprintf(
					"This tears down workspace %q (%d project stack(s)) and releases its refs/ports.\n"+
						"Volumes and databases are PRESERVED. Type 'yes' to continue: ",
					d.Model.Workspace.Name, len(projects))
				if !confirm(cmd, prompt) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}

			res := destroyWorkspace(cmd.Context(), d, projects)
			if g.JSON {
				if err := writeJSON(cmd, res); err != nil {
					return err
				}
			} else {
				w := cmd.OutOrStdout()
				for _, p := range res.Projects {
					fmt.Fprintf(w, "[ok]      down %s\n", p)
				}
				for _, s := range res.SharedStopped {
					fmt.Fprintf(w, "[ok]      stopped shared %s (0 refs)\n", s)
				}
				for _, e := range res.Errors {
					fmt.Fprintf(w, "[warn]    %s\n", e)
				}
				fmt.Fprintf(w, "destroyed workspace %q: %d stack(s) down, %d shared stopped (volumes/DBs preserved)\n",
					d.Model.Workspace.Name, len(res.Projects), len(res.SharedStopped))
			}
			if len(res.Errors) > 0 {
				return fmt.Errorf("destroy completed with %d error(s)", len(res.Errors))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (required for --json/non-interactive)")
	return cmd
}

// DestroyResult is the machine-readable outcome of `workspace destroy`.
type DestroyResult struct {
	Workspace     string   `json:"workspace"`
	Projects      []string `json:"projects"`       // project stacks brought down
	SharedStopped []string `json:"shared_stopped"` // orphaned shared services warm-stopped
	Errors        []string `json:"errors,omitempty"`
}

// destroyWorkspace performs the teardown mechanics (no prompting) so it is
// unit-testable with injected mocks. It is best-effort: a failure on one project
// is recorded and the rest proceed, so a partially-broken workspace can still be
// cleaned up.
func destroyWorkspace(ctx context.Context, d orchestrate.UpDeps, projects []string) DestroyResult {
	res := DestroyResult{Workspace: d.Model.Workspace.Name}
	runner := d.Runner
	if runner == nil {
		runner = docker.ExecRunner{}
	}

	// 1. compose down each project stack (containers + project default network;
	// named volumes survive — never -v here).
	for _, p := range projects {
		outDir := filepath.Join(d.Model.ProjectDir(p), generate.GenDir)
		composeFile := filepath.Join(outDir, generate.ComposeFile)
		if _, err := os.Stat(composeFile); err != nil {
			composeFile = "" // label-driven `compose -p devstack-<p> down`
		}
		cp := docker.Compose{Project: "devstack-" + p, File: composeFile, Dir: outDir, Runner: runner}
		if err := cp.Down(ctx, false); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("down %s: %v", p, err))
			continue
		}
		res.Projects = append(res.Projects, p)
	}

	// 2. drop this workspace's ledger rows (refs + ports) under the flock.
	if err := lock.WithLock(ctx, d.LockPath, func() error {
		for _, p := range projects {
			if _, err := d.DB.RemoveProjectRefs(p); err != nil {
				return err
			}
			if err := d.DB.ReleasePortsFor(p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("ledger cleanup: %v", err))
	}

	// 3. warm-stop any shared service now at zero refs (reversible; volumes
	// survive). GC enumerates by ledger ∩ live labels, so it only touches
	// services no OTHER workspace references.
	if gc, err := d.Manager.GC(ctx, true); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("shared gc: %v", err))
	} else {
		res.SharedStopped = gc.Stopped
	}

	// 4. remove generated artifacts (.devstack): the workspace-root shared dir and
	// each project's dir. Best-effort — a missing dir is fine.
	_ = os.RemoveAll(filepath.Join(d.Model.Root, generate.GenDir))
	for _, p := range projects {
		_ = os.RemoveAll(filepath.Join(d.Model.ProjectDir(p), generate.GenDir))
	}
	return res
}

// confirm prompts on stdout and returns true only if the user types "yes".
func confirm(cmd *cobra.Command, prompt string) bool {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}
