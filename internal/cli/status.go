package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/git"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/workspace"
)

// newStatusCmd wires `devstack status` — the spec-09 composite view: per-project
// service state + health, a brief git summary, the last saga outcome, and the
// shared-service ref graph. Read-only (lock-free) bar a best-effort reconcile.
// Per-repo git detail lives in `ws status`.
func newStatusCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Service health + last saga outcome + shared-service ref graph",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			ctx := cmd.Context()
			_, _ = mgr.Reconcile(ctx) // best-effort self-heal

			projects := collectProjectStatus(ctx, mgr)
			shared, err := mgr.Status()
			if err != nil {
				return err
			}

			if g.JSON {
				return writeJSON(cmd, map[string]any{"projects": projects, "shared": shared})
			}
			renderContextHeader(cmd, mgr, g)
			renderStatus(cmd, projects, shared)
			return nil
		},
	}
}

type serviceStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Health string `json:"health,omitempty"`
}

type sagaSummary struct {
	Phase  string `json:"phase"`
	Status string `json:"status"`
}

type projectStatus struct {
	Project  string          `json:"project"`
	Branch   string          `json:"branch,omitempty"`
	Dirty    bool            `json:"dirty"`
	Services []serviceStatus `json:"services"`
	LastSaga *sagaSummary    `json:"lastSaga,omitempty"`
}

// collectProjectStatus assembles the per-project view from live containers, the
// saga ledger, and a tolerant git summary.
func collectProjectStatus(ctx context.Context, mgr *workspace.Manager) []projectStatus {
	phases, _ := mgr.DB.PhasesFor(mgr.Model.Workspace.Name)
	gx, _ := git.New()

	out := []projectStatus{}
	for _, name := range sortedProjectNames(mgr.Model) {
		ps := projectStatus{Project: name}

		// Git summary (tolerant: many projects are local, non-repo paths).
		if gx != nil {
			if dir := mgr.Model.ProjectDir(name); dir != "" && gx.IsRepo(ctx, dir) {
				if st, err := gx.Status(ctx, dir); err == nil {
					ps.Branch = st.Branch
					ps.Dirty = st.Dirty()
				}
			}
		}

		// Live services from tool-labelled containers (+ health via inspect).
		cs, _ := mgr.Docker.ListManaged(ctx, map[string]string{
			generate.LabelManaged: "true", generate.LabelProject: name,
		})
		for _, c := range cs {
			svc := c.Labels[generate.LabelService]
			if svc == "" {
				svc = c.Name
			}
			ss := serviceStatus{Name: svc, State: c.State}
			if det, err := mgr.Docker.ContainerInspect(ctx, c.ID); err == nil && det.HasHealthcheck() {
				ss.Health = string(det.Health)
			}
			ps.Services = append(ps.Services, ss)
		}
		sort.Slice(ps.Services, func(i, j int) bool { return ps.Services[i].Name < ps.Services[j].Name })

		ps.LastSaga = lastSaga(phases, name)
		out = append(out, ps)
	}
	return out
}

// lastSaga summarizes a project's saga outcome: a failed phase if any, else the
// terminal compose-up status.
func lastSaga(phases []state.SagaPhase, project string) *sagaSummary {
	var composeUp *sagaSummary
	for _, p := range phases {
		if p.Scope != project {
			continue
		}
		if p.Status == state.PhaseFailed {
			return &sagaSummary{Phase: p.Phase, Status: p.Status}
		}
		if p.Phase == "compose-up" {
			composeUp = &sagaSummary{Phase: p.Phase, Status: p.Status}
		}
	}
	return composeUp
}

func renderStatus(cmd *cobra.Command, projects []projectStatus, shared []workspace.SharedStatus) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "PROJECTS")
	if len(projects) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, p := range projects {
		git := ""
		if p.Branch != "" {
			git = " [" + p.Branch + dirtyMark(p.Dirty) + "]"
		}
		saga := ""
		if p.LastSaga != nil {
			saga = fmt.Sprintf("  saga:%s=%s", p.LastSaga.Phase, p.LastSaga.Status)
		}
		fmt.Fprintf(w, "  %s%s%s\n", p.Project, git, saga)
		for _, s := range p.Services {
			h := ""
			if s.Health != "" {
				h = " (" + s.Health + ")"
			}
			fmt.Fprintf(w, "      %-16s %s%s\n", s.Name, s.State, h)
		}
		if len(p.Services) == 0 {
			fmt.Fprintln(w, "      (no running services)")
		}
	}
	fmt.Fprintln(w, "SHARED")
	if len(shared) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, s := range shared {
		fmt.Fprintf(w, "  %-20s %-10s refs=%d  %v\n", s.Alias, s.Status, s.RefCount, s.Projects)
	}
}

func dirtyMark(dirty bool) string {
	if dirty {
		return "*"
	}
	return ""
}
