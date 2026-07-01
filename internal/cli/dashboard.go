package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/workspace"
)

// dashboardPoll is the default safety-poll cadence (spec 16: event-driven + a 2s
// fallback; the model uses the fallback poll for its live refresh).
const dashboardPoll = 2 * time.Second

// newDashboardCmd wires `devstack dashboard` (spec 16): a Bubble Tea cockpit over
// the read-only Engine SDK + the ledger. On a non-TTY (or --json/--quiet) it
// refuses to launch the TUI and prints a one-shot status snapshot instead — the
// scriptable, non-interactive equivalent.
func newDashboardCmd(g *GlobalOpts) *cobra.Command {
	var noStats bool
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Live TUI cockpit: shared + project services, health, and a log tail",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			// Best-effort self-heal so ref counts are truthful (lock-free reads,
			// this reconcile takes the lock only if it prunes — same as status).
			_, _ = mgr.Reconcile(cmd.Context())

			if !dashboardInteractive(cmd, g) {
				return printDashboardSnapshot(cmd, g, mgr)
			}

			ctx := cmd.Context()
			fetch := func(c context.Context) dashboardData { return collectDashboardData(c, mgr) }
			model := newDashboardModel(ctx, fetch, dashboardPoll)
			_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
			return err
		},
	}
	cmd.Flags().BoolVar(&noStats, "no-stats", false, "reserved: disable the CPU/mem stats stream (stats are opt-in in this build)")
	return cmd
}

// dashboardInteractive reports whether the TUI may launch: a real stdout TTY and
// neither --json nor --quiet requested.
func dashboardInteractive(cmd *cobra.Command, g *GlobalOpts) bool {
	if g.JSON || g.Quiet {
		return false
	}
	f, ok := cmd.OutOrStdout().(*os.File)
	return ok && isTerminal(f)
}

// printDashboardSnapshot is the non-TTY fallback: a one-shot projection reusing
// the same shared-status + per-project views as `status`, plus a redirect to the
// scriptable commands.
func printDashboardSnapshot(cmd *cobra.Command, g *GlobalOpts, mgr *workspace.Manager) error {
	ctx := cmd.Context()
	projects := collectProjectStatus(ctx, mgr)
	shared, err := mgr.Status()
	if err != nil {
		return err
	}
	if g.JSON {
		return writeJSON(cmd, map[string]any{"projects": projects, "shared": shared})
	}
	if g.Quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "dashboard needs an interactive terminal; showing a one-shot snapshot.")
	fmt.Fprintln(w, "use `devstack logs` (follow) or `devstack status --json` for non-interactive output.")
	fmt.Fprintln(w)
	renderStatus(cmd, projects, shared)
	return nil
}

// collectDashboardData is the read-only collector: it fans in shared-service rows
// (ledger), per-project service rows (live containers + health), and a bounded
// tail of recent log lines into one snapshot. Lock-free.
func collectDashboardData(ctx context.Context, mgr *workspace.Manager) dashboardData {
	var data dashboardData

	shared, err := mgr.Status()
	if err != nil {
		data.Err = err.Error()
	}
	for _, s := range shared {
		data.Rows = append(data.Rows, dashRow{
			Name:     s.Alias,
			Kind:     "shared",
			State:    s.Status,
			Refs:     s.RefCount,
			Projects: s.Projects,
			Engine:   dashEngine(s),
		})
	}

	for _, p := range collectProjectStatus(ctx, mgr) {
		for _, svc := range p.Services {
			data.Rows = append(data.Rows, dashRow{
				Name:   p.Project + "/" + svc.Name,
				Kind:   "project",
				State:  svc.State,
				Health: svc.Health,
				URL:    fmt.Sprintf("https://%s.%s.localhost", svc.Name, p.Project),
			})
		}
	}

	data.Logs = collectRecentLogs(ctx, mgr.Docker, 8)
	return data
}

// dashEngine renders a shared row's "engine version" detail.
func dashEngine(s workspace.SharedStatus) string {
	if s.Major == "" || s.Major == "default" {
		return s.Engine
	}
	return s.Engine + " " + s.Major
}

// collectRecentLogs pulls up to `tail` trailing lines from each managed container
// (non-follow) into a service-tagged, bounded slice for the log pane. Best-effort:
// an unreadable service is skipped, not fatal.
func collectRecentLogs(ctx context.Context, client docker.Client, tail int) []dashLog {
	targets, err := resolveLogTargets(ctx, client, nil)
	if err != nil {
		return nil
	}
	var out []dashLog
	for _, t := range targets {
		ch, err := client.ContainerLogStream(ctx, t.ID, docker.LogOptions{Tail: tail})
		if err != nil {
			continue
		}
		for ll := range ch {
			out = append(out, dashLog{Service: t.Service, Line: ll.Text})
		}
	}
	// Deterministic-ish ordering: group by service (arrival order within a
	// service is preserved by the stream).
	sort.SliceStable(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return clampLogs(out)
}
