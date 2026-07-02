package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/internal/xdg"
)

// newSharedCmd wires `shared status|gc|doctor`. `status` is the M2 read-only
// projection of the ledger (shared services + ref counts + consuming projects);
// `gc`/`doctor` (which stop/reconcile against the daemon) land with the up saga.
func newSharedCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shared",
		Short: "Inspect and reclaim shared services",
	}
	cmd.AddCommand(
		newSharedStatusCmd(g),
		newSharedGcCmd(g),
		newSharedDoctorCmd(g),
		// `shared expose`/`shared ports` stay as aliases of the top-level `expose`/
		// `ports` (fresh instances; same logic) for backward compatibility.
		newExposeCmd(g),
		newPortsCmd(g),
	)
	return cmd
}

// newSharedGcCmd wires `shared gc [--stop]` — find shared services at zero refs
// and (with --stop) stop them. Default is a dry-run report: warm DBs are cheap,
// so reclamation is opt-in (spec 03/09). Volumes are never touched.
func newSharedGcCmd(g *GlobalOpts) *cobra.Command {
	var stop bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Report (or with --stop, stop) shared services at zero references",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			res, err := mgr.GC(cmd.Context(), stop)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, res)
			}
			w := cmd.OutOrStdout()
			if len(res.Candidates) == 0 {
				fmt.Fprintln(w, "no shared services at zero references")
				return nil
			}
			for _, c := range res.Candidates {
				if slices.Contains(res.Stopped, c) {
					fmt.Fprintf(w, "stopped %s\n", c)
				} else if stop {
					fmt.Fprintf(w, "%s (zero refs, left running)\n", c)
				} else {
					fmt.Fprintf(w, "%s (zero refs — run `shared gc --stop` to stop)\n", c)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&stop, "stop", false, "actually stop the zero-ref services (default: report only)")
	return cmd
}

// newSharedDoctorCmd wires `shared doctor` — the self-healing reconcile: prune
// ref rows for projects no longer live (the count is derived from reality).
func newSharedDoctorCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Reconcile the ledger against live containers (prune dead refs)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			pruned, err := mgr.Reconcile(cmd.Context())
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"pruned": pruned})
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "reconciled: pruned %d stale ref row(s)\n", len(pruned))
			return nil
		},
	}
}

func newSharedStatusCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show shared-service ref counts and consuming projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			// Best-effort self-healing reconcile: prune refs for projects whose
			// containers are no longer live. Skipped silently if the daemon is down
			// (the ledger view below is still truthful about recorded refs).
			_, _ = mgr.Reconcile(cmd.Context())

			rows, err := mgr.Status()
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"shared": rows})
			}
			w := cmd.OutOrStdout()
			if len(rows) == 0 {
				fmt.Fprintln(w, "no shared services recorded yet (run `devstack up`)")
				return nil
			}
			for _, r := range rows {
				fmt.Fprintf(w, "%-20s %-10s refs=%d  %v\n", r.Alias, r.Status, r.RefCount, r.Projects)
			}
			return nil
		},
	}
}

// buildManager assembles a workspace.Manager from the current directory: the
// loaded workspace, the read-only docker client (context keys the ledger), the
// machine-global state ledger, and the embedded template source. The returned
// closer releases the ledger and the docker client.
func buildManager(cmd *cobra.Command) (*workspace.Manager, func(), error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	model, err := config.Load(cwd)
	if err != nil {
		return nil, nil, err
	}

	ctx := cmd.Context()
	ctxName := state.DefaultContext
	var dockerClient docker.Client
	if c, err := docker.NewClient(ctx); err == nil {
		dockerClient = c
		ctxName = c.ContextName()
	} else {
		// No daemon client: ledger reads still work; reconcile degrades to a no-op.
		dockerClient = &docker.MockClient{Context: ctxName}
	}

	db, err := state.Open(ctx, xdg.DataHome(), ctxName)
	if err != nil {
		if c, ok := dockerClient.(interface{ Close() error }); ok {
			_ = c.Close()
		}
		return nil, nil, err
	}

	mgr := &workspace.Manager{
		Model:    model,
		DB:       db,
		Docker:   dockerClient,
		Source:   builtinSource(),
		LockPath: filepath.Join(xdg.RuntimeDir(), "devstack.lock"),
	}
	closeFn := func() {
		db.Close()
		_ = dockerClient.Close()
	}
	return mgr, closeFn, nil
}
