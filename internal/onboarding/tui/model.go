package tui

import (
	"context"
	"fmt"
	"os"
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

	bootDone      bool
	bootFrame     int
	stepIndex     int
	actionIndex   int
	confirming    bool
	inputAction   *onboarding.Action
	inputPrefix   string
	input         textinput.Model
	notice        string
	err           string
	quitting      bool
	finished      bool
	actionRunning bool

	domainManager       bool
	domainIndex         int
	domainConfirmAction string
	domainConfirmValue  string

	tunnelManager       bool
	tunnelIndex         int
	tunnelConfirmAction string
	tunnelConfirmValue  string
}

type reportMsg onboarding.Report
type bootTickMsg time.Time
type bootDoneMsg struct{}

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
	final, err := program.Run()
	if err != nil {
		return err
	}
	if model, ok := final.(Model); ok && model.finished {
		fmt.Fprint(os.Stdout, model.farewell())
	}
	return nil
}

// farewell is printed to the normal terminal once the alt-screen TUI closes via
// "Finish" on the last step: a warm sign-off plus a light status recap.
func (m Model) farewell() string {
	ok, warn, action := stepCounts(stepsForReport(m.report))

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(brandStyle.Render("vtunnel") + mutedStyle.Render(" onboarding complete") + "\n\n")
	if action == 0 {
		b.WriteString(okStyle.Render("Excited to see you soon!") + " " + mutedStyle.Render("👋") + "\n\n")
	} else {
		b.WriteString(warnStyle.Render("A few items still need a fix — see you soon!") + " " + mutedStyle.Render("👋") + "\n\n")
	}
	b.WriteString(mutedStyle.Render("Recap   ") +
		okStyle.Render(fmt.Sprintf("ok %d", ok)) + mutedStyle.Render("   ") +
		warnStyle.Render(fmt.Sprintf("warning %d", warn)) + mutedStyle.Render("   ") +
		actionStyle.Render(fmt.Sprintf("to fix %d", action)) + "\n\n")
	if action == 0 {
		b.WriteString(mutedStyle.Render("Get started   ") + commandStyle.Render("vtunnel http 3000 dev") + "\n")
	} else {
		b.WriteString(mutedStyle.Render("Resume setup  ") + commandStyle.Render("vtunnel onboarding") + "\n")
	}
	return b.String()
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
	return tea.Batch(m.fetch(), bootTick(), bootDone())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case bootTickMsg:
		if m.bootDone {
			return m, nil
		}
		m.bootFrame++
		return m, bootTick()
	case bootDoneMsg:
		m.bootDone = true
		return m, nil
	case tea.KeyMsg:
		if m.inputAction != nil {
			return m.updateInput(msg)
		}
		if m.domainConfirmAction != "" {
			return m.updateDomainConfirm(msg)
		}
		if m.tunnelConfirmAction != "" {
			return m.updateTunnelConfirm(msg)
		}
		if m.domainManager {
			return m.updateDomainManager(msg)
		}
		if m.tunnelManager {
			return m.updateTunnelManager(msg)
		}
		if m.confirming {
			return m.updateConfirm(msg)
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "right", "l", "tab", "n":
			m.stepIndex = clamp(m.stepIndex+1, 0, len(stepsForReport(m.report))-1)
			m.actionIndex = 0
			return m, nil
		case "left", "h", "shift+tab", "p":
			m.stepIndex = clamp(m.stepIndex-1, 0, len(stepsForReport(m.report))-1)
			m.actionIndex = 0
			return m, nil
		case "down", "j":
			m.actionIndex = clamp(m.actionIndex+1, 0, len(currentActions(m.report, m.stepIndex))-1)
			return m, nil
		case "up", "k":
			m.actionIndex = clamp(m.actionIndex-1, 0, len(currentActions(m.report, m.stepIndex))-1)
			return m, nil
		case "enter":
			actions := currentActions(m.report, m.stepIndex)
			action, ok := selectedAction(actions, m.actionIndex)
			if !ok {
				steps := stepsForReport(m.report)
				if m.stepIndex >= len(steps)-1 {
					// Last step has no actions and no "Continue": this is "Finish".
					// Close the TUI; Run prints the farewell + recap to the terminal.
					m.finished = true
					m.quitting = true
					return m, tea.Quit
				}
				m.stepIndex = clamp(m.stepIndex+1, 0, len(steps)-1)
				m.actionIndex = 0
				return m, nil
			}
			if isContinueAction(action) {
				m.stepIndex = clamp(m.stepIndex+1, 0, len(stepsForReport(m.report))-1)
				m.actionIndex = 0
				m.notice = ""
				m.err = ""
				return m, nil
			}
			if action.ID == "manage-domains" {
				m.domainManager = true
				m.domainIndex = clamp(m.domainIndex, 0, len(domainState(m.report).domains)-1)
				m.notice = ""
				m.err = ""
				return m, nil
			}
			if action.ID == "manage-tunnels" {
				m.tunnelManager = true
				m.tunnelIndex = clamp(m.tunnelIndex, 0, len(tunnelState(m.report).tunnels)-1)
				m.notice = ""
				m.err = ""
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
		m.stepIndex = clamp(m.stepIndex, 0, len(stepsForReport(m.report))-1)
		m.actionIndex = clamp(m.actionIndex, 0, len(currentActions(m.report, m.stepIndex))-1)
		m.actionRunning = false
		return m, nil
	case actionMsg:
		m.actionRunning = false
		m.confirming = false
		m.inputAction = nil
		m.inputPrefix = ""
		m.domainConfirmAction = ""
		m.domainConfirmValue = ""
		m.tunnelConfirmAction = ""
		m.tunnelConfirmValue = ""
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
		action, ok := selectedAction(currentActions(m.report, m.stepIndex), m.actionIndex)
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
		m.inputPrefix = ""
		return m, nil
	case "enter":
		action := *m.inputAction
		value := m.input.Value()
		if strings.TrimSpace(value) == "" {
			m.err = action.InputPrompt + " is required"
			return m, nil
		}
		if m.inputPrefix != "" {
			value = m.inputPrefix + "=" + value
		}
		m.actionRunning = true
		return m, m.execute(action, value)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) updateDomainManager(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := domainState(m.report)
	m.domainIndex = clamp(m.domainIndex, 0, len(state.domains)-1)
	switch msg.String() {
	case "esc":
		m.domainManager = false
		return m, nil
	case "down", "j":
		m.domainIndex = clamp(m.domainIndex+1, 0, len(state.domains)-1)
		return m, nil
	case "up", "k":
		m.domainIndex = clamp(m.domainIndex-1, 0, len(state.domains)-1)
		return m, nil
	case "a", "n":
		action := onboarding.Action{ID: "add-domain", Label: "Add a domain", Mutates: true, InputPrompt: "Domain"}
		m.inputAction = &action
		m.inputPrefix = ""
		m.input.Placeholder = action.InputPrompt
		m.input.SetValue("")
		m.input.Focus()
		return m, nil
	case "r", "e":
		if len(state.domains) == 0 {
			return m, nil
		}
		domain := state.domains[m.domainIndex]
		action := onboarding.Action{ID: "rename-domain", Label: "Rename domain", Mutates: true, InputPrompt: "New domain"}
		m.inputAction = &action
		m.inputPrefix = domain
		m.input.Placeholder = action.InputPrompt
		m.input.SetValue(domain)
		m.input.Focus()
		return m, nil
	case "enter", "d":
		if len(state.domains) == 0 {
			return m, nil
		}
		domain := state.domains[m.domainIndex]
		if domain == state.defaultDomain {
			m.notice = "Default domain already set: " + domain
			m.err = ""
			return m, nil
		}
		m.actionRunning = true
		return m, m.execute(onboarding.Action{ID: "set-default-domain", Label: "Set default domain"}, domain)
	case "x", "delete", "backspace":
		if len(state.domains) == 0 {
			return m, nil
		}
		m.domainConfirmAction = "remove-domain"
		m.domainConfirmValue = state.domains[m.domainIndex]
		return m, nil
	}
	return m, nil
}

func (m Model) updateDomainConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n":
		m.domainConfirmAction = ""
		m.domainConfirmValue = ""
		return m, nil
	case "y", "enter":
		action := onboarding.Action{ID: m.domainConfirmAction, Label: "Remove domain", Mutates: true}
		value := m.domainConfirmValue
		m.actionRunning = true
		return m, m.execute(action, value)
	}
	return m, nil
}

func (m Model) updateTunnelManager(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := tunnelState(m.report)
	m.tunnelIndex = clamp(m.tunnelIndex, 0, len(state.tunnels)-1)
	switch msg.String() {
	case "esc":
		m.tunnelManager = false
		return m, nil
	case "down", "j":
		m.tunnelIndex = clamp(m.tunnelIndex+1, 0, len(state.tunnels)-1)
		return m, nil
	case "up", "k":
		m.tunnelIndex = clamp(m.tunnelIndex-1, 0, len(state.tunnels)-1)
		return m, nil
	case "a", "n":
		action := onboarding.Action{ID: "create-tunnel", Label: "Create tunnel", Mutates: true, InputPrompt: "Tunnel name"}
		m.inputAction = &action
		m.inputPrefix = ""
		m.input.Placeholder = action.InputPrompt
		m.input.SetValue("vtunnel")
		m.input.Focus()
		return m, nil
	case "enter", "d":
		if len(state.tunnels) == 0 {
			return m, nil
		}
		tunnel := state.tunnels[m.tunnelIndex]
		if tunnel.id == state.configuredID || tunnel.name == state.configuredID {
			m.notice = "Tunnel already selected: " + tunnel.name
			m.err = ""
			return m, nil
		}
		m.actionRunning = true
		return m, m.execute(onboarding.Action{ID: "use-tunnel", Label: "Use tunnel"}, tunnel.id)
	case "x", "delete", "backspace":
		if len(state.tunnels) == 0 {
			return m, nil
		}
		tunnel := state.tunnels[m.tunnelIndex]
		if tunnel.id == state.configuredID || tunnel.name == state.configuredID {
			m.err = "Select another tunnel before deleting the configured tunnel."
			m.notice = ""
			return m, nil
		}
		m.tunnelConfirmAction = "delete-tunnel"
		m.tunnelConfirmValue = tunnel.id
		return m, nil
	}
	return m, nil
}

func (m Model) updateTunnelConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n":
		m.tunnelConfirmAction = ""
		m.tunnelConfirmValue = ""
		return m, nil
	case "y", "enter":
		action := onboarding.Action{ID: m.tunnelConfirmAction, Label: "Delete tunnel", Mutates: true}
		value := m.tunnelConfirmValue
		m.actionRunning = true
		return m, m.execute(action, value)
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	return Render(Snapshot{
		Report:        m.report,
		StepIndex:     m.stepIndex,
		ActionIndex:   m.actionIndex,
		Confirming:    m.confirming,
		InputAction:   m.inputAction,
		InputValue:    m.input.View(),
		DomainManager: m.domainManager,
		DomainIndex:   m.domainIndex,
		DomainConfirm: m.domainConfirmAction != "",
		DomainTarget:  m.domainConfirmValue,
		TunnelManager: m.tunnelManager,
		TunnelIndex:   m.tunnelIndex,
		TunnelConfirm: m.tunnelConfirmAction != "",
		TunnelTarget:  m.tunnelConfirmValue,
		Notice:        m.notice,
		Error:         m.err,
		ActionRunning: m.actionRunning,
		Width:         m.width,
		Height:        m.height,
		Booting:       !m.bootDone,
		BootFrame:     m.bootFrame,
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

func bootTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg {
		return bootTickMsg(t)
	})
}

func bootDone() tea.Cmd {
	return tea.Tick(2300*time.Millisecond, func(time.Time) tea.Msg {
		return bootDoneMsg{}
	})
}

type Snapshot struct {
	Report        onboarding.Report
	StepIndex     int
	ActionIndex   int
	Confirming    bool
	InputAction   *onboarding.Action
	InputValue    string
	DomainManager bool
	DomainIndex   int
	DomainConfirm bool
	DomainTarget  string
	TunnelManager bool
	TunnelIndex   int
	TunnelConfirm bool
	TunnelTarget  string
	Notice        string
	Error         string
	ActionRunning bool
	Width         int
	Height        int
	Booting       bool
	BootFrame     int
}

func Render(snapshot Snapshot) string {
	width := snapshot.Width
	if width < 72 {
		width = 72
	}
	contentWidth := width - 4
	if contentWidth > 150 {
		contentWidth = 150
	}
	if snapshot.Booting {
		return pageStyle.Render(renderBoot(snapshot.BootFrame, contentWidth, snapshot.Height))
	}

	status := warnStyle.Render("needs setup")
	if snapshot.Report.Ready {
		status = okStyle.Render("ready")
	}

	var lines []string
	steps := stepsForReport(snapshot.Report)
	stepIndex := clamp(snapshot.StepIndex, 0, len(steps)-1)

	lines = append(lines, renderHeaderStatus(snapshot.Report, status, contentWidth))
	lines = append(lines, "")
	lines = append(lines, renderOverview(snapshot.Report, stepIndex, contentWidth))
	lines = append(lines, "")
	lines = append(lines, renderStepProgress(steps, stepIndex, contentWidth))
	lines = append(lines, "")
	lines = append(lines, renderWizardLayout(snapshot, steps, stepIndex, contentWidth))
	if snapshot.ActionRunning {
		lines = append(lines, "", pulseStyle.Render("working..."))
	}
	if snapshot.Notice != "" {
		lines = append(lines, "", renderMessage("Done", snapshot.Notice, okStyle, contentWidth))
	}
	if snapshot.Error != "" {
		lines = append(lines, "", renderMessage("Blocked", snapshot.Error, actionStyle, contentWidth))
	}
	lines = append(lines, "", footerStyle.Width(contentWidth).Render("tab next   shift+tab back   up/down action   enter select/continue   r recheck   q quit"))

	rendered := strings.Split(strings.Join(lines, "\n"), "\n")
	if snapshot.DomainManager {
		rendered = overlayCenteredWithClear(rendered, renderDomainManager(snapshot, domainModalWidth(contentWidth)), contentWidth, 2, 4)
	}
	if snapshot.TunnelManager {
		rendered = overlayCenteredWithClear(rendered, renderTunnelManager(snapshot, domainModalWidth(contentWidth)), contentWidth, 2, 4)
	}
	if snapshot.Confirming {
		rendered = overlayCentered(rendered, renderConfirm(snapshot, modalWidth(contentWidth)), contentWidth)
	}
	if snapshot.InputAction != nil {
		rendered = overlayCentered(rendered, renderInput(snapshot, modalWidth(contentWidth)), contentWidth)
	}
	return pageStyle.Render(strings.Join(rendered, "\n"))
}

func renderBoot(frame int, width int, height int) string {
	logo := brandStyle.Render(strings.TrimRight(`
  _   __________  ___  ___  ________ 
 | | / /_  __/ / / / |/ / |/ / __/ / 
 | |/ / / / / /_/ /    /    / _// /__
 |___/ /_/  \____/_/|_/_/|_/___/____/
`, "\n"))
	loader := renderSquareLoader(frame)
	body := strings.Join([]string{
		logo,
		"",
		headingStyle.Render("Welcome to VTunnel"),
		mutedStyle.Render("Preparing your local Cloudflare Tunnel workspace..."),
		"",
		loader,
	}, "\n")
	blockWidth := min(width, 76)
	block := renderPlainBlock("Onboarding", body, blockWidth)
	lines := strings.Split(block, "\n")
	top := max(1, (height-len(lines)-2)/2)
	if height <= 0 {
		top = 2
	}
	left := max(0, (width-lipgloss.Width(lines[0]))/2)
	prefix := strings.Repeat(" ", left)
	out := make([]string, 0, top+len(lines))
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	for _, line := range lines {
		out = append(out, prefix+line)
	}
	return strings.Join(out, "\n")
}

func renderSquareLoader(frame int) string {
	const count = 12
	active := frame % count
	var builder strings.Builder
	for index := 0; index < count; index++ {
		cell := "■"
		switch {
		case index == active:
			builder.WriteString(okStyle.Render(cell))
		case (index+1)%count == active || (index+2)%count == active:
			builder.WriteString(commandStyle.Render(cell))
		default:
			builder.WriteString(mutedStyle.Render("□"))
		}
		if index != count-1 {
			builder.WriteString(" ")
		}
	}
	return builder.String()
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
	steps := stepsForReport(report)
	if len(steps) == 0 {
		return mutedStyle.Render("Status  loading")
	}
	ok, warn, action := stepCounts(steps)
	current := steps[clamp(selected, 0, len(steps)-1)].Title
	line := mutedStyle.Render("Status  ") +
		okStyle.Render(fmt.Sprintf("ready %d", ok)) + mutedStyle.Render("   ") +
		warnStyle.Render(fmt.Sprintf("warn %d", warn)) + mutedStyle.Render("   ") +
		actionStyle.Render(fmt.Sprintf("action %d", action)) + mutedStyle.Render("   ") +
		mutedStyle.Render(fmt.Sprintf("step %d/%d %s", selected+1, len(steps), current))
	return truncate(line, width)
}

func renderWizardLayout(snapshot Snapshot, steps []onboarding.Step, selected int, width int) string {
	if width < 112 {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			renderStepWorkspace(snapshot, steps, selected, width),
		)
	}
	sideWidth := 38
	mainWidth := width - sideWidth - 4
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		renderStepDetails(steps, selected, mainWidth),
		strings.Repeat(" ", 4),
		renderSidePanel(snapshot, steps, selected, sideWidth),
	)
}

func renderStepWorkspace(snapshot Snapshot, steps []onboarding.Step, selected int, width int) string {
	actions := currentActions(snapshot.Report, selected)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		renderStepDetails(steps, selected, width),
		"",
		renderStepActions(actions, snapshot.ActionIndex, selected == len(steps)-1, width),
	)
}

func renderStepProgress(steps []onboarding.Step, selected int, width int) string {
	if len(steps) == 0 {
		return mutedStyle.Render("Loading setup map...")
	}
	cells := make([]string, 0, len(steps))
	for index, step := range steps {
		title := shortStepTitle(step.Title)
		line := fmt.Sprintf("%02d %s %s", index+1, title, plainStepStatus(step))
		if index == selected {
			cells = append(cells, activeStepPillStyle.Render(line))
			continue
		}
		if stepStatus(step) == onboarding.StatusAction {
			cells = append(cells, actionStepStyle.Render(line))
			continue
		}
		cells = append(cells, stepStyle.Render(line))
	}
	lines := packCells(cells, max(16, width-2))
	return renderPlainBlock("Setup path", strings.Join(lines, "\n"), width)
}

func renderSidePanel(snapshot Snapshot, steps []onboarding.Step, selected int, width int) string {
	actions := currentActions(snapshot.Report, selected)
	var progress []string
	for index, step := range steps {
		line := fmt.Sprintf("%02d  %-14s %s", index+1, truncate(shortStepTitle(step.Title), 14), stepStatusLabel(step))
		if index == selected {
			line = selectedStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		progress = append(progress, line)
	}
	return lipgloss.JoinVertical(
		lipgloss.Left,
		renderStepActions(actions, snapshot.ActionIndex, selected == len(steps)-1, width),
		"",
		renderPlainBlock("Steps", strings.Join(progress, "\n"), width),
	)
}

func renderStepDetails(steps []onboarding.Step, selected int, width int) string {
	if len(steps) == 0 {
		return renderBlock("Step", mutedStyle.Render("Loading checks..."), width)
	}
	step := steps[clamp(selected, 0, len(steps)-1)]
	var lines []string
	lines = append(lines, headingStyle.Render(step.Title))
	if step.Summary != "" {
		for _, line := range wrapText(step.Summary, width-4) {
			lines = append(lines, mutedStyle.Render(line))
		}
	}
	if step.Description != "" {
		lines = append(lines, "")
		for _, line := range wrapText(step.Description, width-4) {
			lines = append(lines, mutedStyle.Render(line))
		}
	}
	lines = append(lines, "")
	if len(step.Checks) == 0 && len(step.Commands) == 0 {
		lines = append(lines, mutedStyle.Render("No checks for this phase."))
	}
	for _, check := range step.Checks {
		lines = append(lines, renderCheck(check, width-2))
	}
	if len(step.Commands) > 0 {
		if len(step.Checks) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, sectionKickerStyle.Render("Commands"))
		lines = append(lines, renderCommands(step.Commands, width-2)...)
	}
	if len(step.Manual) > 0 {
		lines = append(lines, "", sectionKickerStyle.Render("Notes"))
		for _, note := range step.Manual {
			for _, line := range wrapText(note, width-6) {
				lines = append(lines, "  "+mutedStyle.Render(line))
			}
		}
	}
	return renderBlock("Current step", strings.Join(lines, "\n"), width)
}

func renderStepActions(actions []onboarding.Action, selected int, final bool, width int) string {
	var lines []string
	if len(actions) == 0 {
		if final {
			lines = append(lines, okStyle.Render("Finish"))
			for _, line := range wrapText("Press enter to close onboarding.", width-4) {
				lines = append(lines, mutedStyle.Render(line))
			}
		} else {
			lines = append(lines, okStyle.Render("Continue"))
			for _, line := range wrapText("Press enter or tab to move to the next step.", width-4) {
				lines = append(lines, mutedStyle.Render(line))
			}
		}
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
	labelWidth := 24
	if width < 56 {
		labelWidth = 16
	}
	label := truncate(check.Label, labelWidth)
	detailWidth := max(12, width-lipgloss.Width(marker)-labelWidth-4)
	detailLines := wrapText(check.Detail, detailWidth)
	if len(detailLines) == 0 {
		detailLines = []string{""}
	}
	lines := []string{
		fmt.Sprintf("%s %-*s %s", marker, labelWidth, label, mutedStyle.Render(detailLines[0])),
	}
	indent := strings.Repeat(" ", lipgloss.Width(marker)+labelWidth+4)
	for _, line := range detailLines[1:] {
		lines = append(lines, indent+mutedStyle.Render(line))
	}
	return strings.Join(lines, "\n")
}

// renderCommands lays out reference commands as a cheat-sheet: the invocation
// is the primary element (command color), the description is muted. No status
// badge, because these are commands to run, not checks that passed.
func renderCommands(commands []onboarding.Command, width int) []string {
	const margin, gap = 2, 2
	nameWidth := 0
	for _, command := range commands {
		if w := lipgloss.Width(command.Invocation); w > nameWidth {
			nameWidth = w
		}
	}
	if maxName := max(12, width/2); nameWidth > maxName {
		nameWidth = maxName
	}
	indent := strings.Repeat(" ", margin+nameWidth+gap)
	var lines []string
	for _, command := range commands {
		name := truncate(command.Invocation, nameWidth)
		descWidth := max(12, width-margin-nameWidth-gap)
		descLines := wrapText(command.Description, descWidth)
		if len(descLines) == 0 {
			descLines = []string{""}
		}
		pad := strings.Repeat(" ", nameWidth-lipgloss.Width(name)+gap)
		lines = append(lines, strings.Repeat(" ", margin)+commandStyle.Render(name)+pad+mutedStyle.Render(descLines[0]))
		for _, line := range descLines[1:] {
			lines = append(lines, indent+mutedStyle.Render(line))
		}
	}
	return lines
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
	if isContinueAction(action) {
		marker = "next"
		markerStyle = okBadgeStyle
	}

	title := action.Label
	if selected {
		title = "> " + title
	} else {
		title = "  " + title
	}
	title = truncate(title, max(10, width-lipgloss.Width(marker)-4))
	if selected {
		title = selectedStyle.Render(title)
	}
	desc := action.Description
	if action.Command != "" {
		desc = action.Command
	}
	gap := max(1, width-lipgloss.Width(title)-lipgloss.Width(marker)-1)
	lines := []string{
		title + strings.Repeat(" ", gap) + markerStyle.Render(marker),
	}
	for _, line := range wrapText(desc, max(10, width-6)) {
		lines = append(lines, "    "+mutedStyle.Render(line))
	}
	if selected {
		return selectedActionStyle.Width(width).Render(strings.Join(lines, "\n"))
	}
	return strings.Join(lines, "\n")
}

func renderConfirm(snapshot Snapshot, width int) string {
	action, ok := selectedAction(currentActions(snapshot.Report, snapshot.StepIndex), snapshot.ActionIndex)
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

func renderDomainManager(snapshot Snapshot, width int) string {
	state := domainState(snapshot.Report)
	selected := clamp(snapshot.DomainIndex, 0, len(state.domains)-1)
	var lines []string
	lines = append(lines, headingStyle.Render("Manage domains"))
	if len(state.domains) == 0 {
		lines = append(lines, mutedStyle.Render("No domain configured yet."))
	} else {
		for index, domain := range state.domains {
			prefix := "  "
			if index == selected {
				prefix = "> "
			}
			suffix := ""
			if domain == state.defaultDomain {
				suffix = " " + okStyle.Render("default")
			}
			line := prefix + domain + suffix
			if index == selected {
				line = selectedStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, "")
	if snapshot.DomainConfirm {
		lines = append(lines, actionStyle.Render("Remove "+snapshot.DomainTarget+"?"))
		lines = append(lines, mutedStyle.Render("y/enter confirm   n/esc cancel"))
	} else {
		lines = append(lines, footerStyle.Render("up/down select   enter default   a add   r rename   x remove   esc close"))
	}
	return renderBlock("Domains", strings.Join(lines, "\n"), width)
}

func renderTunnelManager(snapshot Snapshot, width int) string {
	state := tunnelState(snapshot.Report)
	selected := clamp(snapshot.TunnelIndex, 0, len(state.tunnels)-1)
	var lines []string
	lines = append(lines, headingStyle.Render("Manage tunnels"))
	if len(state.tunnels) == 0 {
		lines = append(lines, mutedStyle.Render("No tunnel visible via cloudflared."))
	} else {
		for index, tunnel := range state.tunnels {
			prefix := "  "
			if index == selected {
				prefix = "> "
			}
			suffix := ""
			if tunnel.id == state.configuredID || tunnel.name == state.configuredID {
				suffix = " " + okStyle.Render("selected")
			}
			line := prefix + tunnel.name + mutedStyle.Render(" "+shortID(tunnel.id)) + suffix
			if index == selected {
				line = selectedStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, "")
	if snapshot.TunnelConfirm {
		lines = append(lines, actionStyle.Render("Delete "+snapshot.TunnelTarget+"?"))
		lines = append(lines, mutedStyle.Render("y/enter confirm   n/esc cancel"))
	} else {
		lines = append(lines, footerStyle.Render("up/down select   enter use   a create   x delete   esc close"))
	}
	return renderBlock("Tunnels", strings.Join(lines, "\n"), width)
}

func renderMessage(title string, message string, style lipgloss.Style, width int) string {
	message = strings.ReplaceAll(strings.TrimSpace(message), "\n  ", "\n")
	body := style.Render(title) + "\n" + mutedStyle.Render(message)
	return renderBlock(title, body, width)
}

func stepStatusLabel(step onboarding.Step) string {
	switch stepStatus(step) {
	case onboarding.StatusOK:
		return okStyle.Render("ok")
	case onboarding.StatusWarn:
		return warnStyle.Render("warn")
	default:
		return actionStyle.Render("fix")
	}
}

func plainStepStatus(step onboarding.Step) string {
	switch stepStatus(step) {
	case onboarding.StatusOK:
		return "ok"
	case onboarding.StatusWarn:
		return "warn"
	default:
		return "fix"
	}
}

func stepCounts(steps []onboarding.Step) (int, int, int) {
	var ok int
	var warn int
	var action int
	for _, step := range steps {
		switch stepStatus(step) {
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
	return renderPlainBlock(title, body, width)
}

func renderPlainBlock(title string, body string, width int) string {
	if width < 16 {
		width = 16
	}
	innerWidth := max(12, width-2)
	title = " " + title + " "
	topFill := max(0, innerWidth-lipgloss.Width(title))
	lines := []string{
		borderStyle.Render("┌" + title + strings.Repeat("─", topFill) + "┐"),
	}
	for _, line := range strings.Split(body, "\n") {
		line = truncate(line, innerWidth)
		lines = append(lines, borderStyle.Render("│")+padRight(line, innerWidth)+borderStyle.Render("│"))
	}
	lines = append(lines, borderStyle.Render("└"+strings.Repeat("─", innerWidth)+"┘"))
	return strings.Join(lines, "\n")
}

func overlayCentered(lines []string, modal string, width int) []string {
	return overlayCenteredWithClear(lines, modal, width, 1, 2)
}

func overlayCenteredWithClear(lines []string, modal string, width int, verticalPad int, horizontalPad int) []string {
	modalLines := strings.Split(modal, "\n")
	needed := len(modalLines) + 4 + verticalPad*2
	if len(lines) < needed {
		for len(lines) < needed {
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
	clearLeft := max(0, left-horizontalPad)
	clearRight := min(width, left+modalWidth+horizontalPad)
	for target := max(0, top-verticalPad); target < min(len(lines), top+len(modalLines)+verticalPad); target++ {
		background := ""
		if target < len(lines) {
			background = stripLine(lines[target], width)
		}
		background = clearLineSegment(background, clearLeft, clearRight-clearLeft, width)
		if target >= top && target < top+len(modalLines) {
			lines[target] = overlayLine(background, modalLines[target-top], left, width)
			continue
		}
		lines[target] = background
	}
	return lines
}

func clearLineSegment(background string, left int, clearWidth int, width int) string {
	if lipgloss.Width(background) < width {
		background = padRight(background, width)
	}
	left = clamp(left, 0, width)
	clearWidth = clamp(clearWidth, 0, width-left)
	prefix := takeWidth(background, left)
	suffix := trimLeftWidth(background, left+clearWidth)
	return truncate(prefix+strings.Repeat(" ", clearWidth)+suffix, width)
}

func overlayLine(background string, modal string, left int, width int) string {
	if lipgloss.Width(background) < width {
		background = padRight(background, width)
	}
	prefix := takeWidth(background, left)
	suffixStart := left + lipgloss.Width(modal)
	suffix := ""
	if suffixStart < lipgloss.Width(background) {
		suffix = trimLeftWidth(background, suffixStart)
	}
	return truncate(prefix+modal+suffix, width)
}

func takeWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		next := builder.String() + string(r)
		if lipgloss.Width(next) > width {
			break
		}
		builder.WriteRune(r)
	}
	return padRight(builder.String(), width)
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

func domainModalWidth(width int) int {
	if width < 86 {
		return max(60, width-6)
	}
	return min(84, width-10)
}

func stepStatus(step onboarding.Step) onboarding.Status {
	status := onboarding.StatusOK
	for _, check := range step.Checks {
		if check.Status == onboarding.StatusAction {
			return onboarding.StatusAction
		}
		if check.Status == onboarding.StatusWarn {
			status = onboarding.StatusWarn
		}
	}
	return status
}

func shortStepTitle(title string) string {
	switch title {
	case "Local checks":
		return "Local"
	case "Cloudflare auth":
		return "Auth"
	case "Cloudflare discovery":
		return "Discovery"
	case "Domain selection":
		return "Domains"
	case "Local cloudflared config":
		return "Config"
	default:
		return title
	}
}

func stepsForReport(report onboarding.Report) []onboarding.Step {
	if len(report.Steps) > 0 {
		return report.Steps
	}
	if report.Ready && len(report.Phases) == 0 {
		return []onboarding.Step{{
			ID:       onboarding.StepCompletion,
			Title:    "Completion",
			Summary:  "vtunnel is ready.",
			Commands: onboarding.CompletionCommands(""),
		}}
	}
	steps := make([]onboarding.Step, 0, len(report.Phases))
	for index, phase := range report.Phases {
		step := onboarding.Step{
			ID:      onboarding.StepID(fmt.Sprintf("phase-%d", index)),
			Title:   phase.Title,
			Summary: phase.Summary,
			Checks:  phase.Checks,
		}
		if index == 0 {
			step.Actions = report.Actions
		}
		steps = append(steps, step)
	}
	return steps
}

func currentActions(report onboarding.Report, stepIndex int) []onboarding.Action {
	steps := stepsForReport(report)
	if len(steps) == 0 {
		return nil
	}
	stepIndex = clamp(stepIndex, 0, len(steps)-1)
	step := steps[stepIndex]
	actions := append([]onboarding.Action{}, step.Actions...)
	if stepIndex < len(steps)-1 {
		actions = append(actions, continueAction())
	}
	return actions
}

func continueAction() onboarding.Action {
	return onboarding.Action{
		ID:          "continue-step",
		Label:       "Continue",
		Description: "Move to the next onboarding step.",
	}
}

func isContinueAction(action onboarding.Action) bool {
	return action.ID == "continue-step"
}

type domainsViewState struct {
	domains       []string
	defaultDomain string
}

type tunnelsViewState struct {
	tunnels      []tunnelView
	configuredID string
}

type tunnelView struct {
	name string
	id   string
}

func domainState(report onboarding.Report) domainsViewState {
	var state domainsViewState
	for _, step := range stepsForReport(report) {
		if step.ID != onboarding.StepDomains && step.Title != "Domain selection" {
			continue
		}
		for _, check := range step.Checks {
			switch check.Label {
			case "selected domains":
				if strings.TrimSpace(check.Detail) != "none configured" {
					for _, domain := range strings.Split(check.Detail, ",") {
						domain = strings.TrimSpace(domain)
						if domain != "" {
							state.domains = append(state.domains, domain)
						}
					}
				}
			case "default domain":
				state.defaultDomain = strings.TrimSpace(check.Detail)
			}
		}
		break
	}
	return state
}

func tunnelState(report onboarding.Report) tunnelsViewState {
	var state tunnelsViewState
	for _, step := range stepsForReport(report) {
		if step.ID != onboarding.StepTunnel && step.Title != "Tunnel" {
			continue
		}
		for _, check := range step.Checks {
			switch check.Label {
			case "available tunnel":
				name, id := parseNameID(check.Detail)
				if name != "" || id != "" {
					state.tunnels = append(state.tunnels, tunnelView{name: name, id: id})
				}
			case "configured tunnel exists":
				_, id := parseNameID(check.Detail)
				if id != "" {
					state.configuredID = id
				}
			case "configured tunnel":
				if state.configuredID == "" {
					state.configuredID = strings.TrimSpace(strings.TrimSuffix(check.Detail, " does not exist"))
				}
			}
		}
		break
	}
	return state
}

func parseNameID(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	open := strings.LastIndex(value, "(")
	close := strings.LastIndex(value, ")")
	if open >= 0 && close > open {
		name := strings.TrimSpace(value[:open])
		id := strings.TrimSpace(value[open+1 : close])
		return name, id
	}
	return value, value
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
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
		runes := []rune(value)
		if len(runes) > width {
			runes = runes[:width]
		}
		return string(runes)
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+3 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func wrapText(value string, width int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if width <= 0 {
		return []string{value}
	}
	var lines []string
	for _, paragraph := range strings.Split(value, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := ""
		for _, word := range words {
			if lipgloss.Width(word) > width {
				if current != "" {
					lines = append(lines, current)
					current = ""
				}
				chunks := splitWidth(word, width)
				lines = append(lines, chunks...)
				continue
			}
			next := word
			if current != "" {
				next = current + " " + word
			}
			if lipgloss.Width(next) <= width {
				current = next
				continue
			}
			lines = append(lines, current)
			current = word
		}
		if current != "" {
			lines = append(lines, current)
		}
	}
	return lines
}

func splitWidth(value string, width int) []string {
	if width <= 0 {
		return []string{value}
	}
	var lines []string
	var current strings.Builder
	for _, r := range value {
		next := current.String() + string(r)
		if current.Len() > 0 && lipgloss.Width(next) > width {
			lines = append(lines, current.String())
			current.Reset()
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func packCells(cells []string, width int) []string {
	var lines []string
	current := ""
	for _, cell := range cells {
		next := cell
		if current != "" {
			next = current + "  " + cell
		}
		if lipgloss.Width(next) <= width {
			current = next
			continue
		}
		if current != "" {
			lines = append(lines, current)
		}
		current = cell
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
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

func padRight(value string, width int) string {
	gap := width - lipgloss.Width(value)
	if gap <= 0 {
		return value
	}
	return value + strings.Repeat(" ", gap)
}

var (
	pageStyle = lipgloss.NewStyle().
			Padding(1, 2)
	selectedActionStyle = lipgloss.NewStyle().
				PaddingLeft(1)

	brandStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	headingStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	sectionKickerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	borderStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("66"))
	mutedStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	footerStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	actionStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	pulseStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("48"))
	commandStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	selectedStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))

	stepStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	activeStepPillStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("16")).
				Background(lipgloss.Color("48")).
				Padding(0, 1)
	actionStepStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	okBadgeStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	warnBadgeStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	actionBadgeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	inputBadgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	mutedBadgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245"))
)
