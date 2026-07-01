package cli

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func sampleData() dashboardData {
	return dashboardData{
		Rows: []dashRow{
			{Name: "shared-postgres", Kind: "shared", State: "running", Refs: 2, Projects: []string{"api", "web"}, Engine: "postgres 18"},
			{Name: "shop/api", Kind: "project", State: "running", Health: "healthy", URL: "https://api.shop.localhost"},
		},
		Logs: []dashLog{{Service: "shop/api", Line: "GET /healthz 200"}},
	}
}

func newTestModel() dashboardModel {
	return newDashboardModel(context.Background(), func(context.Context) dashboardData { return sampleData() }, 0)
}

// TestDashboardDataMsg feeds a snapshot and asserts the table rows are populated.
func TestDashboardDataMsg(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(dashDataMsg(sampleData()))
	dm := updated.(dashboardModel)
	if len(dm.rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(dm.rows))
	}
	if got := len(dm.table.Rows()); got != 2 {
		t.Fatalf("table rows = %d, want 2", got)
	}
	if len(dm.logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(dm.logs))
	}
}

// TestDashboardQuitKey verifies q sets quitting and returns tea.Quit.
func TestDashboardQuitKey(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c", "esc"} {
		m := newTestModel()
		updated, cmd := m.Update(keyPress(key))
		dm := updated.(dashboardModel)
		if !dm.quit {
			t.Fatalf("key %q did not set quit", key)
		}
		if cmd == nil {
			t.Fatalf("key %q returned nil cmd (want tea.Quit)", key)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("key %q cmd is not tea.Quit", key)
		}
	}
}

// TestDashboardRefreshKey verifies r yields a fetch command producing a dashDataMsg.
func TestDashboardRefreshKey(t *testing.T) {
	m := newTestModel()
	_, cmd := m.Update(keyPress("r"))
	if cmd == nil {
		t.Fatal("r returned nil cmd")
	}
	if _, ok := cmd().(dashDataMsg); !ok {
		t.Fatalf("r cmd did not produce dashDataMsg, got %T", cmd())
	}
}

// TestDashboardResize verifies WindowSizeMsg records dimensions.
func TestDashboardResize(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	dm := updated.(dashboardModel)
	if dm.width != 120 || dm.height != 40 {
		t.Fatalf("dims = %dx%d, want 120x40", dm.width, dm.height)
	}
}

// TestDashboardView asserts the rendered view is non-empty and shows key content.
func TestDashboardView(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(dashDataMsg(sampleData()))
	dm := updated.(dashboardModel)
	dm2, _ := dm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := dm2.(dashboardModel).View()
	if strings.TrimSpace(view.Content) == "" {
		t.Fatal("view is empty")
	}
	for _, want := range []string{"dashboard", "shared-postgres", "logs", "[q]uit"} {
		if !strings.Contains(view.Content, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if !view.AltScreen {
		t.Error("dashboard view should request AltScreen")
	}
}

// TestDashboardSelectedDetail checks the right-pane detail for the highlighted row.
func TestDashboardSelectedDetail(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(dashDataMsg(sampleData()))
	dm := updated.(dashboardModel)
	detail := dm.selectedDetail()
	if !strings.Contains(detail, "shared-postgres") || !strings.Contains(detail, "refs: 2") {
		t.Fatalf("detail = %q", detail)
	}
}

// TestDashboardInitBatch verifies Init issues a fetch (poll=0 → no tick).
func TestDashboardInitBatch(t *testing.T) {
	m := newTestModel()
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init returned nil cmd")
	}
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "ctrl+c":
		return tea.KeyPressMsg{Mod: tea.ModCtrl, Code: 'c'}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		r := []rune(s)[0]
		return tea.KeyPressMsg{Code: r, Text: s}
	}
}
