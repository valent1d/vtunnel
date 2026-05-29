package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vtunnel/internal/onboarding"
)

type engine interface {
	Report(context.Context) onboarding.Report
	Execute(context.Context, string, string) (string, error)
}

type Model struct {
	engine engine
	report onboarding.Report
	width  int
	height int

	phaseIndex    int
	actionIndex   int
	confirming    bool
	inputAction   *onboarding.Action
	input         textinput.Model
	notice        string
	err           string
	quitting      bool
	actionRunning bool
}

type reportMsg onboarding.Report

type actionMsg struct {
	notice string
	err    string
}

func Run(ctx context.Context, engine *onboarding.Engine) error {
	program := tea.NewProgram(
		NewModel(engine),
		tea.WithAltScreen(),
		tea.WithContext(ctx),
	)
	_, err := program.Run()
	return err
}

func NewModel(engine engine) Model {
	input := textinput.New()
	input.CharLimit = 256
	input.Width = 56
	return Model{
		engine: engine,
		width:  96,
		height: 28,
		input:  input,
	}
}

func (m Model) Init() tea.Cmd {
	return m.fetch()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.inputAction != nil {
			return m.updateInput(msg)
		}
		if m.confirming {
			return m.updateConfirm(msg)
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "right", "l", "tab":
			m.phaseIndex = clamp(m.phaseIndex+1, 0, len(m.report.Phases)-1)
			return m, nil
		case "left", "h", "shift+tab":
			m.phaseIndex = clamp(m.phaseIndex-1, 0, len(m.report.Phases)-1)
			return m, nil
		case "down", "j":
			m.actionIndex = clamp(m.actionIndex+1, 0, len(m.report.Actions)-1)
			return m, nil
		case "up", "k":
			m.actionIndex = clamp(m.actionIndex-1, 0, len(m.report.Actions)-1)
			return m, nil
		case "enter":
			action, ok := selectedAction(m.report.Actions, m.actionIndex)
			if !ok {
				return m, nil
			}
			if !action.Mutates {
				m.notice = action.Command
				m.err = ""
				if action.ID == "open-token-url" {
					m.confirming = true
				}
				return m, nil
			}
			if action.InputPrompt != "" {
				m.inputAction = &action
				m.input.Placeholder = action.InputPrompt
				m.input.SetValue("")
				m.input.Focus()
				return m, nil
			}
			m.confirming = true
			return m, nil
		case "r":
			return m, m.fetch()
		}
	case reportMsg:
		m.report = onboarding.Report(msg)
		m.phaseIndex = clamp(m.phaseIndex, 0, len(m.report.Phases)-1)
		m.actionIndex = clamp(m.actionIndex, 0, len(m.report.Actions)-1)
		m.actionRunning = false
		return m, nil
	case actionMsg:
		m.actionRunning = false
		m.confirming = false
		m.inputAction = nil
		if msg.err != "" {
			m.err = msg.err
			return m, nil
		}
		m.notice = msg.notice
		m.err = ""
		return m, m.fetch()
	}
	return m, nil
}

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n":
		m.confirming = false
		return m, nil
	case "y", "enter":
		action, ok := selectedAction(m.report.Actions, m.actionIndex)
		if !ok {
			m.confirming = false
			return m, nil
		}
		m.actionRunning = true
		return m, m.execute(action, "")
	}
	return m, nil
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.inputAction = nil
		return m, nil
	case "enter":
		action := *m.inputAction
		value := m.input.Value()
		if strings.TrimSpace(value) == "" {
			m.err = action.InputPrompt + " is required"
			return m, nil
		}
		m.actionRunning = true
		return m, m.execute(action, value)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	return Render(Snapshot{
		Report:        m.report,
		PhaseIndex:    m.phaseIndex,
		ActionIndex:   m.actionIndex,
		Confirming:    m.confirming,
		InputAction:   m.inputAction,
		InputValue:    m.input.View(),
		Notice:        m.notice,
		Error:         m.err,
		ActionRunning: m.actionRunning,
		Width:         m.width,
		Height:        m.height,
	})
}

func (m Model) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return reportMsg(m.engine.Report(ctx))
	}
}

func (m Model) execute(action onboarding.Action, input string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		notice, err := m.engine.Execute(ctx, action.ID, input)
		if err != nil {
			return actionMsg{err: err.Error()}
		}
		return actionMsg{notice: notice}
	}
}

type Snapshot struct {
	Report        onboarding.Report
	PhaseIndex    int
	ActionIndex   int
	Confirming    bool
	InputAction   *onboarding.Action
	InputValue    string
	Notice        string
	Error         string
	ActionRunning bool
	Width         int
	Height        int
}

func Render(snapshot Snapshot) string {
	width := snapshot.Width
	if width < 72 {
		width = 72
	}
	contentWidth := width - 4
	if contentWidth > 110 {
		contentWidth = 110
	}

	status := warnStyle.Render("needs setup")
	if snapshot.Report.Ready {
		status = okStyle.Render("ready")
	}

	var lines []string
	lines = append(lines, renderHeaderStatus(snapshot.Report, status, contentWidth))
	lines = append(lines, "")
	lines = append(lines, renderOverview(snapshot.Report, snapshot.PhaseIndex, contentWidth))
	if snapshot.Report.Ready {
		lines = append(lines, "", renderReady(contentWidth))
	} else {
		lines = append(lines, "", renderStepper(snapshot.Report.Phases, snapshot.PhaseIndex, contentWidth))
		lines = append(lines, "")
		lines = append(lines, renderWorkspace(snapshot, contentWidth))
	}
	if snapshot.Confirming {
		lines = append(lines, "", renderConfirm(snapshot, contentWidth))
	}
	if snapshot.InputAction != nil {
		lines = append(lines, "", renderInput(snapshot, contentWidth))
	}
	if snapshot.ActionRunning {
		lines = append(lines, "", pulseStyle.Render("working..."))
	}
	if snapshot.Notice != "" {
		lines = append(lines, "", renderMessage("Done", snapshot.Notice, okStyle, contentWidth))
	}
	if snapshot.Error != "" {
		lines = append(lines, "", renderMessage("Blocked", snapshot.Error, actionStyle, contentWidth))
	}
	lines = append(lines, "", footerStyle.Width(contentWidth).Render("tab/shift+tab phase   up/down action   enter select   r recheck   q quit"))
	return pageStyle.Render(strings.Join(lines, "\n"))
}

func renderHeaderStatus(report onboarding.Report, status string, width int) string {
	left := brandStyle.Render("vtunnel") + mutedStyle.Render(" onboarding")
	updated := ""
	if !report.Updated.IsZero() {
		updated = mutedStyle.Render("updated " + report.Updated.Format("15:04:05"))
	}
	right := strings.TrimSpace(strings.Join([]string{updated, status}, "  "))
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return lipgloss.JoinHorizontal(lipgloss.Center, left, strings.Repeat(" ", gap), right)
}

func renderOverview(report onboarding.Report, selected int, width int) string {
	if len(report.Phases) == 0 {
		return mutedStyle.Render("Status  loading")
	}
	ok, warn, action := phaseCounts(report.Phases)
	current := report.Phases[clamp(selected, 0, len(report.Phases)-1)].Title
	line := mutedStyle.Render("Status  ") +
		okStyle.Render(fmt.Sprintf("ready %d", ok)) + mutedStyle.Render("   ") +
		warnStyle.Render(fmt.Sprintf("warn %d", warn)) + mutedStyle.Render("   ") +
		actionStyle.Render(fmt.Sprintf("action %d", action)) + mutedStyle.Render("   ") +
		mutedStyle.Render("current "+current)
	return truncate(line, width)
}

func renderReady(width int) string {
	body := strings.Join([]string{
		okStyle.Render("vtunnel is ready."),
		"",
		headingStyle.Render("Try:"),
		commandStyle.Render("  vtunnel http 3000 dev"),
		commandStyle.Render("  vtunnel"),
	}, "\n")
	return renderBlock("Ready", body, width)
}

func renderWorkspace(snapshot Snapshot, width int) string {
	return lipgloss.JoinVertical(
		lipgloss.Left,
		renderPhase(snapshot.Report, snapshot.PhaseIndex, width),
		"",
		renderActions(snapshot.Report.Actions, snapshot.ActionIndex, width),
	)
}

func renderStepper(phases []onboarding.Phase, selected int, width int) string {
	if len(phases) == 0 {
		return mutedStyle.Render("Loading setup map...")
	}
	var lines []string
	titleWidth := 24
	if width < 78 {
		titleWidth = 18
	}
	for index, phase := range phases {
		prefix := " "
		if index == selected {
			prefix = ">"
		}
		title := truncate(shortPhaseTitle(phase.Title), titleWidth)
		line := fmt.Sprintf("%s %d  %-*s %s", prefix, index+1, titleWidth, title, phaseStatusLabel(phase))
		if index == selected {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, truncate(line, width-2))
	}
	return renderBlock("Setup", strings.Join(lines, "\n"), width)
}

func renderPhase(report onboarding.Report, selected int, width int) string {
	if len(report.Phases) == 0 {
		return renderBlock("Current step", mutedStyle.Render("Loading checks..."), width)
	}
	phase := report.Phases[clamp(selected, 0, len(report.Phases)-1)]
	var lines []string
	lines = append(lines, headingStyle.Render("Current step: "+phase.Title))
	if phase.Summary != "" {
		lines = append(lines, mutedStyle.Render(wrapLine(phase.Summary, width-2)))
	}
	lines = append(lines, "")
	if len(phase.Checks) == 0 {
		lines = append(lines, mutedStyle.Render("No checks for this phase."))
	}
	for _, check := range phase.Checks {
		lines = append(lines, renderCheck(check, width-2))
	}
	return renderBlock("Details", strings.Join(lines, "\n"), width)
}

func renderActions(actions []onboarding.Action, selected int, width int) string {
	var lines []string
	if len(actions) == 0 {
		lines = append(lines, okStyle.Render("No action needed."))
		return renderBlock("Actions", strings.Join(lines, "\n"), width)
	}
	for index, action := range actions {
		if index > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, renderAction(action, index == selected, width-2))
	}
	return renderBlock("Actions", strings.Join(lines, "\n"), width)
}

func renderCheck(check onboarding.Check, width int) string {
	marker := statusMarker(check.Status)
	labelWidth := 22
	if width < 56 {
		labelWidth = 16
	}
	label := truncate(check.Label, labelWidth)
	detailWidth := max(12, width-lipgloss.Width(marker)-labelWidth-4)
	detail := truncate(check.Detail, detailWidth)
	return fmt.Sprintf("%s %-*s %s", marker, labelWidth, label, mutedStyle.Render(detail))
}

func renderAction(action onboarding.Action, selected bool, width int) string {
	marker := "manual"
	markerStyle := mutedBadgeStyle
	if action.Mutates {
		marker = "apply"
		markerStyle = warnBadgeStyle
	}
	if action.InputPrompt != "" {
		marker = "input"
		markerStyle = inputBadgeStyle
	}
	if action.Command != "" && !action.Mutates {
		marker = "manual"
		markerStyle = mutedBadgeStyle
	}

	title := truncate(action.Label, max(10, width-18))
	if selected {
		title = selectedStyle.Render("> " + title)
	} else {
		title = "  " + title
	}
	desc := action.Description
	if action.Command != "" {
		desc = action.Command
	}
	gap := max(1, width-lipgloss.Width(title)-lipgloss.Width(marker)-1)
	lines := []string{
		title + strings.Repeat(" ", gap) + markerStyle.Render(marker),
		"    " + mutedStyle.Render(truncate(desc, max(10, width-4))),
	}
	if selected {
		return selectedActionStyle.Width(width).Render(strings.Join(lines, "\n"))
	}
	return strings.Join(lines, "\n")
}

func renderConfirm(snapshot Snapshot, width int) string {
	action, ok := selectedAction(snapshot.Report.Actions, snapshot.ActionIndex)
	title := "Confirm action"
	detail := "y/enter confirm   n/esc cancel"
	if ok {
		title = "Confirm: " + action.Label
		if action.Command != "" && !action.Mutates {
			detail = action.Command + "\n" + detail
		}
	}
	return renderBlock("Confirmation", warnStyle.Render(title)+"\n"+mutedStyle.Render(detail), width)
}

func renderInput(snapshot Snapshot, width int) string {
	title := "Input"
	if snapshot.InputAction != nil {
		title = snapshot.InputAction.InputPrompt
	}
	return renderBlock("Input", warnStyle.Render(title)+"\n"+snapshot.InputValue, width)
}

func renderMessage(title string, message string, style lipgloss.Style, width int) string {
	message = strings.ReplaceAll(strings.TrimSpace(message), "\n  ", "\n")
	body := style.Render(title) + "\n" + mutedStyle.Render(message)
	return renderBlock(title, body, width)
}

func phaseStatusGlyph(phase onboarding.Phase) string {
	status := phaseStatus(phase)
	switch status {
	case onboarding.StatusOK:
		return "ok"
	case onboarding.StatusWarn:
		return "warn"
	default:
		return "fix"
	}
}

func phaseStatusLabel(phase onboarding.Phase) string {
	switch phaseStatus(phase) {
	case onboarding.StatusOK:
		return okStyle.Render("done")
	case onboarding.StatusWarn:
		return warnStyle.Render("warning")
	default:
		return actionStyle.Render("action needed")
	}
}

func phaseCounts(phases []onboarding.Phase) (int, int, int) {
	var ok int
	var warn int
	var action int
	for _, phase := range phases {
		switch phaseStatus(phase) {
		case onboarding.StatusOK:
			ok++
		case onboarding.StatusWarn:
			warn++
		default:
			action++
		}
	}
	return ok, warn, action
}

func renderBlock(title string, body string, width int) string {
	innerWidth := max(20, width-2)
	var lines []string
	lines = append(lines, sectionKickerStyle.Render(title))
	lines = append(lines, rule(innerWidth))
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, truncate(line, innerWidth))
	}
	return strings.Join(lines, "\n")
}

func rule(width int) string {
	if width < 1 {
		return ""
	}
	return ruleStyle.Render(strings.Repeat("-", width))
}

func phaseStatus(phase onboarding.Phase) onboarding.Status {
	status := onboarding.StatusOK
	for _, check := range phase.Checks {
		if check.Status == onboarding.StatusAction {
			return onboarding.StatusAction
		}
		if check.Status == onboarding.StatusWarn {
			status = onboarding.StatusWarn
		}
	}
	return status
}

func shortPhaseTitle(title string) string {
	switch title {
	case "Local checks":
		return "Local"
	case "Cloudflare auth":
		return "Cloudflare"
	case "Domain selection":
		return "Domain"
	case "Local cloudflared config":
		return "Config"
	default:
		return title
	}
}

func statusMarker(status onboarding.Status) string {
	switch status {
	case onboarding.StatusOK:
		return okBadgeStyle.Render("OK")
	case onboarding.StatusWarn:
		return warnBadgeStyle.Render("WARN")
	default:
		return actionBadgeStyle.Render("FIX")
	}
}

func selectedAction(actions []onboarding.Action, selected int) (onboarding.Action, bool) {
	if len(actions) == 0 {
		return onboarding.Action{}, false
	}
	selected = clamp(selected, 0, len(actions)-1)
	return actions[selected], true
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

func wrapLine(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	return truncate(value, width)
}

func max(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

var (
	pageStyle = lipgloss.NewStyle().
			Padding(1, 2)
	selectedActionStyle = lipgloss.NewStyle().
				PaddingLeft(1)

	brandStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	headingStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	sectionKickerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	ruleStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	mutedStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	footerStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	actionStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	pulseStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("48"))
	commandStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	selectedStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))

	stepStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	activeStepStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))

	okBadgeStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	warnBadgeStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	actionBadgeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	inputBadgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	mutedBadgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
)
