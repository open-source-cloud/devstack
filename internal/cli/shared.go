package cli

import (
	"fmt"
	"os"
	"path/filepath"

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
		stub("gc", "Reclaim unused shared services", "M2 (up saga)"),
		stub("doctor", "Reconcile the ledger against live containers", "M2 (up saga)"),
	)
	return cmd
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
