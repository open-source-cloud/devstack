package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/hooks"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/internal/xdg"
)

// newUpCmd wires `devstack up [project...]` — the onboarding saga (spec 09): it
// reconciles the ledger, then drives preflight → network → generate →
// shared(health-gated) → compose-up → hooks, resumable and compensating.
func newUpCmd(g *GlobalOpts) *cobra.Command {
	var (
		build       bool
		noHooks     bool
		noPreflight bool
		profile     string
	)
	cmd := &cobra.Command{
		Use:   "up [project...]",
		Short: "Bring the workspace up (network, shared infra, generate, compose up, hooks)",
		Long: "up takes a workspace from config to a running, health-gated stack in one\n" +
			"idempotent command. Phases record their state so a re-run skips satisfied\n" +
			"work and a crash mid-run resumes; a failure compensates the mutating phases\n" +
			"(refs/containers) but never destroys data (volumes/DBs survive).",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			d.Projects = args
			d.Build = build
			d.NoHooks = noHooks
			d.NoPreflight = noPreflight
			d.Profile = profile

			// Self-healing reconcile before the saga (spec 09): prune ref rows for
			// projects no longer live. Best-effort — never blocks `up`.
			_, _ = d.Manager.Reconcile(cmd.Context())

			phases, err := orchestrate.BuildUp(d)
			if err != nil {
				return err
			}
			saga := &orchestrate.Saga{Workspace: d.Model.Workspace.Name, DB: d.DB, LockPath: d.LockPath}

			// Plain/quiet stream each phase as it completes; --json collects them.
			if !g.JSON && !g.Quiet {
				w := cmd.OutOrStdout()
				saga.Emit = func(r orchestrate.Record) { fmt.Fprintln(w, orchestrate.FormatPlain(r)) }
			}
			records, runErr := saga.Run(cmd.Context(), phases)

			if g.JSON {
				if err := writeJSON(cmd, records); err != nil {
					return err
				}
			}
			if runErr != nil {
				return runErr
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&build, "build", false, "build images before starting (compose build)")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "skip lifecycle hooks")
	cmd.Flags().BoolVar(&noPreflight, "no-preflight", false, "skip the preflight checks")
	cmd.Flags().StringVar(&profile, "profile", "", "env-overlay profile for ${profile}")
	return cmd
}

// newDownCmd wires `devstack down [project...]` — stop project stacks, run
// preDown hooks first, drop their ref rows. The external network and volumes are
// never touched; shared services are left running (autostop is X1 config + spec 03).
func newDownCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down [project...]",
		Short: "Stop this workspace's project stacks and release their refs",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			projects := args
			if len(projects) == 0 {
				for name := range d.Model.Projects {
					projects = append(projects, name)
				}
			}
			ctx := cmd.Context()
			w := cmd.OutOrStdout()
			type result struct {
				Project string `json:"project"`
				Status  string `json:"status"`
				Error   string `json:"error,omitempty"`
			}
			var results []result
			var firstErr error
			for _, p := range projects {
				if _, ok := d.Model.Projects[p]; !ok {
					return fmt.Errorf("project %q is not in this workspace", p)
				}
				status := "stopped"
				if err := downProject(ctx, d, p); err != nil {
					status = "failed"
					results = append(results, result{Project: p, Status: status, Error: err.Error()})
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				results = append(results, result{Project: p, Status: status})
				if !g.JSON && !g.Quiet {
					fmt.Fprintf(w, "[ok]      down %s\n", p)
				}
			}
			if g.JSON {
				if err := writeJSON(cmd, map[string]any{"down": results}); err != nil {
					return err
				}
			}
			return firstErr
		},
	}
	return cmd
}

// downProject runs a project's preDown hooks (warn-by-default), composes the
// stack down (never -v: volumes survive), and drops its ref rows.
func downProject(ctx context.Context, d orchestrate.UpDeps, project string) error {
	outDir := filepath.Join(d.Model.ProjectDir(project), generate.GenDir)
	composeFile := filepath.Join(outDir, generate.ComposeFile)
	if _, err := os.Stat(composeFile); err != nil {
		composeFile = "" // fall back to label-driven `compose -p <proj> down`
	}

	p := d.Model.Projects[project]
	if len(p.Hooks.PreDown) > 0 {
		runner := &hooks.Runner{
			Execer: hooks.OSExecer{BaseDir: d.Model.ProjectDir(project), Project: "devstack-" + project, File: composeFile},
			Ledger: d.DB,
			Lock:   func(ctx context.Context, fn func() error) error { return lock.WithLock(ctx, d.LockPath, fn) },
		}
		// preDown defaults to warn so a broken teardown hook can't trap a workspace.
		if _, err := runner.RunPhase(ctx, p.Hooks.PreDown, hooks.PhaseOpts{
			Project: project, Phase: "preDown", DefaultOnFailure: hooks.OnWarn,
		}); err != nil {
			return err
		}
	}

	cp := docker.Compose{Project: "devstack-" + project, File: composeFile, Dir: outDir, Runner: docker.ExecRunner{}}
	if err := cp.Down(ctx, false); err != nil {
		return err
	}
	if _, err := d.Manager.RegisterDown(ctx, project); err != nil {
		return err
	}
	return nil
}

// buildUpDeps assembles the up/down dependencies from the current directory. It
// uses the real Engine SDK client (up/down require a daemon — the preflight
// phase reports an unreachable daemon clearly).
func buildUpDeps(cmd *cobra.Command) (orchestrate.UpDeps, func(), error) {
	var zero orchestrate.UpDeps
	cwd, err := os.Getwd()
	if err != nil {
		return zero, nil, err
	}
	model, err := config.Load(cwd)
	if err != nil {
		return zero, nil, err
	}
	ctx := cmd.Context()
	dc, err := docker.NewClient(ctx)
	if err != nil {
		return zero, nil, fmt.Errorf("docker client: %w", err)
	}
	db, err := state.Open(ctx, xdg.DataHome(), dc.ContextName())
	if err != nil {
		_ = dc.Close()
		return zero, nil, err
	}
	lockPath := filepath.Join(xdg.RuntimeDir(), "devstack.lock")
	mgr := &workspace.Manager{Model: model, DB: db, Docker: dc, Source: builtinSource(), LockPath: lockPath}
	d := orchestrate.UpDeps{
		Model: model, DB: db, Docker: dc, Manager: mgr,
		Source: mgr.Source, LockPath: lockPath,
	}
	closeFn := func() {
		db.Close()
		_ = dc.Close()
	}
	return d, closeFn, nil
}
