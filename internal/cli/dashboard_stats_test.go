package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
)

// statsMockClient seeds a shared + a project container plus their canned stats
// so the collector's stats fetch is exercised without a daemon.
func statsMockClient() *docker.MockClient {
	return &docker.MockClient{
		Containers: []docker.Container{
			{ID: "pg1", Name: "devstack-shared-postgres-1", State: "running", Labels: map[string]string{
				generate.LabelManaged: "true", generate.LabelShared: "postgres",
			}},
			{ID: "web1", Name: "devstack-shop-web-1", State: "running", Labels: map[string]string{
				generate.LabelManaged: "true", generate.LabelProject: "shop", generate.LabelService: "web",
			}},
		},
		Stats: map[string]docker.Stats{
			"pg1":  {CPUPercent: 3.1, MemUsage: 412 * 1024 * 1024, MemLimit: 2 * 1024 * 1024 * 1024, MemPercent: 20},
			"web1": {CPUPercent: 12.0, MemUsage: 190 * 1024 * 1024, MemLimit: 0},
		},
	}
}

// TestCollectDashboardStats verifies the collector keys each container's sample
// by the dashboard row's display name (shared alias / "<project>/<service>").
func TestCollectDashboardStats(t *testing.T) {
	m := statsMockClient()
	got := collectDashboardStats(context.Background(), m, true)
	if len(got) != 2 {
		t.Fatalf("stats map = %d entries, want 2: %+v", len(got), got)
	}
	if st, ok := got["shared-postgres"]; !ok || st.CPUPercent != 3.1 {
		t.Errorf("shared-postgres stats = %+v (ok=%v)", st, ok)
	}
	if st, ok := got["shop/web"]; !ok || st.MemUsage != 190*1024*1024 {
		t.Errorf("shop/web stats = %+v (ok=%v)", st, ok)
	}
	if m.StatsCalls != 2 {
		t.Fatalf("StatsCalls = %d, want 2", m.StatsCalls)
	}
}

// TestCollectDashboardStatsDisabled asserts --no-stats (enabled=false) issues no
// ContainerStats calls at all.
func TestCollectDashboardStatsDisabled(t *testing.T) {
	m := statsMockClient()
	if got := collectDashboardStats(context.Background(), m, false); got != nil {
		t.Fatalf("disabled stats should be nil, got %+v", got)
	}
	if m.StatsCalls != 0 {
		t.Fatalf("StatsCalls = %d, want 0 (fetch must be skipped)", m.StatsCalls)
	}
}

// TestAttachDashboardStats folds seeded stats into the matching rows and leaves
// unmatched rows without stats.
func TestAttachDashboardStats(t *testing.T) {
	m := statsMockClient()
	rows := []dashRow{
		{Name: "shared-postgres", Kind: "shared"},
		{Name: "shop/web", Kind: "project"},
		{Name: "shop/api", Kind: "project"}, // no container → stays statless
	}
	attachDashboardStats(context.Background(), m, rows, true)

	if !rows[0].HasStats || rows[0].CPUPercent != 3.1 {
		t.Errorf("shared-postgres row = %+v", rows[0])
	}
	if !rows[1].HasStats || rows[1].MemUsage != 190*1024*1024 {
		t.Errorf("shop/web row = %+v", rows[1])
	}
	if rows[2].HasStats {
		t.Errorf("shop/api should have no stats: %+v", rows[2])
	}
}

// TestAttachDashboardStatsDisabled asserts disabled stats leave every row untouched.
func TestAttachDashboardStats_Disabled(t *testing.T) {
	m := statsMockClient()
	rows := []dashRow{{Name: "shared-postgres", Kind: "shared"}}
	attachDashboardStats(context.Background(), m, rows, false)
	if rows[0].HasStats {
		t.Fatalf("row should have no stats when disabled: %+v", rows[0])
	}
	if m.StatsCalls != 0 {
		t.Fatalf("StatsCalls = %d, want 0", m.StatsCalls)
	}
}

// TestDashboardStatsInModel drives the Bubble Tea Update/View with stats-bearing
// rows and asserts the CPU%/MEM columns render in the table and detail pane.
func TestDashboardStatsInModel(t *testing.T) {
	data := dashboardData{Rows: []dashRow{
		{Name: "shared-postgres", Kind: "shared", State: "running", Refs: 2,
			HasStats: true, CPUPercent: 3.1, MemUsage: 412 * 1024 * 1024, MemLimit: 2 * 1024 * 1024 * 1024},
	}}
	m := newDashboardModel(context.Background(), func(context.Context) dashboardData { return data }, 0)
	updated, _ := m.Update(dashDataMsg(data))
	dm := updated.(dashboardModel)

	tr := dm.table.Rows()
	if len(tr) != 1 {
		t.Fatalf("table rows = %d, want 1", len(tr))
	}
	// Columns: SERVICE, STATE, HEALTH, CPU%, MEM, REFS.
	if got := tr[0][3]; got != "3.1%" {
		t.Errorf("CPU%% cell = %q, want 3.1%%", got)
	}
	if got := tr[0][4]; !strings.Contains(got, "412.0MB") {
		t.Errorf("MEM cell = %q, want it to contain 412.0MB", got)
	}

	detail := dm.selectedDetail()
	if !strings.Contains(detail, "cpu: 3.1%") || !strings.Contains(detail, "mem:") {
		t.Errorf("detail missing stats: %q", detail)
	}

	dm2, _ := dm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := dm2.(dashboardModel).View()
	if !strings.Contains(view.Content, "CPU% (engine)") {
		t.Errorf("view missing CPU%% column header")
	}
}

// TestDashboardNoStatsRendersDash verifies a statless row shows the "—" placeholder.
func TestDashboardNoStatsRendersDash(t *testing.T) {
	r := dashRow{Name: "shop/api", Kind: "project", State: "running", HasStats: false}
	if got := dashCPU(r); got != "—" {
		t.Errorf("dashCPU = %q, want —", got)
	}
	if got := dashMem(r); got != "—" {
		t.Errorf("dashMem = %q, want —", got)
	}
}

// TestFormatBytes covers the byte humanizer used by the MEM column.
func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{512, "512B"},
		{1024, "1.0KB"},
		{412 * 1024 * 1024, "412.0MB"},
		{2 * 1024 * 1024 * 1024, "2.0GB"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.in); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
