package httpui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"vtunnel/internal/cloudflared"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

func (m Model) View() tea.View {
	if m.quit {
		return tea.NewView("")
	}
	return tea.View{AltScreen: true, Content: Render(Snapshot{
		Routes:             m.routes,
		Logs:               m.logs,
		Selected:           m.selected,
		LogCursor:          m.logCursor,
		Focus:              m.focus,
		Mode:               m.mode,
		CreateStep:         m.createStep,
		PortInput:          m.portInput.View(),
		SubInput:           m.subInput.View(),
		DomainInput:        m.domainInput.View(),
		Notice:             m.notice,
		Error:              m.err,
		Width:              m.width,
		Height:             m.height,
		SelectedRoute:      selectedRoute(m.routes, m.selected),
		ConfirmCancel:      m.confirmCancel,
		HelpExpanded:       m.helpExpanded,
		Edge:               m.edge,
		Detail:             m.detail,
		DetailErr:          m.detailErr,
		CreateProtectLabel: accessModeLabel[m.createProtect],
		CreateRows:         m.createRows(),
		OrbErr:             m.orbErr,
		AccessModes:        m.accessModes,
		AccessMode:         m.accessMode,
		AccessField:        m.accessFocus,
		AccessHost:         m.accessTarget,
		AccessProtected:    m.accessWasProtected,
		AccessPaused:       m.accessPaused,
		AccessIdPs:         m.accessIdPs,
		AccessIdPIndex:     m.accessIdPIndex,
		AllowInput:         m.allowInput.View(),
		AccessBusy:         m.accessBusy,
		AccessErr:          m.accessErr,
		Now:                time.Now(),
	})}
}

type Snapshot struct {
	Routes             []routes.Route
	Logs               []requestlog.Entry
	Selected           int
	LogCursor          int
	Focus              focus
	Mode               mode
	CreateStep         int
	PortInput          string
	SubInput           string
	DomainInput        string
	Notice             string
	Error              string
	Width              int
	Height             int
	SelectedRoute      routes.Route
	ConfirmCancel      bool
	HelpExpanded       bool
	Edge               cloudflared.EdgeStatus
	Now                time.Time
	Detail             *requestlog.Exchange
	DetailErr          string
	CreateProtectLabel string
	CreateRows         []CreateRow
	OrbErr             string
	AccessModes        []string
	AccessMode         int
	AccessField        int
	AccessHost         string
	AccessProtected    bool
	AccessPaused       bool
	AccessIdPs         []string
	AccessIdPIndex     int
	AllowInput         string
	AccessBusy         bool
	AccessErr          string
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

	helpBar := help.New()
	helpBar.ShowAll = snapshot.HelpExpanded
	helpBar.SetWidth(contentWidth)
	helpView := helpBar.View(keys)
	// Reserve rows for the page padding, header, blank line and the help bar,
	// plus a row of slack, so the command bar is never clipped off the bottom.
	availableRows := max(12, height-6-strings.Count(helpView, "\n"))

	var base []string
	base = append(base, renderHeader(snapshot, contentWidth))

	if contentWidth >= 92 {
		leftWidth := 38
		rightWidth := contentWidth - leftWidth - 2
		right := renderRightPane(snapshot, rightWidth, availableRows)
		// Size the tunnel sidebar to the right pane's height so the columns align.
		leftRows := max(0, strings.Count(right, "\n")-1)
		base = append(base, lipgloss.JoinHorizontal(
			lipgloss.Top,
			renderTunnelList(snapshot, leftWidth, leftRows),
			"  ",
			right,
		))
	} else {
		sidebar := renderTunnelList(snapshot, contentWidth, 0)
		base = append(base, sidebar, "")
		base = append(base, renderRightPane(snapshot, contentWidth, max(8, availableRows-strings.Count(sidebar, "\n")-1)))
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
	if snapshot.Mode == modeAccess {
		lines = overlayCentered(lines, renderAccessPanel(snapshot, modalWidth(contentWidth)), contentWidth)
	}
	if snapshot.Notice != "" {
		lines = append(lines, "", okStyle.Render(snapshot.Notice))
	}
	if snapshot.Error != "" {
		lines = append(lines, "", actionStyle.Render(snapshot.Error))
	}
	lines = append(lines, "", helpView)
	// Safety net: if the content still exceeds the screen, clip from the bottom
	// of the panes (the request list scrolls anyway) so the command bar — the
	// last lines — is never pushed off-screen. pageStyle adds a padding row top
	// and bottom, hence height-2.
	if budget := height - 2; len(lines) > budget {
		footerKeep := 2 + strings.Count(helpView, "\n") // blank separator + help bar rows
		footer := lines[len(lines)-footerKeep:]
		lines = append(lines[:max(1, budget-footerKeep)], footer...)
	}
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
		// A 1-char marker sits between the cursor prefix and the name (the name
		// field shrinks by one to keep columns aligned), badging OrbStack routes.
		marker := " "
		if route.Orbstack != nil {
			marker = orbstackBadge
		}
		name := routeName(route.Hostname)
		host := route.Hostname
		line := fmt.Sprintf("%s%s %-12s %s", prefix, marker, name, host)
		// A trailing 🔒 (rendered after the row style, width reserved) marks
		// Access-protected routes without disturbing column alignment.
		avail := width - 4
		suffix := ""
		if route.Access != nil {
			suffix = " " + accessBadge
			avail -= lipgloss.Width(suffix)
		}
		rendered := style.Render(truncate(line, avail))
		if suffix != "" {
			rendered += okStyle.Render(suffix)
		}
		lines = append(lines, rendered)
	}
	return renderBox(fmt.Sprintf("Tunnels (%d)", len(snapshot.Routes)), strings.Join(padRows(lines, minRows), "\n"), width)
}

func renderRightPane(snapshot Snapshot, width int, availableRows int) string {
	route := snapshot.SelectedRoute
	// One bucket per displayed column, 1s each: the chart shows the last N seconds.
	chartWidth := max(10, width-2)
	stats := computeStats(snapshot.Logs, route.CreatedAt, snapshot.Now, chartWidth, time.Second)
	sections := []string{renderHero(route, stats, snapshot.Edge, width)}
	if route.Access != nil {
		sections = append(sections, "", renderAccess(route.Access, width))
	}
	if route.Orbstack != nil {
		sections = append(sections, "", renderOrbStack(route.Orbstack, width))
	}
	sections = append(sections,
		"",
		renderOverview(stats, snapshot.Edge, width),
		"",
		renderTraffic(stats, width),
		"",
		renderEdge(snapshot.Edge, width),
	)
	top := lipgloss.JoinVertical(lipgloss.Left, sections...)
	// The request list takes whatever height remains below the cards (1 blank
	// separator + 2 box borders), so the pane fits exactly in availableRows.
	logRows := availableRows - (strings.Count(top, "\n") + 1) - 3
	if logRows < 3 {
		logRows = 3
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, "", renderLogs(snapshot, width, logRows))
}

func renderHero(route routes.Route, stats Stats, edge cloudflared.EdgeStatus, width int) string {
	if route.Hostname == "" {
		return renderBox("Tunnel", mutedStyle.Render("Select a tunnel, or press n to expose a local port."), width)
	}
	url := commandStyle.Render("https://" + route.Hostname)
	meta := okStyle.Render("● live")
	if stats.Uptime > 0 {
		meta += mutedStyle.Render("  ·  up " + formatUptime(stats.Uptime))
	}
	if n := len(edge.Connections); n > 0 {
		meta += mutedStyle.Render(fmt.Sprintf("  ·  %d edge%s", n, plural(n)))
	}
	if edge.Version != "" {
		meta += mutedStyle.Render("  ·  cloudflared " + edge.Version)
	}
	body := url + mutedStyle.Render("  →  ") + commandStyle.Render(route.Target) + "\n" + meta
	return renderBox(route.Hostname, body, width)
}

// orbstackBadge marks OrbStack-backed routes in the tunnel list; accessBadge
// marks Access-protected routes.
const (
	orbstackBadge = "⬡"
	accessBadge   = "🔒"
)

// renderAccess is a compact card for Access-protected routes: the login method,
// who is allowed, and (for SSO) the identity provider.
func renderAccess(info *routes.AccessInfo, width int) string {
	method := info.Mode
	if info.Mode == "sso" && info.IdP != "" {
		method = "sso · " + info.IdP
	}
	body := commandStyle.Render(accessBadge+" Cloudflare Access") + mutedStyle.Render("  ·  "+method)
	if len(info.Allow) > 0 {
		body += "\n" + mutedStyle.Render("allow  ") + strings.Join(info.Allow, ", ")
	}
	return renderBox("Protected", body, width)
}

// renderOrbStack is a compact detail card shown for OrbStack-backed routes:
// the container, its image, and any custom domains. Kept to two lines so it
// barely costs vertical space in the right pane.
func renderOrbStack(info *routes.OrbstackInfo, width int) string {
	body := commandStyle.Render(orbstackBadge + " " + info.Container)
	if info.Image != "" {
		body += mutedStyle.Render("  ·  " + info.Image)
	}
	if len(info.CustomDomains) > 0 {
		body += "\n" + mutedStyle.Render("domains  ") + strings.Join(info.CustomDomains, ", ")
	}
	return renderBox("OrbStack", body, width)
}

func renderOverview(stats Stats, edge cloudflared.EdgeStatus, width int) string {
	errValue := fmt.Sprintf("%d", stats.Errors)
	if stats.Total > 0 && stats.Errors > 0 {
		errValue = warnStyle.Render(fmt.Sprintf("%d (%.1f%%)", stats.Errors, stats.errorRate()*100))
	}
	line1 := strings.Join([]string{
		mutedStyle.Render("Requests ") + humanCount(stats.Total),
		mutedStyle.Render("Errors ") + errValue,
		mutedStyle.Render("p95 ") + formatLatency(stats.P95),
		mutedStyle.Render("Sent ") + humanBytes(stats.BytesSent),
	}, mutedStyle.Render("   ·   "))
	line2 := mutedStyle.Render("Codes  ") + fmt.Sprintf("2xx %d  3xx %d  4xx %d  5xx %d",
		stats.ClassCounts[2], stats.ClassCounts[3], stats.ClassCounts[4], stats.ClassCounts[5])
	return renderBox("Overview", line1+"\n"+line2, width)
}

func renderTraffic(stats Stats, width int) string {
	title := fmt.Sprintf("Traffic · last %ds · peak %d/s", len(stats.Buckets), stats.PeakBucket)
	bar := sparkline(stats.Buckets)
	style := okStyle
	if stats.Errors > 0 {
		style = warnStyle
	}
	body := style.Render(bar)
	if stats.PeakBucket == 0 {
		body = mutedStyle.Render("no traffic in the last " + fmt.Sprintf("%ds", len(stats.Buckets)))
	}
	return renderBox(title, body, width)
}

func renderEdge(edge cloudflared.EdgeStatus, width int) string {
	title := "Cloudflare edge"
	if len(edge.Connections) == 0 {
		return renderBox(title, mutedStyle.Render("Connecting…  (edge locations appear once cloudflared connects)"), width)
	}
	title = fmt.Sprintf("Cloudflare edge (%d)", len(edge.Connections))
	lines := make([]string, 0, len(edge.Connections))
	for _, c := range edge.Connections {
		city := c.City
		if city == "" {
			city = c.Location
		}
		line := okStyle.Render("●") + fmt.Sprintf(" %-7s %-13s %s", c.Location, city, mutedStyle.Render(c.Protocol))
		lines = append(lines, truncate(line, width-4))
	}
	return renderBox(title, strings.Join(lines, "\n"), width)
}

func renderLogs(snapshot Snapshot, width int, rows int) string {
	route := snapshot.SelectedRoute
	logs := snapshot.Logs
	if len(logs) == 0 {
		return renderBox(logTitle(route), strings.Join(padRows([]string{mutedStyle.Render("Waiting for requests...")}, rows), "\n"), width)
	}
	n := len(logs)
	cursor := clamp(snapshot.LogCursor, 0, n-1)
	// Scroll the window to keep the cursor visible, preferring it at the bottom
	// (chronological: oldest at top, newest — the live tail — at the bottom).
	start := 0
	if n > rows {
		start = clamp(cursor-rows+1, 0, n-rows)
	}
	end := min(n, start+rows)
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		entry := logs[i]
		prefix := " "
		style := statusColor(entry.Status)
		if snapshot.Focus == focusLogs && i == cursor {
			prefix = ">"
			style = selectedRowStyle
		}
		line := fmt.Sprintf("%-5s %3d %-36s %8s", entry.Method, entry.Status, entry.Path, entry.Duration.Round(time.Millisecond))
		lines = append(lines, style.Render(truncate(prefix+" "+line, width-4)))
	}
	title := logTitle(route)
	if n > rows {
		title += fmt.Sprintf(" %d-%d/%d", start+1, end, n)
	}
	return renderBox(title, strings.Join(padRows(lines, rows), "\n"), width)
}

// CreateRow is one rendered row of the create modal, derived by the model from
// the active field list.
type CreateRow struct {
	Label    string
	Value    string
	Active   bool
	Selector bool // a ◂/▸ chooser (source, container, protection) rather than a text input
}

func renderCreateForm(snapshot Snapshot, width int) string {
	rows := make([]string, 0, len(snapshot.CreateRows)+3)
	for _, row := range snapshot.CreateRows {
		value := row.Value
		if row.Selector {
			if row.Active {
				value = commandStyle.Render("◂ " + row.Value + " ▸")
			} else {
				value = mutedStyle.Render(row.Value)
			}
		}
		rows = append(rows, createRow(row.Label, value, row.Active))
	}
	rows = append(rows, "", mutedStyle.Render("enter next/create   tab move   ◂/▸ choose   esc cancel"))
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
	lines := []string{
		fmt.Sprintf("Time:      %s", entry.Time.Format(time.RFC3339)),
		fmt.Sprintf("Request:   %s %s", entry.Method, entry.Path),
		fmt.Sprintf("Status:    %d  ·  %s  ·  %d bytes", entry.Status, entry.Duration.Round(time.Millisecond), entry.Bytes),
		fmt.Sprintf("Target:    %s", entry.Target),
		fmt.Sprintf("Remote:    %s", entry.RemoteAddr),
	}

	switch {
	case snapshot.DetailErr != "":
		lines = append(lines, "", warnStyle.Render(snapshot.DetailErr))
	case snapshot.Detail == nil:
		lines = append(lines, "", mutedStyle.Render("Loading headers and body…"))
	default:
		ex := snapshot.Detail
		bodyWidth := max(10, width-4)
		lines = append(lines, "", brandStyle.Render("Request"))
		lines = append(lines, renderHeaderBlock(ex.RequestHeaders, bodyWidth)...)
		lines = append(lines, renderBodyBlock(ex.RequestBody, ex.RequestTruncated, bodyWidth)...)
		lines = append(lines, "", brandStyle.Render("Response"))
		lines = append(lines, renderHeaderBlock(ex.ResponseHeaders, bodyWidth)...)
		lines = append(lines, renderBodyBlock(ex.ResponseBody, ex.ResponseTruncated, bodyWidth)...)
	}

	lines = append(lines, "", mutedStyle.Render("r replay  ·  enter/esc close"))
	return renderBox("Request detail", strings.Join(lines, "\n"), width)
}

// renderHeaderBlock renders up to a handful of headers, sorted, truncated to width.
func renderHeaderBlock(header http.Header, width int) []string {
	if len(header) == 0 {
		return []string{mutedStyle.Render("  (no headers)")}
	}
	keys := make([]string, 0, len(header))
	for key := range header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	const maxHeaders = 12
	lines := make([]string, 0, len(keys))
	for i, key := range keys {
		if i == maxHeaders {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("  … %d more", len(keys)-maxHeaders)))
			break
		}
		line := "  " + mutedStyle.Render(key+": ") + strings.Join(header[key], ", ")
		lines = append(lines, truncate(line, width))
	}
	return lines
}

// renderBodyBlock shows a capped preview of a captured body.
func renderBodyBlock(body []byte, truncated bool, width int) []string {
	if len(body) == 0 {
		return []string{mutedStyle.Render("  (empty body)")}
	}
	const maxLines = 12
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	rows := strings.Split(text, "\n")
	lines := make([]string, 0, maxLines+1)
	for i, row := range rows {
		if i == maxLines {
			lines = append(lines, mutedStyle.Render("  … body truncated for display"))
			return lines
		}
		lines = append(lines, truncate("  "+row, width))
	}
	if truncated {
		lines = append(lines, mutedStyle.Render("  … body truncated at capture limit"))
	}
	return lines
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
		return "Requests"
	}
	return "Requests: " + routeName(route.Hostname)
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
	index := clamp(snapshot.LogCursor, 0, len(snapshot.Logs)-1)
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

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGT"[exp])
}

func formatLatency(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Round(time.Millisecond)/time.Millisecond)
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		if m := int(d.Minutes()) % 60; m != 0 {
			return fmt.Sprintf("%dh%dm", int(d.Hours()), m)
		}
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		if h := int(d.Hours()) % 24; h != 0 {
			return fmt.Sprintf("%dd%dh", int(d.Hours())/24, h)
		}
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
