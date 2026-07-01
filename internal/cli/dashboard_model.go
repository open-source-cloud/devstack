package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	table "charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// dashRow is one service row in the cockpit's top-left table (spec 16 layout).
type dashRow struct {
	Name     string   // shared-postgres | api/php
	Kind     string   // "shared" | "project"
	State    string   // running | exited | ...
	Health   string   // starting|healthy|unhealthy|"" (no healthcheck)
	Refs     int      // shared: referencing-project count
	Projects []string // shared: the referencing projects
	Engine   string   // shared: engine + version
	URL      string   // project: https://<svc>.<proj>.localhost
}

// dashLog is one recent, service-tagged log line for the bottom pane.
type dashLog struct {
	Service string
	Line    string
}

// dashboardData is the read-only snapshot the collector fans out to the model:
// service rows + a bounded tail of recent log lines. It is produced lock-free.
type dashboardData struct {
	Rows []dashRow
	Logs []dashLog
	Err  string // a collector-level error to surface in the footer
}

// dashDataMsg delivers a fresh snapshot into the model's Update loop.
type dashDataMsg dashboardData

// dashTickMsg is the safety-poll cadence (event streams are out of scope for the
// unit-testable model; a steady poll keeps the cockpit live).
type dashTickMsg struct{}

const dashLogCap = 500 // bounded ring for the log pane

// dashboardModel is the Bubble Tea cockpit. It is fully unit-testable without a
// TTY: construct it with a canned fetch, drive Update with messages, and inspect
// the table rows / View output.
type dashboardModel struct {
	ctx    context.Context
	fetch  func(context.Context) dashboardData
	poll   time.Duration
	theme  dashTheme
	table  table.Model
	rows   []dashRow
	logs   []dashLog
	width  int
	height int
	err    string
	quit   bool
}

// dashTheme carries the few styles the cockpit needs so it tracks internal/prompt.
type dashTheme struct {
	Title  lipgloss.Style
	Detail lipgloss.Style
	Footer lipgloss.Style
	Border lipgloss.Style
}

func defaultDashTheme() dashTheme {
	return dashTheme{
		Title:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")),
		Detail: lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		Footer: lipgloss.NewStyle().Faint(true),
		Border: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63")),
	}
}

// newDashboardModel builds the cockpit around an injected fetch (the read-only
// collector). poll<=0 disables the safety poll (used in tests).
func newDashboardModel(ctx context.Context, fetch func(context.Context) dashboardData, poll time.Duration) dashboardModel {
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "SERVICE", Width: 22},
			{Title: "STATE", Width: 10},
			{Title: "HEALTH", Width: 10},
			{Title: "REFS", Width: 18},
		}),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	return dashboardModel{ctx: ctx, fetch: fetch, poll: poll, theme: defaultDashTheme(), table: t}
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), m.tickCmd())
}

// fetchCmd runs the read-only collector off the render loop and delivers a snapshot.
func (m dashboardModel) fetchCmd() tea.Cmd {
	return func() tea.Msg {
		if m.fetch == nil {
			return dashDataMsg{}
		}
		return dashDataMsg(m.fetch(m.ctx))
	}
}

func (m dashboardModel) tickCmd() tea.Cmd {
	if m.poll <= 0 {
		return nil
	}
	return tea.Tick(m.poll, func(time.Time) tea.Msg { return dashTickMsg{} })
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dashDataMsg:
		m.rows = msg.Rows
		m.logs = clampLogs(msg.Logs)
		m.err = msg.Err
		m.table.SetRows(dashTableRows(m.rows))
		return m, nil
	case dashTickMsg:
		return m, tea.Batch(m.fetchCmd(), m.tickCmd())
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		case "r":
			return m, m.fetchCmd()
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// resize distributes the terminal size between the table and the log pane.
func (m *dashboardModel) resize() {
	if m.width > 0 {
		m.table.SetWidth(m.width)
	}
	// Reserve the top table (~half, min 4 rows) and leave the rest for logs.
	th := m.height/2 - 2
	if th < 4 {
		th = 4
	}
	m.table.SetHeight(th)
}

func (m dashboardModel) View() tea.View {
	var b strings.Builder
	b.WriteString(m.theme.Title.Render("devstack dashboard"))
	b.WriteString("  ")
	b.WriteString(m.theme.Footer.Render("read-only · no daemon · command-scoped"))
	b.WriteString("\n\n")
	b.WriteString(m.table.View())
	b.WriteString("\n\n")

	// Detail for the selected row.
	if d := m.selectedDetail(); d != "" {
		b.WriteString(m.theme.Detail.Render(d))
		b.WriteString("\n\n")
	}

	// Log pane.
	b.WriteString(m.theme.Title.Render("logs"))
	b.WriteString("\n")
	if len(m.logs) == 0 {
		b.WriteString(m.theme.Footer.Render("  (no recent log lines)"))
		b.WriteString("\n")
	}
	for _, l := range m.logsTail() {
		fmt.Fprintf(&b, "%-18s │ %s\n", l.Service, l.Line)
	}
	if m.err != "" {
		b.WriteString("\n")
		b.WriteString(m.theme.Footer.Render("! " + m.err))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Footer.Render("[r]efresh  [j/k]scroll  [q]uit"))

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// selectedDetail renders the right-pane detail for the highlighted service.
func (m dashboardModel) selectedDetail() string {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.rows) {
		return ""
	}
	r := m.rows[i]
	var parts []string
	parts = append(parts, "▸ "+r.Name)
	if r.Engine != "" {
		parts = append(parts, "engine: "+r.Engine)
	}
	if r.Kind == "shared" {
		parts = append(parts, fmt.Sprintf("refs: %d %v", r.Refs, r.Projects))
	}
	if r.URL != "" {
		parts = append(parts, "url: "+r.URL)
	}
	state := r.State
	if r.Health != "" {
		state += " (" + r.Health + ")"
	}
	parts = append(parts, "state: "+state)
	return strings.Join(parts, "   ")
}

// logsTail returns the last visible slice of the log ring for the pane height.
func (m dashboardModel) logsTail() []dashLog {
	n := m.height/2 - 4
	if n < 3 {
		n = 8
	}
	if len(m.logs) <= n {
		return m.logs
	}
	return m.logs[len(m.logs)-n:]
}

// dashTableRows projects rows into the bubbles table shape.
func dashTableRows(rows []dashRow) []table.Row {
	out := make([]table.Row, 0, len(rows))
	for _, r := range rows {
		refs := ""
		switch {
		case r.Kind == "shared":
			refs = fmt.Sprintf("refs=%d", r.Refs)
		case r.URL != "":
			refs = r.URL
		}
		out = append(out, table.Row{r.Name, r.State, r.Health, refs})
	}
	return out
}

func clampLogs(logs []dashLog) []dashLog {
	if len(logs) <= dashLogCap {
		return logs
	}
	return logs[len(logs)-dashLogCap:]
}
