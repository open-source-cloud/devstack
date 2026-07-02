package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/version"
	"github.com/open-source-cloud/devstack/internal/workspace"
)

// contextInfo is the resolved active-context projection printed by `context` and
// used for the console header + shell prompt segment (spec 30).
type contextInfo struct {
	Workspace string `json:"workspace"`
	Root      string `json:"root"`
	Project   string `json:"project,omitempty"`
	Role      string `json:"role,omitempty"`
	Docker    string `json:"dockerContext"`
	Backend   string `json:"backend"`
	Version   string `json:"version"`
}

// resolveContext gathers the active-context projection from a manager. All fields
// come from the already-loaded model + ledger; no new data sources.
func resolveContext(mgr *workspace.Manager) contextInfo {
	proj := resolveActiveProject(mgr.Model, mgr.DB)
	ci := contextInfo{
		Workspace: mgr.Model.Workspace.Name,
		Root:      mgr.Model.Root,
		Project:   proj,
		Docker:    mgr.Docker.ContextName(),
		Backend:   backendLabel(mgr),
		Version:   version.Version,
	}
	if proj != "" {
		// The per-project Postgres role is the sanitized project name (hyphens →
		// underscores), matching provision.pgIdent. Display-only.
		ci.Role = strings.ReplaceAll(proj, "-", "_")
	}
	return ci
}

// backendLabel describes where the shared stack runs: "local", or the remote
// docker context / host endpoint.
func backendLabel(mgr *workspace.Manager) string {
	b := mgr.Model.Workspace.Backend
	if b == nil || !b.IsRemote() {
		return "local"
	}
	if b.Context != "" {
		return "remote:" + b.Context
	}
	return "remote:" + b.Host
}

// renderContextHeader prints a compact active-context line atop human command
// output (status/up). Suppressed under --json/--quiet — the update-notice
// precedent (root.go). Best-effort: a nil/empty manager prints nothing.
func renderContextHeader(cmd *cobra.Command, mgr *workspace.Manager, g *GlobalOpts) {
	if g.JSON || g.Quiet || mgr == nil {
		return
	}
	ci := resolveContext(mgr)
	parts := []string{"workspace " + ci.Workspace}
	if ci.Project != "" {
		parts = append(parts, "project "+ci.Project)
	}
	if ci.Role != "" {
		parts = append(parts, "role "+ci.Role)
	}
	parts = append(parts, "docker "+ci.Docker, ci.Version)
	fmt.Fprintf(cmd.OutOrStdout(), "devstack · %s\n\n", strings.Join(parts, " · "))
}

// renderPromptSegment prints a terse "workspace" or "workspace:project" segment
// for a shell prompt (spec 30). It is deliberately cheap: it discovers the
// workspace from config only (no Docker client, no ledger) and reads the project
// from DEVSTACK_PROJECT (set by the `use` shell hook). Outside a workspace it
// prints nothing, so the prompt segment simply disappears.
func renderPromptSegment(cmd *cobra.Command) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	m, err := config.Load(cwd)
	if err != nil {
		return nil
	}
	seg := m.Workspace.Name
	if p := os.Getenv("DEVSTACK_PROJECT"); p != "" {
		seg += ":" + p
	}
	fmt.Fprintln(cmd.OutOrStdout(), seg)
	return nil
}

// newContextCmd wires the read-only `context` command: print the resolved active
// workspace/project/role/docker-context/version. Lock-free.
func newContextCmd(g *GlobalOpts) *cobra.Command {
	var promptMode bool
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Show the active workspace, project, role and Docker context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if promptMode {
				return renderPromptSegment(cmd)
			}
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			ci := resolveContext(mgr)
			if g.JSON {
				return writeJSON(cmd, ci)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "workspace\t%s\n", ci.Workspace)
			fmt.Fprintf(tw, "root\t%s\n", ci.Root)
			if ci.Project != "" {
				fmt.Fprintf(tw, "project\t%s\n", ci.Project)
			} else {
				fmt.Fprintf(tw, "project\t(none — run `devstack use <project>`)\n")
			}
			if ci.Role != "" {
				fmt.Fprintf(tw, "db role\t%s\n", ci.Role)
			}
			fmt.Fprintf(tw, "docker context\t%s\n", ci.Docker)
			fmt.Fprintf(tw, "backend\t%s\n", ci.Backend)
			fmt.Fprintf(tw, "version\t%s\n", ci.Version)
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&promptMode, "prompt", false, "terse single-line output for a shell prompt segment (cheap; no Docker/ledger)")
	return cmd
}

// newUseCmd wires `use [name]`: set the active project (or switch to a registered
// workspace). It persists the active context to the ledger under the flock, and
// with --print emits an eval-able script (cd + export) so a shell wrapper can make
// the switch mutate the live shell (spec 30; the wrapper ships with `shell-init`).
func newUseCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var printScript bool
	var shell string
	cmd := &cobra.Command{
		Use:   "use [name]",
		Short: "Set the active project (or switch workspace); persists across terminals",
		Long: "Set the active workspace/project. Names a project in the current workspace, or a\n" +
			"registered workspace to switch to. The choice is persisted (per Docker context) and\n" +
			"becomes the default for db/s3/queue/... commands. A child process cannot change its\n" +
			"parent shell, so `--print` emits an eval-able script (cd + export DEVSTACK_*); the\n" +
			"`devstack` shell wrapper from `devstack shell-init` runs that for you.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			targetRoot := mgr.Model.Root
			targetProject := ""
			projects := sortedProjectNames(mgr.Model)

			switch {
			case project != "":
				if !contains(projects, project) {
					return fmt.Errorf("no project %q in workspace %q", project, mgr.Model.Workspace.Name)
				}
				targetProject = project
			case len(args) == 1:
				name := args[0]
				switch {
				case contains(projects, name):
					targetProject = name
				default:
					// Try a registered workspace by name.
					root, ok, err := lookupWorkspaceRoot(mgr, name)
					if err != nil {
						return err
					}
					if !ok {
						return fmt.Errorf("no project or registered workspace named %q", name)
					}
					targetRoot = root
				}
			default:
				// Bare `use`: report current context + candidates. Under --print
				// the hint goes to stderr so stdout stays an eval-safe (empty) script.
				out := cmd.OutOrStdout()
				if printScript {
					out = cmd.ErrOrStderr()
				}
				return printUseHint(out, mgr, projects)
			}

			if err := lock.WithLock(cmd.Context(), mgr.LockPath, func() error {
				return mgr.DB.SetActiveContext(targetRoot, targetProject)
			}); err != nil {
				return err
			}

			if printScript {
				emitUseScript(cmd, targetRoot, targetProject, shell)
				return nil
			}
			if g.JSON {
				return writeJSON(cmd, map[string]string{"workspace": targetRoot, "project": targetProject})
			}
			if !g.Quiet {
				w := cmd.OutOrStdout()
				if targetProject != "" {
					fmt.Fprintf(w, "active project → %s\n", targetProject)
				} else {
					fmt.Fprintf(w, "active workspace → %s\n", targetRoot)
				}
				fmt.Fprintln(w, "tip: add `eval \"$(devstack shell-init zsh)\"` so `use` also cd's your shell")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "force-select a project in the current workspace")
	cmd.Flags().BoolVar(&printScript, "print", false, "emit an eval-able shell script (cd + export) instead of persisting only")
	cmd.Flags().StringVar(&shell, "shell", "", "syntax for --print output: fish (else POSIX sh/zsh/bash)")
	_ = cmd.Flags().MarkHidden("shell")
	return cmd
}

// lookupWorkspaceRoot resolves a registered workspace name to its root (lock-free).
func lookupWorkspaceRoot(mgr *workspace.Manager, name string) (string, bool, error) {
	rows, err := mgr.DB.ListWorkspaces()
	if err != nil {
		return "", false, err
	}
	for _, w := range rows {
		if w.Name == name {
			return w.Root, true, nil
		}
	}
	return "", false, nil
}

// emitUseScript writes the eval script the shell wrapper runs: POSIX (sh/zsh/bash)
// by default, fish syntax when shell=="fish". Single-quoted values are valid in
// both. The `devstack` wrapper from `shell-init` eval's this to mutate the shell.
func emitUseScript(cmd *cobra.Command, root, project, shell string) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "cd %s\n", shellQuote(root))
	if shell == "fish" {
		fmt.Fprintf(w, "set -gx DEVSTACK_WORKSPACE %s\n", shellQuote(root))
		if project != "" {
			fmt.Fprintf(w, "set -gx DEVSTACK_PROJECT %s\n", shellQuote(project))
		} else {
			fmt.Fprintln(w, "set -e DEVSTACK_PROJECT")
		}
		return
	}
	fmt.Fprintf(w, "export DEVSTACK_WORKSPACE=%s\n", shellQuote(root))
	if project != "" {
		fmt.Fprintf(w, "export DEVSTACK_PROJECT=%s\n", shellQuote(project))
	} else {
		fmt.Fprintln(w, "unset DEVSTACK_PROJECT")
	}
}

// printUseHint reports the current active context and the selectable projects when
// `use` is invoked with no target.
func printUseHint(w io.Writer, mgr *workspace.Manager, projects []string) error {
	active := resolveActiveProject(mgr.Model, mgr.DB)
	if active != "" {
		fmt.Fprintf(w, "active project: %s\n", active)
	} else {
		fmt.Fprintln(w, "active project: (none)")
	}
	if len(projects) == 0 {
		fmt.Fprintln(w, "no projects in this workspace")
		return nil
	}
	fmt.Fprintln(w, "projects:")
	for _, p := range projects {
		marker := "  "
		if p == active {
			marker = "* "
		}
		fmt.Fprintf(w, "%s%s\n", marker, p)
	}
	fmt.Fprintln(w, "run `devstack use <project>` to select one")
	return nil
}

// shellQuote single-quotes a value for safe eval in POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
