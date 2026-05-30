package sshui

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m Model) View() tea.View {
	if m.quit {
		return tea.NewView("")
	}
	return tea.View{AltScreen: true, Content: m.render()}
}

func (m Model) render() string {
	width := m.width
	if width < 84 {
		width = 84
	}
	content := width - 2
	if content > 110 {
		content = 110
	}

	header := brandStyle.Render("vtunnel") + mutedStyle.Render(" ssh")
	right := mutedStyle.Render(fmt.Sprintf("browser SSH %d", len(m.endpoints)))
	gap := max(1, content-lipgloss.Width(header)-lipgloss.Width(right))
	lines := []string{header + strings.Repeat(" ", gap) + right, ""}

	leftWidth := 36
	rightWidth := content - leftWidth - 2
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.renderList(leftWidth), "  ", m.renderDetail(rightWidth))
	lines = append(lines, body)

	out := strings.Split(strings.Join(lines, "\n"), "\n")
	if m.mode == modeCreate {
		out = overlay(out, m.renderCreate(min(64, content-4)), content)
	}
	if m.mode == modeConfirmRemove {
		out = overlay(out, m.renderConfirm(min(64, content-4)), content)
	}
	if m.notice != "" {
		out = append(out, "", okStyle.Render(m.notice))
	}
	if m.err != "" {
		out = append(out, "", errStyle.Render(m.err))
	}
	out = append(out, "", mutedStyle.Render("n new · x remove · c copy URL · r refresh · q quit"))
	return pageStyle.Render(strings.Join(out, "\n"))
}

func (m Model) renderList(width int) string {
	var rows []string
	if len(m.endpoints) == 0 {
		rows = append(rows, mutedStyle.Render("No browser SSH"), mutedStyle.Render("Press n to create one"))
	}
	for i, endpoint := range m.endpoints {
		prefix := "  "
		style := rowStyle
		if i == m.selected {
			prefix = selectedStyle.Render("› ")
			style = selectedStyle
		}
		rows = append(rows, prefix+style.Render(truncate(endpoint.Hostname, width-6)))
	}
	return renderBox(fmt.Sprintf("Browser SSH (%d)", len(m.endpoints)), strings.Join(padRows(rows, 8), "\n"), width)
}

func (m Model) renderDetail(width int) string {
	endpoint, ok := m.current()
	if !ok {
		return renderBox("Detail", mutedStyle.Render("Select an endpoint, or press n to expose SSH in the browser."), width)
	}
	rows := []string{
		brandStyle.Render(endpoint.Hostname),
		mutedStyle.Render("open ") + commandStyle.Render(endpoint.URL),
		"",
		mutedStyle.Render("→ ssh://" + endpoint.Target),
		"",
		mutedStyle.Render("Open in any browser — no client, no cloudflared."),
		mutedStyle.Render("Sign in via Cloudflare Access; the email prefix"),
		mutedStyle.Render("must match the SSH username on the server."),
	}
	return renderBox("Detail", strings.Join(padRows(rows, 8), "\n"), width)
}

func (m Model) renderCreate(width int) string {
	rows := []string{
		createRow("Subdomain", m.inputs[fSub].View(), m.createStep == fSub),
		createRow("SSH target", m.inputs[fTarget].View(), m.createStep == fTarget),
		createRow("Allow", m.inputs[fAllow].View(), m.createStep == fAllow),
		createRow("IdP", m.inputs[fIdP].View(), m.createStep == fIdP),
		"",
		mutedStyle.Render("Allow: an email (you@you.com) or @domain — required."),
		mutedStyle.Render("IdP: leave blank for email one-time PIN, or an SSO provider name."),
		mutedStyle.Render("enter next/create · tab move · esc cancel"),
	}
	if m.busy {
		rows = append(rows, "", okStyle.Render("working… (provisioning Access + restarting cloudflared)"))
	}
	return renderBox("New browser SSH", strings.Join(rows, "\n"), width)
}

func (m Model) renderConfirm(width int) string {
	endpoint, _ := m.current()
	body := strings.Join([]string{
		fmt.Sprintf("Remove browser SSH %s?", endpoint.Hostname),
		mutedStyle.Render("Deletes its Cloudflare Access app and restarts cloudflared."),
		"",
		mutedStyle.Render("y remove · n cancel"),
	}, "\n")
	if m.busy {
		body += "\n" + okStyle.Render("working…")
	}
	return renderBox("Confirm", body, width)
}

func createRow(label, input string, active bool) string {
	marker := "  "
	labelStyle := mutedStyle
	if active {
		marker = selectedStyle.Render("› ")
		labelStyle = brandStyle
	}
	return marker + labelStyle.Render(fmt.Sprintf("%-11s", label)) + " " + input
}

func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	default:
		switch {
		case lookPath("xclip"):
			cmd = exec.Command("xclip", "-selection", "clipboard")
		case lookPath("xsel"):
			cmd = exec.Command("xsel", "--clipboard", "--input")
		default:
			return errors.New("no clipboard tool found")
		}
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// --- shared rendering helpers (mirrors the tcp/http look) ---

func renderBox(title, body string, width int) string {
	if width < 12 {
		width = 12
	}
	inner := width - 2
	title = " " + title + " "
	fill := max(0, inner-lipgloss.Width(title))
	lines := []string{borderStyle.Render("┌" + title + strings.Repeat("─", fill) + "┐")}
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, borderStyle.Render("│")+padRight(truncate(line, inner), inner)+borderStyle.Render("│"))
	}
	lines = append(lines, borderStyle.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(lines, "\n")
}

func overlay(base []string, panel string, width int) []string {
	out := append([]string{}, base...)
	out = append(out, "")
	for _, line := range strings.Split(panel, "\n") {
		pad := max(0, (width-lipgloss.Width(line))/2)
		out = append(out, strings.Repeat(" ", pad)+line)
	}
	return out
}

func padRows(rows []string, minRows int) []string {
	for len(rows) < minRows {
		rows = append(rows, "")
	}
	return rows
}

func padRight(s string, width int) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func truncate(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 3 {
		return s[:max(0, width)]
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+3 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var (
	pageStyle     = lipgloss.NewStyle().Padding(1, 1)
	brandStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	borderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("66"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	rowStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("151"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	commandStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)
