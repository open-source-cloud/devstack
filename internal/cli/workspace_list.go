package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/xdg"
)

// wsSharedRef is one shared-service ref-count entry for a workspace list row.
type wsSharedRef struct {
	Service string `json:"service"`
	Refs    int    `json:"refs"`
}

// wsListRow is one `workspace list` row (also the --json element shape). It is a
// live projection: the registry supplies name/root/timestamps, everything else is
// re-derived from the committed workspace.yaml at list time (Q-WS-REGISTRY).
type wsListRow struct {
	Name     string        `json:"name"`
	Root     string        `json:"root"`
	Projects []string      `json:"projects"`
	Shared   []wsSharedRef `json:"shared"`
	LastUpAt string        `json:"last_up_at,omitempty"`
	Stale    bool          `json:"stale"`           // root no longer on disk
	Issue    string        `json:"issue,omitempty"` // e.g. an unparseable workspace.yaml
}

// newWorkspaceListCmd wires `devstack workspace list [--json] [--prune]` (spec 26):
// enumerate every workspace recorded for the current Docker context, re-deriving
// each root's projects + shared-service refs from its workspace.yaml. A vanished
// root is flagged `stale` (and dropped by --prune); an unparseable workspace.yaml
// degrades to a flagged row rather than failing the whole list.
func newWorkspaceListCmd(g *GlobalOpts) *cobra.Command {
	var prune bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every registered workspace for this Docker context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, closeFn, err := openLedger(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			registered, err := db.ListWorkspaces()
			if err != nil {
				return err
			}
			// A single ledger read of every ref row for this context; joined per
			// workspace below. Lock-free (reads are snapshots).
			allRefs, err := db.AllRefs()
			if err != nil {
				return err
			}

			var rows []wsListRow
			var vanished []string
			for _, w := range registered {
				row := wsListRow{Name: w.Name, Root: w.Root, LastUpAt: w.LastUpAt}
				if _, statErr := os.Stat(w.Root); statErr != nil {
					row.Stale = true
					vanished = append(vanished, w.Root)
					rows = append(rows, row)
					continue
				}
				m, loadErr := config.LoadAt(w.Root)
				if loadErr != nil {
					// Degrade: the root exists but its workspace.yaml won't parse. Flag
					// it, never fail the whole list (and never prune it — the root is
					// still there).
					row.Issue = "unreadable workspace.yaml"
					rows = append(rows, row)
					continue
				}
				row.Name = m.Workspace.Name
				row.Projects = sortedProjectNames(m)
				row.Shared = sharedRefsFor(m, allRefs)
				rows = append(rows, row)
			}

			// --prune is the ONLY path that removes a vanished root (a moved checkout
			// keeps its history otherwise). Under the flock.
			if prune && len(vanished) > 0 {
				if err := lock.WithLock(cmd.Context(), lockPath(), func() error {
					for _, root := range vanished {
						if _, err := db.RemoveWorkspace(root); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					return err
				}
				// Drop the pruned rows from the output.
				kept := rows[:0]
				for _, r := range rows {
					if r.Stale {
						continue
					}
					kept = append(kept, r)
				}
				rows = kept
			}

			if g.JSON {
				return writeJSON(cmd, rows)
			}
			return renderWorkspaceList(cmd, rows)
		},
	}
	cmd.Flags().BoolVar(&prune, "prune", false, "drop registry rows whose workspace root no longer exists")
	return cmd
}

// sharedRefsFor joins the ledger ref rows to a workspace's projects: for each
// shared service any of this workspace's projects reference, the ref count. Sorted
// by service for deterministic output.
func sharedRefsFor(m *config.Model, allRefs []state.Ref) []wsSharedRef {
	projSet := map[string]bool{}
	for name := range m.Projects {
		projSet[name] = true
	}
	counts := map[string]int{}
	for _, r := range allRefs {
		if projSet[r.Project] {
			counts[r.SharedService]++
		}
	}
	out := make([]wsSharedRef, 0, len(counts))
	for svc, n := range counts {
		out = append(out, wsSharedRef{Service: svc, Refs: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// renderWorkspaceList prints the plain table: NAME · ROOT · PROJECTS · SHARED · LAST UP.
func renderWorkspaceList(cmd *cobra.Command, rows []wsListRow) error {
	w := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintln(w, "no workspaces recorded yet (run `devstack up` in a workspace)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tROOT\tPROJECTS\tSHARED\tLAST UP")
	for _, r := range rows {
		root := r.Root
		switch {
		case r.Stale:
			root += "  [stale: gone]"
		case r.Issue != "":
			root += "  [" + r.Issue + "]"
		}
		projects := "—"
		if len(r.Projects) > 0 {
			projects = joinComma(r.Projects)
		}
		shared := "—"
		if len(r.Shared) > 0 {
			parts := make([]string, 0, len(r.Shared))
			for _, s := range r.Shared {
				parts = append(parts, fmt.Sprintf("%s (%d)", s.Service, s.Refs))
			}
			shared = joinComma(parts)
		}
		lastUp := r.LastUpAt
		if lastUp == "" {
			lastUp = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, root, projects, shared, lastUp)
	}
	return tw.Flush()
}

func joinComma(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// openLedger opens the machine-global ledger for the current Docker context
// WITHOUT requiring a workspace at the CWD (workspace list is machine-wide). The
// docker client only supplies the context key; if the daemon is unreachable the
// ledger still opens under the default context.
func openLedger(cmd *cobra.Command) (*state.DB, func(), error) {
	ctx := cmd.Context()
	ctxName := state.DefaultContext
	var closeDocker func()
	if c, err := docker.NewClient(ctx); err == nil {
		ctxName = c.ContextName()
		closeDocker = func() { _ = c.Close() }
	}
	db, err := state.Open(ctx, xdg.DataHome(), ctxName)
	if err != nil {
		if closeDocker != nil {
			closeDocker()
		}
		return nil, nil, err
	}
	closeFn := func() {
		db.Close()
		if closeDocker != nil {
			closeDocker()
		}
	}
	return db, closeFn, nil
}

// lockPath returns the machine-global advisory lock path (spec 08).
func lockPath() string {
	return filepath.Join(xdg.RuntimeDir(), "devstack.lock")
}
