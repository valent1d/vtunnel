package mcpsetupui

import (
	"fmt"
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
	if width < 88 {
		width = 88
	}
	content := width - 2
	if content > 116 {
		content = 116
	}

	header := brandStyle.Render("vtunnel") + mutedStyle.Render(" mcp")
	right := mutedStyle.Render(fmt.Sprintf("scope %s · %d/%d configured", m.manager.Scope(), m.configuredCount(), len(m.clients)))
	gap := max(1, content-lipgloss.Width(header)-lipgloss.Width(right))
	lines := []string{header + strings.Repeat(" ", gap) + right, ""}

	leftWidth := 30
	rightWidth := content - leftWidth - 2
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.renderList(leftWidth), "  ", m.renderDetail(rightWidth))
	out := append(lines, body)

	if m.check != "" {
		out = append(out, "", okStyle.Render(m.check))
	}
	if m.notice != "" {
		out = append(out, "", okStyle.Render(m.notice))
	}
	if m.err != "" {
		out = append(out, "", errStyle.Render(m.err))
	}
	out = append(out, "", mutedStyle.Render("a add · x remove · c self-check · r refresh · q quit"))
	out = append(out, mutedStyle.Render("✓ configured · ○ installed · · not detected"))
	return pageStyle.Render(strings.Join(out, "\n"))
}

func (m Model) configuredCount() int {
	n := 0
	for _, c := range m.clients {
		if c.Configured {
			n++
		}
	}
	return n
}

func (m Model) renderList(width int) string {
	var rows []string
	if len(m.clients) == 0 {
		rows = append(rows, mutedStyle.Render("Loading…"))
	}
	for i, client := range m.clients {
		prefix := "  "
		style := rowStyle
		if i == m.selected {
			prefix = selectedStyle.Render("› ")
			style = selectedStyle
		}
		rows = append(rows, prefix+badge(client)+" "+style.Render(truncate(client.Name, width-8)))
	}
	return renderBox("Clients", strings.Join(padRows(rows, 8), "\n"), width)
}

func (m Model) renderDetail(width int) string {
	client, ok := m.current()
	if !ok {
		return renderBox("Detail", mutedStyle.Render("No clients."), width)
	}
	state := errStyle.Render("not detected")
	if client.Configured {
		state = okStyle.Render("configured")
	} else if client.Installed {
		state = commandStyle.Render("installed, not configured")
	}
	rows := []string{
		brandStyle.Render(client.Name) + "  " + state,
	}
	if client.Location != "" {
		rows = append(rows, mutedStyle.Render(client.Location))
	}
	if client.Note != "" {
		rows = append(rows, mutedStyle.Render(client.Note))
	}
	rows = append(rows, "", mutedStyle.Render("Add with: a   ·   Remove with: x"))
	rows = append(rows, "", mutedStyle.Render("Manual setup:"))
	if client.ManualCommand != "" {
		rows = append(rows, commandStyle.Render("  "+client.ManualCommand))
	} else if client.ManualSnippet != "" {
		if client.ManualPath != "" {
			rows = append(rows, mutedStyle.Render("  edit "+client.ManualPath))
		}
		for _, line := range strings.Split(client.ManualSnippet, "\n") {
			rows = append(rows, commandStyle.Render("  "+line))
		}
	}
	return renderBox("Detail", strings.Join(padRows(rows, 8), "\n"), width)
}

func badge(client Client) string {
	switch {
	case client.Configured:
		return okStyle.Render("✓")
	case client.Installed:
		return commandStyle.Render("○")
	default:
		return mutedStyle.Render("·")
	}
}

// --- shared rendering helpers (mirrors the tcp/ssh look) ---

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
