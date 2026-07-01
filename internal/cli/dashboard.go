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

			// Stats default ON for the cockpit; --no-stats disables the per-container
			// ContainerStats fetch for low-power machines (spec 16 §gotchas [Q-DASH-STATS]).
			stats := !noStats

			if !dashboardInteractive(cmd, g) {
				return printDashboardSnapshot(cmd, g, mgr, stats)
			}

			ctx := cmd.Context()
			fetch := func(c context.Context) dashboardData { return collectDashboardData(c, mgr, stats) }
			model := newDashboardModel(ctx, fetch, dashboardPoll)
			_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
			return err
		},
	}
	cmd.Flags().BoolVar(&noStats, "no-stats", false, "disable the per-container CPU/mem stats fetch (lower overhead)")
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
func printDashboardSnapshot(cmd *cobra.Command, g *GlobalOpts, mgr *workspace.Manager, stats bool) error {
	ctx := cmd.Context()
	projects := collectProjectStatus(ctx, mgr)
	shared, err := mgr.Status()
	if err != nil {
		return err
	}
	if g.JSON {
		out := map[string]any{"projects": projects, "shared": shared}
		// Include live stats when enabled and available, keyed by the same
		// service display name the TUI rows use (spec 16: keep the snapshot path
		// working, include stats when available).
		if st := collectDashboardStats(ctx, mgr.Docker, stats); len(st) > 0 {
			out["stats"] = st
		}
		return writeJSON(cmd, out)
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
func collectDashboardData(ctx context.Context, mgr *workspace.Manager, stats bool) dashboardData {
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

	attachDashboardStats(ctx, mgr.Docker, data.Rows, stats)

	data.Logs = collectRecentLogs(ctx, mgr.Docker, 8)
	return data
}

// attachDashboardStats fetches a live CPU/mem sample per visible container (a
// bounded, read-only fetch — one call each, no streaming reader) and folds it
// into the matching rows. A disabled flag or an unreadable container leaves the
// row's HasStats false, which the table renders as "—". Best-effort: never fatal.
func attachDashboardStats(ctx context.Context, client docker.Client, rows []dashRow, enabled bool) {
	byKey := collectDashboardStats(ctx, client, enabled)
	if len(byKey) == 0 {
		return
	}
	for i := range rows {
		if st, ok := byKey[rows[i].Name]; ok {
			rows[i].HasStats = true
			rows[i].CPUPercent = st.CPUPercent
			rows[i].MemUsage = st.MemUsage
			rows[i].MemLimit = st.MemLimit
		}
	}
}

// collectDashboardStats resolves the workspace's managed containers and fetches
// one resource-usage sample each, keyed by the row's display name (the shared
// DNS alias, or "<project>/<service>"). Returns nil when stats are disabled — the
// --no-stats path so a low-power machine issues zero ContainerStats calls.
func collectDashboardStats(ctx context.Context, client docker.Client, enabled bool) map[string]docker.Stats {
	if !enabled {
		return nil
	}
	targets, err := resolveLogTargets(ctx, client, nil)
	if err != nil {
		return nil
	}
	out := make(map[string]docker.Stats, len(targets))
	for _, t := range targets {
		st, err := client.ContainerStats(ctx, t.ID)
		if err != nil {
			continue // unreadable container: leave the row without stats
		}
		out[dashStatsKey(t)] = st
	}
	return out
}

// dashStatsKey maps a log target onto the dashboard row's display name so stats
// join to the right row: a shared service by its DNS alias, a project service by
// "<project>/<service>". logTarget.Project is empty for shared services.
func dashStatsKey(t logTarget) string {
	if t.Project != "" {
		return t.Project + "/" + t.Service
	}
	return t.Service
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
