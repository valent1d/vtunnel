package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vtunnel/internal/api"
	"vtunnel/internal/config"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

type apiClient interface {
	Health(context.Context) (api.Health, error)
	ListRoutes(context.Context) ([]routes.Route, error)
	ListLogs(context.Context, requestlog.Filter) ([]requestlog.Entry, error)
	AddRoute(context.Context, routes.Route) error
	DeleteRoute(context.Context, string) error
	Shutdown(context.Context) error
}

type Model struct {
	client apiClient
	cfg    config.Config
	width  int
	height int

	tab           int
	selectedRoute int
	daemonRunning bool
	health        api.Health
	routes        []routes.Route
	logs          []requestlog.Entry
	err           string
	notice        string
	updatedAt     time.Time
	quitting      bool

	creating     bool
	focusedInput int
	inputs       []textinput.Model
}

type snapshotMsg struct {
	daemonRunning bool
	health        api.Health
	routes        []routes.Route
	logs          []requestlog.Entry
	err           string
	updatedAt     time.Time
}

type shutdownMsg struct {
	err string
}

type routeCreatedMsg struct {
	route routes.Route
	err   string
}

type routeDeletedMsg struct {
	hostname string
	err      string
}

type tickMsg time.Time

func Run(ctx context.Context, cfg config.Config) error {
	program := tea.NewProgram(
		NewModelWithConfig(api.New(cfg), cfg),
		tea.WithAltScreen(),
		tea.WithContext(ctx),
	)
	_, err := program.Run()
	return err
}

func NewModel(client apiClient) Model {
	return NewModelWithConfig(client, config.Default())
}

func NewModelWithConfig(client apiClient, cfg config.Config) Model {
	return Model{
		client:    client,
		cfg:       cfg,
		width:     96,
		height:    28,
		updatedAt: time.Now(),
		inputs:    newCreateInputs(cfg),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), tick())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.creating {
			return m.updateCreateForm(msg)
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "right", "l", "tab":
			m.tab = (m.tab + 1) % len(tabNames)
			return m, nil
		case "left", "h", "shift+tab":
			m.tab = (m.tab + len(tabNames) - 1) % len(tabNames)
			return m, nil
		case "1", "2", "3", "4":
			index := int(msg.String()[0] - '1')
			if index >= 0 && index < len(tabNames) {
				m.tab = index
			}
			return m, nil
		case "n":
			m.creating = true
			m.focusedInput = 0
			m.inputs = newCreateInputs(m.cfg)
			m.inputs[0].Focus()
			return m, nil
		case "down", "j":
			m.selectedRoute = clampRouteSelection(m.selectedRoute+1, len(m.routes))
			return m, nil
		case "up", "k":
			m.selectedRoute = clampRouteSelection(m.selectedRoute-1, len(m.routes))
			return m, nil
		case "x", "d":
			route, ok := selectedRoute(m.routes, m.selectedRoute)
			if !ok {
				m.err = "no route selected"
				return m, nil
			}
			return m, m.deleteRoute(route.Hostname)
		case "r":
			return m, m.fetch()
		case "s":
			return m, m.shutdown()
		}
	case snapshotMsg:
		m.daemonRunning = msg.daemonRunning
		m.health = msg.health
		m.routes = msg.routes
		m.selectedRoute = clampRouteSelection(m.selectedRoute, len(m.routes))
		m.logs = msg.logs
		m.err = msg.err
		if msg.err == "" || msg.err == "daemon stopped" {
			m.notice = ""
		}
		m.updatedAt = msg.updatedAt
		return m, nil
	case shutdownMsg:
		if msg.err != "" {
			m.err = msg.err
			return m, nil
		}
		return m, m.fetch()
	case routeCreatedMsg:
		if msg.err != "" {
			m.err = msg.err
			return m, nil
		}
		m.creating = false
		m.err = ""
		m.notice = fmt.Sprintf("created %s", msg.route.Hostname)
		m.tab = tabRoutes
		return m, m.fetch()
	case routeDeletedMsg:
		if msg.err != "" {
			m.err = msg.err
			return m, nil
		}
		m.err = ""
		m.notice = fmt.Sprintf("stopped %s", msg.hostname)
		m.selectedRoute = clampRouteSelection(m.selectedRoute, len(m.routes)-1)
		return m, m.fetch()
	case tickMsg:
		return m, tea.Batch(m.fetch(), tick())
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	return Render(Snapshot{
		DaemonRunning: m.daemonRunning,
		Health:        m.health,
		Routes:        m.routes,
		Logs:          m.logs,
		Error:         m.err,
		Notice:        m.notice,
		SelectedRoute: m.selectedRoute,
		LogFilter:     selectedRouteHostname(m.routes, m.selectedRoute),
		UpdatedAt:     m.updatedAt,
		Width:         m.width,
		Height:        m.height,
		Tab:           m.tab,
		Config:        m.cfg,
		Creating:      m.creating,
		Fields:        createFields(m.inputs, m.focusedInput),
	})
}

func (m Model) updateCreateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.creating = false
		m.err = ""
		return m, nil
	case "tab", "down":
		m.focusedInput = (m.focusedInput + 1) % len(m.inputs)
	case "shift+tab", "up":
		m.focusedInput = (m.focusedInput + len(m.inputs) - 1) % len(m.inputs)
	case "enter":
		if m.focusedInput < len(m.inputs)-1 {
			m.focusedInput++
		} else {
			route, err := routeFromInputs(m.inputs, m.cfg)
			if err != nil {
				m.err = err.Error()
				return m, nil
			}
			return m, m.createRoute(route)
		}
	}

	cmds := make([]tea.Cmd, len(m.inputs))
	for i := range m.inputs {
		if i == m.focusedInput {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		health, err := m.client.Health(ctx)
		if err != nil {
			return snapshotMsg{
				daemonRunning: false,
				err:           "daemon stopped",
				updatedAt:     time.Now(),
			}
		}
		routeList, err := m.client.ListRoutes(ctx)
		if err != nil {
			return snapshotMsg{
				daemonRunning: true,
				health:        health,
				err:           err.Error(),
				updatedAt:     time.Now(),
			}
		}
		logFilter := requestlog.Filter{
			Hostname: selectedRouteHostname(routeList, m.selectedRoute),
			Limit:    12,
		}
		logs, err := m.client.ListLogs(ctx, logFilter)
		if err != nil {
			return snapshotMsg{
				daemonRunning: true,
				health:        health,
				routes:        routeList,
				err:           err.Error(),
				updatedAt:     time.Now(),
			}
		}
		return snapshotMsg{
			daemonRunning: true,
			health:        health,
			routes:        routeList,
			logs:          logs,
			updatedAt:     time.Now(),
		}
	}
}

func (m Model) shutdown() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.client.Shutdown(ctx); err != nil {
			return shutdownMsg{err: err.Error()}
		}
		return shutdownMsg{}
	}
}

func (m Model) createRoute(route routes.Route) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.client.AddRoute(ctx, route); err != nil {
			return routeCreatedMsg{err: err.Error()}
		}
		return routeCreatedMsg{route: route}
	}
}

func (m Model) deleteRoute(hostname string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := m.client.DeleteRoute(ctx, hostname); err != nil {
			return routeDeletedMsg{hostname: hostname, err: err.Error()}
		}
		return routeDeletedMsg{hostname: hostname}
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

type Snapshot struct {
	DaemonRunning bool
	Health        api.Health
	Routes        []routes.Route
	Logs          []requestlog.Entry
	Error         string
	Notice        string
	SelectedRoute int
	LogFilter     string
	UpdatedAt     time.Time
	Width         int
	Height        int
	Tab           int
	Config        config.Config
	Creating      bool
	Fields        []CreateField
}

type CreateField struct {
	Label string
	Value string
	Focus bool
}

func Render(snapshot Snapshot) string {
	width := snapshot.Width
	if width < 72 {
		width = 72
	}
	contentWidth := width - 4
	if contentWidth > 104 {
		contentWidth = 104
	}
	if contentWidth < 72 {
		contentWidth = 72
	}

	lines := []string{
		renderHeader(snapshot, contentWidth),
		"",
		renderSummary(snapshot, contentWidth),
		"",
		renderTabs(snapshot.Tab),
		"",
	}
	if snapshot.Creating {
		lines = append(lines, splitLines(renderCreateForm(snapshot, contentWidth))...)
		lines = append(lines, "")
	}
	lines = append(lines, splitLines(renderCurrentTab(snapshot, contentWidth))...)
	lines = append(lines, "")

	footer := renderFooter(snapshot, contentWidth)
	if snapshot.Height > 0 {
		maxLines := snapshot.Height - 2
		for len(lines) < maxLines-1 {
			lines = append(lines, "")
		}
	}
	lines = append(lines, footer)
	return indentLines(lines, "  ")
}

func statusText(snapshot Snapshot) string {
	if snapshot.DaemonRunning {
		return "daemon: running"
	}
	return "daemon: stopped"
}

func renderHeader(snapshot Snapshot, width int) string {
	left := titleStyle.Render("vtunnel")
	right := statusStyle(snapshot.DaemonRunning).Render(statusText(snapshot))
	return joinEnds(left, right, width)
}

func renderSummary(snapshot Snapshot, width int) string {
	items := []string{
		fmt.Sprintf("routes %d", snapshot.Health.Routes),
		fmt.Sprintf("requests %d", snapshot.Health.Logs),
		"proxy " + valueOr(snapshot.Config.Proxy.Listen, "not configured"),
		"api " + valueOr(snapshot.Config.API.Listen, "not configured"),
	}
	return mutedStyle.Render(truncate(strings.Join(items, "    "), width))
}

func renderTabs(active int) string {
	parts := make([]string, len(tabNames))
	for i, name := range tabNames {
		label := fmt.Sprintf("%d %s", i+1, name)
		if i == active {
			parts[i] = activeTabStyle.Render(label)
		} else {
			parts[i] = tabStyle.Render(label)
		}
	}
	return strings.Join(parts, "    ")
}

func renderCurrentTab(snapshot Snapshot, width int) string {
	switch snapshot.Tab {
	case tabLogs:
		return renderLogs(snapshot.Logs, width, logsTitle(snapshot.LogFilter))
	case tabConfig:
		return renderConfig(snapshot.Config, width)
	case tabSetup:
		return renderSetup(snapshot.Config, width)
	default:
		return renderRouteDashboard(snapshot, width)
	}
}

func renderRoutes(routeList []routes.Route, selected int, width int) string {
	lines := []string{}
	if len(routeList) == 0 {
		lines = append(lines, emptyTitleStyle.Render("No active routes"))
		lines = append(lines, mutedStyle.Render("Press n to create one, or run vtunnel http 3000 dev --domain example.com"))
		return renderPanel("Active tunnels", lines, width)
	}
	lines = append(lines, mutedStyle.Render(fmt.Sprintf("%-34s  %s", "Hostname", "Target")))
	for index, route := range routeList {
		marker := " "
		if index == selected {
			marker = ">"
		}
		lines = append(lines, fmt.Sprintf("%s %s %-32s  %s", marker, dotStyle.Render("*"), truncate(route.Hostname, 32), route.Target))
	}
	return renderPanel("Active tunnels", lines, width)
}

func renderConfig(cfg config.Config, width int) string {
	lines := []string{}
	if cfg.DefaultDomain == "" {
		lines = append(lines, "Default domain: not configured")
	} else {
		lines = append(lines, "Default domain: "+cfg.DefaultDomain)
	}
	if len(cfg.Domains) == 0 {
		lines = append(lines, "Domains: none")
	} else {
		lines = append(lines, "Domains: "+strings.Join(cfg.Domains, ", "))
	}
	lines = append(lines, "Proxy: "+cfg.Proxy.Listen)
	lines = append(lines, "API: "+cfg.API.Listen)
	if cfg.Cloudflared.ConfigPath != "" {
		lines = append(lines, "cloudflared config: "+cfg.Cloudflared.ConfigPath)
	}
	return renderPanel("Config", lines, width)
}

func renderSetup(cfg config.Config, width int) string {
	lines := []string{}
	if len(cfg.Domains) == 0 {
		lines = append(lines, "Add domains with: vtunnel setup --domain example.com")
		lines = append(lines, "Then point cloudflared wildcard ingress at http://"+cfg.Proxy.Listen)
		return renderPanel("Setup", lines, width)
	}
	lines = append(lines, "cloudflared ingress:")
	for _, domain := range cfg.Domains {
		lines = append(lines, fmt.Sprintf(`  - hostname: "*.%s"`, domain))
		lines = append(lines, "    service: http://"+cfg.Proxy.Listen)
	}
	lines = append(lines, "  - service: http_status:404")
	return renderPanel("Setup", lines, width)
}

func renderCreateForm(snapshot Snapshot, width int) string {
	lines := []string{}
	for _, field := range snapshot.Fields {
		prefix := "  "
		if field.Focus {
			prefix = "> "
		}
		value := field.Value
		if value == "" {
			value = mutedStyle.Render("empty")
		}
		lines = append(lines, fmt.Sprintf("%s%-10s %s", prefix, field.Label+":", value))
	}
	lines = append(lines, mutedStyle.Render("enter next/create   esc cancel"))
	return renderPanel("New tunnel", lines, width)
}

func renderLogs(logs []requestlog.Entry, width int, title string) string {
	lines := []string{}
	if len(logs) == 0 {
		lines = append(lines, emptyTitleStyle.Render("No requests yet"))
		lines = append(lines, mutedStyle.Render("Requests will appear here as public traffic reaches vtunnel."))
		return renderPanel(title, lines, width)
	}
	lines = append(lines, mutedStyle.Render(fmt.Sprintf("%-8s  %-6s %-4s %-28s %-8s %s", "Time", "Method", "Code", "Host", "Latency", "Path")))
	for _, entry := range logs {
		line := fmt.Sprintf(
			"%-8s  %-6s %-4d %-28s %-8s %s",
			entry.Time.Format("15:04:05"),
			entry.Method,
			entry.Status,
			truncate(entry.Hostname, 28),
			formatDuration(entry.Duration),
			entry.Path,
		)
		lines = append(lines, truncate(line, width))
	}
	return renderPanel(title, lines, width)
}

func renderRouteDashboard(snapshot Snapshot, width int) string {
	sections := []string{renderRoutes(snapshot.Routes, snapshot.SelectedRoute, width)}
	if len(snapshot.Logs) > 0 {
		sections = append(sections, renderLogs(limitLogs(snapshot.Logs, 5), width, logsTitle(snapshot.LogFilter)))
	} else {
		sections = append(sections, renderPanel("Recent requests", []string{
			emptyTitleStyle.Render("No requests yet"),
			mutedStyle.Render("Traffic logs will appear here once a route receives requests."),
		}, width))
	}
	return strings.Join(sections, "\n\n")
}

func renderFooter(snapshot Snapshot, width int) string {
	updated := "never"
	if !snapshot.UpdatedAt.IsZero() {
		updated = snapshot.UpdatedAt.Format("15:04:05")
	}
	parts := []string{
		"tab switch",
		"n new",
		"up/down select",
		"x stop route",
		"r refresh",
		"s stop daemon",
		"q quit",
		"updated " + updated,
	}
	if snapshot.Error != "" && snapshot.Error != "daemon stopped" {
		parts = append(parts, "error: "+snapshot.Error)
	}
	if snapshot.Notice != "" {
		parts = append(parts, snapshot.Notice)
	}
	return footerStyle.Render(truncate(strings.Join(parts, "   "), width))
}

func renderPanel(title string, lines []string, width int) string {
	if width < 40 {
		width = 40
	}
	body := make([]string, 0, len(lines)+2)
	body = append(body, sectionTitleStyle.Render(title))
	body = append(body, ruleStyle.Render(strings.Repeat("-", min(width, 96))))
	body = append(body, "")
	for _, line := range lines {
		body = append(body, "  "+truncate(line, width-2))
	}
	return strings.Join(body, "\n")
}

func splitLines(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

func indentLines(lines []string, prefix string) string {
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			out[i] = ""
			continue
		}
		out[i] = prefix + line
	}
	return strings.Join(out, "\n")
}

func joinEnds(left string, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func valueOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func limitLogs(entries []requestlog.Entry, limit int) []requestlog.Entry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	return entries[len(entries)-limit:]
}

func logsTitle(hostname string) string {
	if hostname == "" {
		return "Request logs"
	}
	return "Request logs: " + hostname
}

func selectedRoute(routeList []routes.Route, selected int) (routes.Route, bool) {
	if len(routeList) == 0 {
		return routes.Route{}, false
	}
	selected = clampRouteSelection(selected, len(routeList))
	return routeList[selected], true
}

func selectedRouteHostname(routeList []routes.Route, selected int) string {
	route, ok := selectedRoute(routeList, selected)
	if !ok {
		return ""
	}
	return route.Hostname
}

func clampRouteSelection(selected int, routeCount int) int {
	if routeCount <= 0 {
		return 0
	}
	if selected < 0 {
		return 0
	}
	if selected >= routeCount {
		return routeCount - 1
	}
	return selected
}

func statusStyle(running bool) lipgloss.Style {
	if running {
		return okStyle
	}
	return warnStyle
}

func formatDuration(duration time.Duration) string {
	if duration < time.Millisecond {
		return fmt.Sprintf("%dus", duration.Microseconds())
	}
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	return duration.Truncate(100 * time.Millisecond).String()
}

func truncate(value string, maxWidth int) string {
	if maxWidth <= 0 || lipgloss.Width(value) <= maxWidth {
		return value
	}
	if maxWidth <= 3 {
		return value[:maxWidth]
	}
	return value[:maxWidth-3] + "..."
}

func newCreateInputs(cfg config.Config) []textinput.Model {
	inputs := []textinput.Model{
		textinput.New(),
		textinput.New(),
		textinput.New(),
	}
	inputs[0].Placeholder = "dev"
	inputs[0].Prompt = ""
	inputs[1].Placeholder = "3000"
	inputs[1].Prompt = ""
	inputs[2].Placeholder = "example.com"
	inputs[2].Prompt = ""
	inputs[2].SetValue(cfg.DefaultDomain)
	return inputs
}

func createFields(inputs []textinput.Model, focused int) []CreateField {
	labels := []string{"Subdomain", "Port", "Domain"}
	fields := make([]CreateField, 0, len(inputs))
	for i, input := range inputs {
		fields = append(fields, CreateField{
			Label: labels[i],
			Value: input.Value(),
			Focus: i == focused,
		})
	}
	return fields
}

func routeFromInputs(inputs []textinput.Model, cfg config.Config) (routes.Route, error) {
	subdomain := routes.NormalizeHostname(inputs[0].Value())
	if subdomain == "" {
		return routes.Route{}, fmt.Errorf("subdomain is required")
	}
	port := strings.TrimSpace(inputs[1].Value())
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return routes.Route{}, fmt.Errorf("port must be a number")
	}
	domain := routes.NormalizeHostname(inputs[2].Value())
	if domain == "" {
		domain = routes.NormalizeHostname(cfg.DefaultDomain)
	}
	if domain == "" && !strings.Contains(subdomain, ".") {
		return routes.Route{}, fmt.Errorf("domain is required")
	}
	hostname := subdomain
	if !strings.Contains(hostname, ".") {
		hostname = hostname + "." + domain
	}
	route := routes.Route{
		Hostname: hostname,
		Target:   "http://127.0.0.1:" + port,
	}
	if err := routes.Validate(route); err != nil {
		return routes.Route{}, err
	}
	return route, nil
}

const (
	tabRoutes = iota
	tabLogs
	tabConfig
	tabSetup
)

var tabNames = []string{"Routes", "Logs", "Config", "Setup"}

var (
	pageStyle = lipgloss.NewStyle().
			Padding(1, 2)
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("42"))
	sectionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("15"))
	emptyTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15"))
	ruleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("238"))
	tabStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("42"))
	okStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("42"))
	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))
	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
	dotStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))
	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))
)
