// haco tui: a read-only, auto-refreshing dashboard over the same API
// every other haco command already uses. v1 is deliberately read-only
// (roadmap item 8 names a "freeze"/override pass and a full web UI as
// later work) — this is the ahead-of-the-web-UI terminal equivalent,
// reusing the view structs and fetch helpers the table-rendering commands
// already built rather than duplicating HTTP/JSON-decoding code.
package main

import (
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tuiTab int

// tabDashboard and tabLogs are rendered as scrollable plain text in a
// viewport (Dashboard composes every other tab's table into one view;
// Logs tails the control-plane log file) — every other tab is a real
// bubbles table.Model. isTextTab below is what the rest of the file
// switches on to tell the two kinds apart.
const (
	tabDashboard tuiTab = iota
	tabNodes
	tabWorkloads
	tabContainers
	tabEvents
	tabMetrics
	tabServices
	tabProjects
	tabLogs
	tabCount
)

var tuiTabNames = [tabCount]string{"Dashboard", "Nodes", "Workloads", "Containers", "Events", "Metrics", "Services", "Projects", "Logs"}

func isTextTab(tab tuiTab) bool { return tab == tabDashboard || tab == tabLogs }

// eventView and metricsView have no existing CLI view struct to reuse —
// `haco events`/`haco metrics` just dump raw JSON today — so these
// are new, matching store.Event's and store.Metrics' JSON shape.
type eventView struct {
	ID          int       `json:"ID"`
	WorkloadKey string    `json:"WorkloadKey"`
	NodeID      string    `json:"NodeID"`
	Type        string    `json:"Type"`
	Message     string    `json:"Message"`
	CreatedAt   time.Time `json:"CreatedAt"`
}

type metricsView struct {
	NodesByAvailability map[string]int `json:"nodes_by_availability"`
	WorkloadsByStatus   map[string]int `json:"workloads_by_status"`
}

var (
	tuiTabStyle       = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color("244"))
	tuiActiveTabStyle = lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	tuiStatusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	tuiErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	tuiHelpStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// tuiModel is a Bubble Tea model: one table per tab (so switching tabs
// doesn't lose scroll position or require re-fetching immediately), a
// shared project scope fixed at startup (no interactive filtering in
// v1), and the last refresh outcome for the status bar.
type tuiModel struct {
	baseURL string
	project string

	activeTab tuiTab
	tables    [tabCount]table.Model
	viewports [tabCount]viewport.Model
	loaded    [tabCount]bool

	width, height int
	lastRefresh   time.Time
	err           error
}

// tuiDataMsg carries one tab's freshly fetched table data back to Update —
// the tea.Cmd that produces it runs on Bubble Tea's own goroutine, never
// blocking the UI's event loop (the same non-blocking-poll principle
// already applied to the agent's dependency/health polling).
type tuiDataMsg struct {
	tab  tuiTab
	cols []table.Column
	rows []table.Row
	err  error
}

// tuiTextMsg is tuiDataMsg's counterpart for a text-kind tab (Dashboard,
// Logs) — pre-rendered content for a viewport instead of table columns/
// rows. A text tab's own fetch never fails the whole refresh over a local
// problem (e.g. the log file not existing): any error is folded into
// content itself, not surfaced as err — err here is reserved for an actual
// control-plane connectivity failure, consistent with tuiDataMsg.
type tuiTextMsg struct {
	tab     tuiTab
	content string
	err     error
}

type tuiTickMsg time.Time

func runTUI(baseURL string, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: haco tui [project]")
	}
	project := ""
	if len(args) == 1 {
		project = args[0]
	}
	m := newTUIModel(baseURL, project)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func newTUIModel(baseURL, project string) tuiModel {
	m := tuiModel{baseURL: baseURL, project: project}
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).BorderBottom(true)
	styles.Selected = styles.Selected.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Bold(false)
	for i := range m.tables {
		t := table.New(table.WithFocused(true), table.WithHeight(20))
		t.SetStyles(styles)
		m.tables[i] = t
		m.viewports[i] = viewport.New(80, 20)
	}
	return m
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(m.activeTab), tuiTickCmd())
}

func tuiTickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tuiTickMsg(t) })
}

// fetchCmd returns a tea.Cmd — a plain func() tea.Msg Bubble Tea runs off
// the UI goroutine — so a slow or hanging control plane never freezes
// keypresses or tab switching; a failed poll becomes a tuiDataMsg/
// tuiTextMsg with err set, handled by Update as "disconnected, retrying",
// never a crash.
func (m tuiModel) fetchCmd(tab tuiTab) tea.Cmd {
	baseURL, project := m.baseURL, m.project
	if isTextTab(tab) {
		return func() tea.Msg {
			content, err := fetchTextTabContent(baseURL, project, tab)
			return tuiTextMsg{tab: tab, content: content, err: err}
		}
	}
	return func() tea.Msg {
		cols, rows, err := fetchTabData(baseURL, project, tab)
		return tuiDataMsg{tab: tab, cols: cols, rows: rows, err: err}
	}
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h := msg.Height - 6
		if h < 3 {
			h = 3
		}
		for i := range m.tables {
			m.tables[i].SetWidth(msg.Width)
			m.tables[i].SetHeight(h)
			m.viewports[i].Width = msg.Width
			m.viewports[i].Height = h
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right", "l":
			m.activeTab = (m.activeTab + 1) % tabCount
			return m, m.fetchCmd(m.activeTab)
		case "shift+tab", "left", "h":
			m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
			return m, m.fetchCmd(m.activeTab)
		case "r":
			return m, m.fetchCmd(m.activeTab)
		case "up", "k", "down", "j":
			var cmd tea.Cmd
			if isTextTab(m.activeTab) {
				m.viewports[m.activeTab], cmd = m.viewports[m.activeTab].Update(msg)
			} else {
				m.tables[m.activeTab], cmd = m.tables[m.activeTab].Update(msg)
			}
			return m, cmd
		}
		return m, nil

	case tuiTickMsg:
		return m, tea.Batch(m.fetchCmd(m.activeTab), tuiTickCmd())

	case tuiDataMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.lastRefresh = time.Now()
		m.loaded[msg.tab] = true
		m.tables[msg.tab].SetColumns(msg.cols)
		m.tables[msg.tab].SetRows(msg.rows)
		return m, nil

	case tuiTextMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.lastRefresh = time.Now()
		m.loaded[msg.tab] = true
		m.viewports[msg.tab].SetContent(msg.content)
		return m, nil
	}
	return m, nil
}

func (m tuiModel) View() string {
	var b strings.Builder

	for i, name := range tuiTabNames {
		style := tuiTabStyle
		if tuiTab(i) == m.activeTab {
			style = tuiActiveTabStyle
		}
		b.WriteString(style.Render(name))
	}
	b.WriteString("\n\n")

	if !m.loaded[m.activeTab] && m.err == nil {
		b.WriteString(tuiStatusStyle.Render("loading..."))
	} else if isTextTab(m.activeTab) {
		b.WriteString(m.viewports[m.activeTab].View())
	} else {
		b.WriteString(m.tables[m.activeTab].View())
	}
	b.WriteString("\n\n")

	var status string
	switch {
	case m.err != nil:
		status = tuiErrorStyle.Render(fmt.Sprintf("disconnected, retrying... (%s)", m.err.Error()))
	case m.lastRefresh.IsZero():
		status = tuiStatusStyle.Render("connecting...")
	default:
		status = tuiStatusStyle.Render("last refresh: " + m.lastRefresh.Format("15:04:05"))
	}
	b.WriteString(status)
	b.WriteString("\n")
	b.WriteString(tuiHelpStyle.Render("tab/shift+tab or ←/→: switch tab   ↑/↓ or j/k: scroll/move   r: refresh   q: quit"))
	return b.String()
}

// fetchTabData fetches and shapes one tab's data — the only place that
// talks to the control plane for the TUI, reusing the same view structs
// and fetchJSON helper the equivalent CLI table commands already use
// (workloadRecordView/nodeView/serviceInstanceView/projectSummaryView),
// plus intListOrDash/internalURLs for the containers tab.
func fetchTabData(baseURL, project string, tab tuiTab) ([]table.Column, []table.Row, error) {
	switch tab {
	case tabNodes:
		return fetchNodesTab(baseURL)
	case tabWorkloads:
		return fetchWorkloadsTab(baseURL, project)
	case tabContainers:
		return fetchContainersTab(baseURL, project)
	case tabEvents:
		return fetchEventsTab(baseURL, project)
	case tabMetrics:
		return fetchMetricsTab(baseURL, project)
	case tabServices:
		return fetchServicesTab(baseURL, project)
	case tabProjects:
		return fetchProjectsTab(baseURL)
	default:
		return nil, nil, fmt.Errorf("unknown tab %d", tab)
	}
}

func withProjectQuery(path, project string) string {
	if project == "" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "project=" + neturl.QueryEscape(project)
}

func fetchNodesTab(baseURL string) ([]table.Column, []table.Row, error) {
	var nodes []nodeView
	if err := fetchJSON(baseURL, "/v1/nodes", &nodes); err != nil {
		return nil, nil, err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	cols := []table.Column{
		{Title: "NODE", Width: 16}, {Title: "STATUS", Width: 12}, {Title: "ADDRESS", Width: 15},
		{Title: "ARCH", Width: 7}, {Title: "CPU(m)", Width: 8}, {Title: "MEM(Mi)", Width: 9},
		{Title: "HEARTBEAT", Width: 20},
	}
	rows := make([]table.Row, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, table.Row{
			n.ID, n.Availability, orDash(n.Address), orDash(n.Architecture),
			strconv.FormatInt(n.Allocatable.CPUMillis, 10),
			strconv.FormatInt(n.Allocatable.MemoryBytes/1024/1024, 10),
			n.HeartbeatAt.Format(time.RFC3339),
		})
	}
	return cols, rows, nil
}

func fetchWorkloadsTab(baseURL, project string) ([]table.Column, []table.Row, error) {
	var w []workloadRecordView
	if err := fetchJSON(baseURL, withProjectQuery("/v1/workloads", project), &w); err != nil {
		return nil, nil, err
	}
	sort.Slice(w, func(i, j int) bool { return w[i].Project+w[i].Service < w[j].Project+w[j].Service })
	cols := []table.Column{
		{Title: "PROJECT", Width: 14}, {Title: "SERVICE", Width: 14}, {Title: "REVISION", Width: 10},
		{Title: "NODE", Width: 14}, {Title: "STATUS", Width: 20}, {Title: "REASON", Width: 30},
	}
	rows := make([]table.Row, 0, len(w))
	for _, rec := range w {
		rows = append(rows, table.Row{rec.Project, rec.Service, rec.Revision, orDash(rec.NodeID), rec.Status, orDash(rec.StatusReason)})
	}
	return cols, rows, nil
}

func fetchContainersTab(baseURL, project string) ([]table.Column, []table.Row, error) {
	var all []workloadRecordView
	if err := fetchJSON(baseURL, withProjectQuery("/v1/workloads", project), &all); err != nil {
		return nil, nil, err
	}
	w := make([]workloadRecordView, 0, len(all))
	for _, rec := range all {
		if rec.Status == "stopped" || rec.Status == "failed" {
			continue
		}
		w = append(w, rec)
	}
	sort.Slice(w, func(i, j int) bool {
		if w[i].NodeID != w[j].NodeID {
			return w[i].NodeID < w[j].NodeID
		}
		return w[i].Project+w[i].Service < w[j].Project+w[j].Service
	})
	cols := []table.Column{
		{Title: "NODE", Width: 14}, {Title: "PROJECT", Width: 12}, {Title: "SERVICE", Width: 12},
		{Title: "CONTAINER ID", Width: 14}, {Title: "STATUS", Width: 10}, {Title: "PORTS", Width: 12},
		{Title: "INTERNAL URL", Width: 20},
	}
	rows := make([]table.Row, 0, len(w))
	for _, rec := range w {
		cid := rec.ContainerID
		if len(cid) > 12 {
			cid = cid[:12]
		}
		rows = append(rows, table.Row{
			orDash(rec.NodeID), rec.Project, rec.Service, orDash(cid), rec.Status,
			intListOrDash(rec.Constraints.HostPorts), internalURLs(rec.Service, rec.ContainerPorts),
		})
	}
	return cols, rows, nil
}

func fetchEventsTab(baseURL, project string) ([]table.Column, []table.Row, error) {
	var events []eventView
	if err := fetchJSON(baseURL, withProjectQuery("/v1/events", project), &events); err != nil {
		return nil, nil, err
	}
	cols := []table.Column{
		{Title: "TIME", Width: 20}, {Title: "WORKLOAD", Width: 28}, {Title: "NODE", Width: 14},
		{Title: "TYPE", Width: 14}, {Title: "MESSAGE", Width: 40},
	}
	rows := make([]table.Row, 0, len(events))
	for _, e := range events {
		msg := strings.ReplaceAll(e.Message, "\n", " ")
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
		rows = append(rows, table.Row{e.CreatedAt.Format(time.RFC3339), e.WorkloadKey, orDash(e.NodeID), e.Type, msg})
	}
	return cols, rows, nil
}

func fetchMetricsTab(baseURL, project string) ([]table.Column, []table.Row, error) {
	var m metricsView
	if err := fetchJSON(baseURL, withProjectQuery("/v1/metrics", project), &m); err != nil {
		return nil, nil, err
	}
	cols := []table.Column{{Title: "CATEGORY", Width: 20}, {Title: "KEY", Width: 20}, {Title: "COUNT", Width: 8}}
	var rows []table.Row
	for _, k := range sortedKeys(m.NodesByAvailability) {
		rows = append(rows, table.Row{"node availability", k, strconv.Itoa(m.NodesByAvailability[k])})
	}
	for _, k := range sortedKeys(m.WorkloadsByStatus) {
		rows = append(rows, table.Row{"workload status", k, strconv.Itoa(m.WorkloadsByStatus[k])})
	}
	return cols, rows, nil
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fetchServicesTab(baseURL, project string) ([]table.Column, []table.Row, error) {
	cols := []table.Column{
		{Title: "SERVICE", Width: 16}, {Title: "NODE", Width: 14}, {Title: "ADDRESS", Width: 15},
		{Title: "PORT", Width: 8}, {Title: "STATUS", Width: 20},
	}
	if project == "" {
		return cols, []table.Row{{"(start with a project, e.g. haco tui <project>, to see services)", "", "", "", ""}}, nil
	}
	var out []serviceInstanceView
	if err := fetchJSON(baseURL, "/v1/services/"+neturl.PathEscape(project), &out); err != nil {
		return nil, nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service+out[i].NodeID < out[j].Service+out[j].NodeID })
	rows := make([]table.Row, 0, len(out))
	for _, si := range out {
		rows = append(rows, table.Row{si.Service, si.NodeID, si.Address, strconv.Itoa(si.ContainerPort), si.Status})
	}
	return cols, rows, nil
}

func fetchProjectsTab(baseURL string) ([]table.Column, []table.Row, error) {
	var out []projectSummaryView
	if err := fetchJSON(baseURL, "/v1/projects", &out); err != nil {
		return nil, nil, err
	}
	cols := []table.Column{
		{Title: "PROJECT", Width: 18}, {Title: "WORKLOADS", Width: 10}, {Title: "READY", Width: 8},
		{Title: "FAILED", Width: 8}, {Title: "STOPPED", Width: 8}, {Title: "OTHER", Width: 8},
	}
	rows := make([]table.Row, 0, len(out))
	for _, p := range out {
		ready, failed, stopped := p.ByStatus["ready"], p.ByStatus["failed"], p.ByStatus["stopped"]
		other := p.Total - ready - failed - stopped
		rows = append(rows, table.Row{
			p.Project, strconv.Itoa(p.Total), strconv.Itoa(ready),
			strconv.Itoa(failed), strconv.Itoa(stopped), strconv.Itoa(other),
		})
	}
	return cols, rows, nil
}

// fetchTextTabContent is fetchTabData's counterpart for the two text-kind
// tabs.
func fetchTextTabContent(baseURL, project string, tab tuiTab) (string, error) {
	switch tab {
	case tabDashboard:
		return fetchDashboardContent(baseURL, project)
	case tabLogs:
		return fetchLogsContent()
	default:
		return "", fmt.Errorf("unknown text tab %d", tab)
	}
}

// dashboardSections is every other tab in display order — "show all tabs
// in a single window" — each rendered as its own small plain-text table
// via renderPlainTable, reusing the exact same fetch functions (and so the
// exact same columns) the dedicated tab uses, rather than a second,
// divergent implementation.
var dashboardSections = []struct {
	title string
	tab   tuiTab
}{
	{"Nodes", tabNodes},
	{"Workloads", tabWorkloads},
	{"Containers", tabContainers},
	{"Events", tabEvents},
	{"Metrics", tabMetrics},
	{"Services", tabServices},
	{"Projects", tabProjects},
}

var dashboardSectionTitleStyle = lipgloss.NewStyle().Bold(true).Underline(true)

func fetchDashboardContent(baseURL, project string) (string, error) {
	var b strings.Builder
	for i, sec := range dashboardSections {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(dashboardSectionTitleStyle.Render(sec.title))
		b.WriteString("\n")
		cols, rows, err := fetchTabData(baseURL, project, sec.tab)
		if err != nil {
			// One section failing (e.g. a transient error on just that
			// query) shouldn't blank the whole dashboard — show it inline
			// and keep going, the same "don't let one bad part crash
			// everything" principle as the non-blocking poll fixes.
			b.WriteString(tuiErrorStyle.Render("error: " + err.Error()))
			continue
		}
		if len(rows) == 0 {
			b.WriteString("(none)")
			continue
		}
		b.WriteString(renderPlainTable(cols, rows))
	}
	return b.String(), nil
}

// renderPlainTable formats cols/rows as a fixed-width plain-text table —
// the Dashboard tab's building block, since a single bubbles table.Model
// only holds one column schema and Dashboard combines several.
func renderPlainTable(cols []table.Column, rows []table.Row) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(padOrTruncate(c.Title, c.Width))
	}
	b.WriteString("\n")
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			width := 10
			if i < len(cols) {
				width = cols[i].Width
			}
			b.WriteString(padOrTruncate(cell, width))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func padOrTruncate(s string, width int) string {
	if len(s) > width {
		if width <= 1 {
			return s[:width]
		}
		return s[:width-1] + "…"
	}
	return s + strings.Repeat(" ", width-len(s))
}

// fetchLogsContent reads the control-plane log file's tail — local-only
// (same assumption `haco start --logs` already makes: the file lives
// wherever HACO_DATA_DIR/controlplane.log resolves to on this machine).
// Any error is folded into the returned content instead of propagated —
// a missing log file is a local-filesystem fact, not a control-plane
// connectivity failure, so it shouldn't flip the "disconnected, retrying"
// status the other tabs use err for.
const tuiLogTailLines = 500

func fetchLogsContent() (string, error) {
	dataDir := envOr("HACO_DATA_DIR", filepath.Join(homeDir(), ".haco"))
	path := filepath.Join(dataDir, "controlplane.log")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("could not read %s: %v\n\n(the control plane may be running on a different machine, or hasn't been started with `haco start` here)", path, err), nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > tuiLogTailLines {
		lines = lines[len(lines)-tuiLogTailLines:]
	}
	return strings.Join(lines, "\n"), nil
}
