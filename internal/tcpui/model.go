// Package tcpui is the bubbletea dashboard for managing TCP tunnels. Unlike the
// HTTP dashboard there are no request logs or stats (cloudflared proxies TCP
// directly), so this is a focused manager: list, create, remove, and copy the
// client connect command.
package tcpui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// Tunnel is a TCP tunnel surfaced in the dashboard.
type Tunnel struct {
	Hostname   string
	Target     string // host:port (no scheme)
	ConnectCmd string // `vtunnel tcp connect <host>`
}

// Manager performs the TCP operations on behalf of the TUI. The real
// implementation lives in the CLI so this package stays free of the cloudflared
// client. Add/Remove restart cloudflared, so they may take a couple of seconds.
type Manager interface {
	List(ctx context.Context) ([]Tunnel, error)
	Add(ctx context.Context, target, subdomain string) error
	Remove(ctx context.Context, hostname string) error
}

type mode int

const (
	modeDashboard mode = iota
	modeCreate
	modeConfirmRemove
)

type Model struct {
	manager  Manager
	tunnels  []Tunnel
	selected int
	width    int
	height   int

	mode        mode
	createStep  int
	targetInput textinput.Model
	subInput    textinput.Model

	busy   bool
	err    string
	notice string
	quit   bool
}

// Run launches the TCP dashboard.
func Run(ctx context.Context, manager Manager) error {
	program := tea.NewProgram(NewModel(manager), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}

func NewModel(manager Manager) Model {
	target := textinput.New()
	target.Placeholder = "3306 or host:port"
	target.CharLimit = 253
	target.SetWidth(28)

	sub := textinput.New()
	sub.Placeholder = "db"
	sub.CharLimit = 63
	sub.SetWidth(24)

	return Model{manager: manager, width: 100, height: 30, targetInput: target, subInput: sub}
}

func (m Model) Init() tea.Cmd { return m.fetch() }

type tunnelsMsg struct {
	tunnels []Tunnel
	err     error
}

type opMsg struct {
	notice string
	err    error
}

func (m Model) fetch() tea.Cmd {
	manager := m.manager
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tunnels, err := manager.List(ctx)
		return tunnelsMsg{tunnels: tunnels, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyPressMsg:
		switch m.mode {
		case modeCreate:
			return m.updateCreate(msg)
		case modeConfirmRemove:
			return m.updateConfirm(msg)
		}
		return m.updateDashboard(msg)
	case tunnelsMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.tunnels = msg.tunnels
		m.selected = clamp(m.selected, 0, max(0, len(m.tunnels)-1))
		m.err = ""
		return m, nil
	case opMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.notice = msg.notice
		m.err = ""
		m.mode = modeDashboard
		return m, m.fetch()
	}
	return m, nil
}

func (m Model) updateDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "down", "j":
		m.selected = clamp(m.selected+1, 0, max(0, len(m.tunnels)-1))
		return m, nil
	case "up", "k":
		m.selected = clamp(m.selected-1, 0, max(0, len(m.tunnels)-1))
		return m, nil
	case "r":
		m.notice = ""
		return m, m.fetch()
	case "n":
		m.mode = modeCreate
		m.createStep = 0
		m.targetInput.SetValue("")
		m.subInput.SetValue("")
		m.err = ""
		m.focusCreate()
		return m, textinput.Blink
	case "x", "d":
		if len(m.tunnels) > 0 {
			m.mode = modeConfirmRemove
		}
		return m, nil
	case "c":
		if tunnel, ok := m.current(); ok {
			if err := copyToClipboard(tunnel.ConnectCmd); err != nil {
				m.notice = "Connect: " + tunnel.ConnectCmd
			} else {
				m.notice = "Copied: " + tunnel.ConnectCmd
			}
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeDashboard
		return m, nil
	case "tab", "down":
		m.createStep = clamp(m.createStep+1, 0, 1)
		m.focusCreate()
		return m, nil
	case "shift+tab", "up":
		m.createStep = clamp(m.createStep-1, 0, 1)
		m.focusCreate()
		return m, nil
	case "enter":
		if m.createStep < 1 {
			m.createStep++
			m.focusCreate()
			return m, nil
		}
		target := strings.TrimSpace(m.targetInput.Value())
		sub := m.subInput.Value()
		if target == "" {
			m.err = "target is required (a port or host:port)"
			return m, nil
		}
		manager := m.manager
		m.busy = true
		m.err = ""
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			return opMsg{notice: "Exposed " + sub, err: manager.Add(ctx, target, sub)}
		}
	}
	var cmd tea.Cmd
	if m.createStep == 0 {
		m.targetInput, cmd = m.targetInput.Update(msg)
	} else {
		m.subInput, cmd = m.subInput.Update(msg)
	}
	return m, cmd
}

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		tunnel, ok := m.current()
		if !ok {
			m.mode = modeDashboard
			return m, nil
		}
		manager := m.manager
		host := tunnel.Hostname
		m.busy = true
		m.err = ""
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			return opMsg{notice: "Removed " + host, err: manager.Remove(ctx, host)}
		}
	case "n", "esc":
		m.mode = modeDashboard
		return m, nil
	}
	return m, nil
}

func (m *Model) focusCreate() {
	m.targetInput.Blur()
	m.subInput.Blur()
	if m.createStep == 0 {
		m.targetInput.Focus()
	} else {
		m.subInput.Focus()
	}
}

func (m Model) current() (Tunnel, bool) {
	if len(m.tunnels) == 0 {
		return Tunnel{}, false
	}
	return m.tunnels[clamp(m.selected, 0, len(m.tunnels)-1)], true
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
