package httpui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

func (m Model) View() tea.View {
	if m.quit {
		return tea.NewView("")
	}
	return tea.View{AltScreen: true, Content: Render(Snapshot{
		Routes:        m.routes,
		Logs:          m.logs,
		Selected:      m.selected,
		LogOffset:     m.logOffset,
		LogSelected:   m.logSelected,
		Focus:         m.focus,
		Mode:          m.mode,
		CreateStep:    m.createStep,
		PortInput:     m.portInput.View(),
		SubInput:      m.subInput.View(),
		DomainInput:   m.domainInput.View(),
		Notice:        m.notice,
		Error:         m.err,
		Width:         m.width,
		Height:        m.height,
		SelectedRoute: selectedRoute(m.routes, m.selected),
		ConfirmCancel: m.confirmCancel,
		HelpExpanded:  m.helpExpanded,
	})}
}

type Snapshot struct {
	Routes        []routes.Route
	Logs          []requestlog.Entry
	Selected      int
	LogOffset     int
	LogSelected   int
	Focus         focus
	Mode          mode
	CreateStep    int
	PortInput     string
	SubInput      string
	DomainInput   string
	Notice        string
	Error         string
	Width         int
	Height        int
	SelectedRoute routes.Route
	ConfirmCancel bool
	HelpExpanded  bool
}

func Render(snapshot Snapshot) string {
	width := snapshot.Width
	if width < 90 {
		width = 90
	}
	contentWidth := width - 2
	if contentWidth > 120 {
		contentWidth = 120
	}
	height := snapshot.Height
	if height < 24 {
		height = 24
	}

	var base []string
	base = append(base, renderHeader(snapshot, contentWidth))

	if contentWidth >= 92 {
		leftWidth := 38
		rightWidth := contentWidth - leftWidth - 2
		logRows := max(7, height-18)
		leftRows := logRows + 10
		base = append(base, lipgloss.JoinHorizontal(
			lipgloss.Top,
			renderTunnelList(snapshot, leftWidth, leftRows),
			"  ",
			renderRightPane(snapshot, rightWidth, logRows),
		))
	} else {
		base = append(base, renderTunnelList(snapshot, contentWidth, 0))
		base = append(base, "")
		base = append(base, renderRightPane(snapshot, contentWidth, 8))
	}

	lines := strings.Split(strings.Join(base, "\n"), "\n")
	if snapshot.Mode == modeCreate {
		lines = overlayCentered(lines, renderCreateForm(snapshot, modalWidth(contentWidth)), contentWidth)
	}
	if snapshot.Mode == modeConfirmStop {
		lines = overlayCentered(lines, renderConfirmStop(snapshot, modalWidth(contentWidth)), contentWidth)
	}
	if snapshot.Mode == modeRequestDetail {
		lines = overlayCentered(lines, renderRequestDetail(snapshot, modalWidth(contentWidth)), contentWidth)
	}
	if snapshot.Notice != "" {
		lines = append(lines, "", okStyle.Render(snapshot.Notice))
	}
	if snapshot.Error != "" {
		lines = append(lines, "", actionStyle.Render(snapshot.Error))
	}
	helpBar := help.New()
	helpBar.ShowAll = snapshot.HelpExpanded
	helpBar.SetWidth(contentWidth)
	lines = append(lines, "", helpBar.View(keys))
	return pageStyle.Render(strings.Join(lines, "\n"))
}

func renderHeader(snapshot Snapshot, width int) string {
	left := brandStyle.Render("vtunnel") + mutedStyle.Render(" http")
	selected := "none"
	if snapshot.SelectedRoute.Hostname != "" {
		selected = snapshot.SelectedRoute.Hostname
	}
	right := mutedStyle.Render(fmt.Sprintf("tunnels %d  selected %s", len(snapshot.Routes), selected))
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

func renderTunnelList(snapshot Snapshot, width int, minRows int) string {
	var lines []string
	if len(snapshot.Routes) == 0 {
		lines = append(lines, mutedStyle.Render("No active tunnels"))
		lines = append(lines, mutedStyle.Render("Press n to create one"))
		return renderBox("Tunnels (0)", strings.Join(padRows(lines, minRows), "\n"), width)
	}
	for index, route := range snapshot.Routes {
		prefix := " "
		style := routeStyle
		if index == snapshot.Selected {
			prefix = ">"
			if snapshot.Focus == focusTunnels {
				style = selectedRowStyle
			} else {
				style = selectedInactiveStyle
			}
		}
		name := routeName(route.Hostname)
		host := route.Hostname
		line := fmt.Sprintf("%s %-13s %s", prefix, name, host)
		lines = append(lines, style.Render(truncate(line, width-4)))
	}
	return renderBox(fmt.Sprintf("Tunnels (%d)", len(snapshot.Routes)), strings.Join(padRows(lines, minRows), "\n"), width)
}

func renderRightPane(snapshot Snapshot, width int, logRows int) string {
	return lipgloss.JoinVertical(
		lipgloss.Left,
		renderDetails(snapshot.SelectedRoute, width),
		"",
		renderLogs(snapshot, width, logRows),
		"",
		renderMetrics(snapshot.Logs, width),
	)
}

func renderDetails(route routes.Route, width int) string {
	if route.Hostname == "" {
		return renderBox("Details", mutedStyle.Render("Select or create a tunnel."), width)
	}
	body := strings.Join([]string{
		fmt.Sprintf("Destination: %s", commandStyle.Render(route.Target)),
		fmt.Sprintf("Public URL:  %s", commandStyle.Render("https://"+route.Hostname)),
	}, "\n")
	return renderBox("Details", body, width)
}

func renderLogs(snapshot Snapshot, width int, rows int) string {
	route := snapshot.SelectedRoute
	logs := snapshot.Logs
	if len(logs) == 0 {
		return renderBox(logTitle(route), strings.Join(padRows([]string{mutedStyle.Render("Waiting for requests...")}, rows), "\n"), width)
	}
	start := clamp(snapshot.LogOffset, 0, max(0, len(logs)-1))
	if start > max(0, len(logs)-rows) {
		start = max(0, len(logs)-rows)
	}
	end := min(len(logs), start+rows)
	lines := make([]string, 0, end-start)
	for index, entry := range logs[start:end] {
		prefix := " "
		if snapshot.Focus == focusLogs && index == snapshot.LogSelected {
			prefix = ">"
		}
		line := fmt.Sprintf("%-5s %3d %-36s %8s", entry.Method, entry.Status, entry.Path, entry.Duration.Round(time.Millisecond))
		style := statusColor(entry.Status)
		if snapshot.Focus == focusLogs && index == snapshot.LogSelected {
			style = selectedRowStyle
		}
		lines = append(lines, style.Render(truncate(prefix+" "+line, width-4)))
	}
	title := logTitle(route)
	if len(logs) > rows {
		title += fmt.Sprintf(" %d-%d/%d", start+1, end, len(logs))
	}
	return renderBox(title, strings.Join(padRows(lines, rows), "\n"), width)
}

func renderMetrics(logs []requestlog.Entry, width int) string {
	total, codes := metrics(logs)
	errors := codes[4] + codes[5]
	health := okStyle.Render("healthy")
	if errors > 0 {
		health = warnStyle.Render("has errors")
	}
	body := strings.Join([]string{
		fmt.Sprintf("Requests: %-6d Errors: %-4d Active: %-4d Health: %s", total, errors, 1, health),
		fmt.Sprintf("Status Codes: 2xx:%d   3xx:%d   4xx:%d   5xx:%d", codes[2], codes[3], codes[4], codes[5]),
		"Traffic: " + trafficBar(total, errors),
	}, "\n")
	return renderBox("Metrics", body, width)
}

func renderCreateForm(snapshot Snapshot, width int) string {
	rows := []string{
		createRow("Port", snapshot.PortInput, snapshot.CreateStep == 0),
		createRow("Subdomain", snapshot.SubInput, snapshot.CreateStep == 1),
		createRow("Domain", snapshot.DomainInput, snapshot.CreateStep == 2),
		"",
		mutedStyle.Render("enter next/create   tab move   esc cancel"),
	}
	return renderBox("New tunnel", strings.Join(rows, "\n"), width)
}

func createRow(label string, value string, active bool) string {
	prefix := "  "
	if active {
		prefix = "> "
	}
	return fmt.Sprintf("%s%-10s %s", prefix, label, value)
}

func renderConfirmStop(snapshot Snapshot, width int) string {
	host := snapshot.SelectedRoute.Hostname
	if host == "" {
		host = "selected tunnel"
	}
	inner := max(12, width-2)
	center := func(s string) string { return lipgloss.PlaceHorizontal(inner, lipgloss.Center, s) }
	body := strings.Join([]string{
		"",
		center(warnStyle.Render("Stop " + host + "?")),
		"",
		center(confirmButtons("Stop", "Cancel", snapshot.ConfirmCancel)),
		"",
		center(footerStyle.Render("←/→ choose · enter select · esc cancel")),
	}, "\n")
	return renderBox("Confirm", body, width)
}

func renderRequestDetail(snapshot Snapshot, width int) string {
	entry, ok := selectedLog(snapshot)
	if !ok {
		return renderBox("Request", mutedStyle.Render("No request selected."), width)
	}
	body := strings.Join([]string{
		fmt.Sprintf("Time:      %s", entry.Time.Format(time.RFC3339)),
		fmt.Sprintf("Host:      %s", entry.Hostname),
		fmt.Sprintf("Request:   %s %s", entry.Method, entry.Path),
		fmt.Sprintf("Status:    %d", entry.Status),
		fmt.Sprintf("Duration:  %s", entry.Duration.Round(time.Millisecond)),
		fmt.Sprintf("Bytes:     %d", entry.Bytes),
		fmt.Sprintf("Target:    %s", entry.Target),
		fmt.Sprintf("Remote:    %s", entry.RemoteAddr),
		"",
		mutedStyle.Render("enter/esc close"),
	}, "\n")
	return renderBox("Request detail", body, width)
}

func overlayCentered(lines []string, modal string, width int) []string {
	modalLines := strings.Split(modal, "\n")
	if len(lines) < len(modalLines)+4 {
		for len(lines) < len(modalLines)+4 {
			lines = append(lines, "")
		}
	}
	top := max(2, (len(lines)-len(modalLines))/2)
	modalWidth := 0
	for _, line := range modalLines {
		if lineWidth := lipgloss.Width(line); lineWidth > modalWidth {
			modalWidth = lineWidth
		}
	}
	left := max(0, (width-modalWidth)/2)
	for index, modalLine := range modalLines {
		target := top + index
		background := ""
		if target < len(lines) {
			background = stripLine(lines[target], width)
		}
		lines[target] = overlayLine(background, modalLine, left, width)
	}
	return lines
}

func overlayLine(background string, modal string, left int, width int) string {
	if lipgloss.Width(background) < width {
		background = padRight(background, width)
	}
	prefix := truncate(background, left)
	suffixStart := left + lipgloss.Width(modal)
	suffix := ""
	if suffixStart < lipgloss.Width(background) {
		suffix = trimLeftWidth(background, suffixStart)
	}
	line := prefix + modal + suffix
	return truncate(line, width)
}

func stripLine(line string, width int) string {
	plain := strings.Repeat(" ", min(width, lipgloss.Width(line)))
	if lipgloss.Width(plain) < width {
		plain += strings.Repeat(" ", width-lipgloss.Width(plain))
	}
	return plain
}

func trimLeftWidth(value string, width int) string {
	for lipgloss.Width(value) > 0 && width > 0 {
		runes := []rune(value)
		if len(runes) == 0 {
			return ""
		}
		value = string(runes[1:])
		width--
	}
	return value
}

func modalWidth(width int) int {
	if width < 74 {
		return max(48, width-6)
	}
	return 62
}

func renderBox(title string, body string, width int) string {
	if width < 12 {
		width = 12
	}
	innerWidth := width - 2
	title = " " + title + " "
	topFill := max(0, innerWidth-lipgloss.Width(title))
	lines := []string{borderStyle.Render("┌" + title + strings.Repeat("─", topFill) + "┐")}
	for _, line := range strings.Split(body, "\n") {
		line = truncate(line, innerWidth)
		lines = append(lines, borderStyle.Render("│")+padRight(line, innerWidth)+borderStyle.Render("│"))
	}
	lines = append(lines, borderStyle.Render("└"+strings.Repeat("─", innerWidth)+"┘"))
	return strings.Join(lines, "\n")
}

func logTitle(route routes.Route) string {
	if route.Hostname == "" {
		return "Logs"
	}
	return "Logs: " + routeName(route.Hostname)
}

func metrics(logs []requestlog.Entry) (int, map[int]int) {
	codes := map[int]int{}
	for _, entry := range logs {
		class := entry.Status / 100
		codes[class]++
	}
	return len(logs), codes
}

func statusColor(status int) lipgloss.Style {
	switch {
	case status >= 500:
		return actionStyle
	case status >= 400:
		return warnStyle
	case status >= 200 && status < 300:
		return okStyle
	default:
		return mutedStyle
	}
}

func selectedRoute(list []routes.Route, index int) routes.Route {
	if len(list) == 0 {
		return routes.Route{}
	}
	return list[clamp(index, 0, len(list)-1)]
}

func selectedLog(snapshot Snapshot) (requestlog.Entry, bool) {
	if len(snapshot.Logs) == 0 {
		return requestlog.Entry{}, false
	}
	index := clamp(snapshot.LogOffset+snapshot.LogSelected, 0, len(snapshot.Logs)-1)
	return snapshot.Logs[index], true
}

func routeName(hostname string) string {
	parts := strings.Split(hostname, ".")
	if len(parts) == 0 || parts[0] == "" {
		return hostname
	}
	return parts[0]
}

func padRows(lines []string, rows int) []string {
	if rows <= 0 {
		return lines
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return lines
}

func truncate(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+3 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func padRight(value string, width int) string {
	gap := width - lipgloss.Width(value)
	if gap <= 0 {
		return value
	}
	return value + strings.Repeat(" ", gap)
}

func trafficBar(total int, errors int) string {
	if total == 0 {
		return mutedStyle.Render("░░░░░░░░░░")
	}
	good := total - errors
	if good < 0 {
		good = 0
	}
	filled := good * 10 / total
	if filled < 1 && good > 0 {
		filled = 1
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
	if errors > 0 {
		return warnStyle.Render(bar)
	}
	return okStyle.Render(bar)
}

func clamp(value int, minValue int, maxValue int) int {
	if maxValue < minValue {
		return minValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func max(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func min(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

var (
	pageStyle   = lipgloss.NewStyle().Padding(1, 1)
	brandStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("66"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("48"))
	selectedInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("81"))
	routeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("151"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	actionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	commandStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))

	buttonStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("238")).Padding(0, 3)
	activeButtonStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(lipgloss.Color("48")).Padding(0, 3)
)

// confirmButtons renders a Confirm/Cancel button pair, highlighting the focused one.
func confirmButtons(confirmLabel, cancelLabel string, cancelFocused bool) string {
	confirm, cancel := buttonStyle.Render(confirmLabel), buttonStyle.Render(cancelLabel)
	if cancelFocused {
		cancel = activeButtonStyle.Render(cancelLabel)
	} else {
		confirm = activeButtonStyle.Render(confirmLabel)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, confirm, "   ", cancel)
}
