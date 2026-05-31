// Package mcpsetupui is the bubbletea dashboard for registering vtunnel's MCP
// server into AI coding clients (Claude Code, Cursor, VS Code, Codex, OpenCode,
// Antigravity), seeing which are configured, and adding/removing it. The actual
// client operations live behind the Manager interface so the CLI supplies them
// and this package stays free of the setup plumbing.
package mcpsetupui

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Client is a client's setup state plus its manual-setup instructions, surfaced
// in the dashboard.
type Client struct {
	ID         string
	Name       string
	Installed  bool
	Configured bool
	Location   string
	Note       string
	// Manual instructions: either ManualCommand (a CLI line) or ManualPath +
	// ManualSnippet (a file to edit and the snippet to paste).
	ManualPath    string
	ManualCommand string
	ManualSnippet string
}

// Manager performs the client operations on behalf of the TUI.
type Manager interface {
	List(ctx context.Context) ([]Client, error)
	Add(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	SelfCheck(ctx context.Context) (tools []string, err error)
	Scope() string
}

type Model struct {
	manager  Manager
	clients  []Client
	selected int
	width    int
	height   int

	busy   bool
	err    string
	notice string
	check  string
	quit   bool
}

// Run launches the MCP setup dashboard.
func Run(ctx context.Context, manager Manager) error {
	program := tea.NewProgram(NewModel(manager), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}

func NewModel(manager Manager) Model {
	return Model{manager: manager, width: 100, height: 30}
}

func (m Model) Init() tea.Cmd { return m.fetch() }

type clientsMsg struct {
	clients []Client
	err     error
}

type opMsg struct {
	notice string
	err    error
}

type checkMsg struct {
	line string
}

func (m Model) fetch() tea.Cmd {
	manager := m.manager
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		clients, err := manager.List(ctx)
		return clientsMsg{clients: clients, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case clientsMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.clients = msg.clients
		m.selected = clamp(m.selected, 0, max(0, len(m.clients)-1))
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
		return m, m.fetch()
	case checkMsg:
		m.busy = false
		m.check = msg.line
		return m, nil
	}
	return m, nil
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.quit = true
		return m, tea.Quit
	case "down", "j":
		m.selected = clamp(m.selected+1, 0, max(0, len(m.clients)-1))
		return m, nil
	case "up", "k":
		m.selected = clamp(m.selected-1, 0, max(0, len(m.clients)-1))
		return m, nil
	case "r":
		m.notice = ""
		return m, m.fetch()
	case "a":
		return m.runOp(true)
	case "x", "d":
		return m.runOp(false)
	case "c":
		manager := m.manager
		m.busy = true
		m.check = "checking…"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			tools, err := manager.SelfCheck(ctx)
			if err != nil {
				return checkMsg{line: "server self-check: ✗ " + err.Error()}
			}
			return checkMsg{line: "server self-check: ✓ responds, " + strconv.Itoa(len(tools)) + " tools"}
		}
	}
	return m, nil
}

func (m Model) runOp(add bool) (tea.Model, tea.Cmd) {
	client, ok := m.current()
	if !ok {
		return m, nil
	}
	manager := m.manager
	id, name := client.ID, client.Name
	m.busy = true
	m.err = ""
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if add {
			return opMsg{notice: "Added to " + name, err: manager.Add(ctx, id)}
		}
		return opMsg{notice: "Removed from " + name, err: manager.Remove(ctx, id)}
	}
}

func (m Model) current() (Client, bool) {
	if len(m.clients) == 0 {
		return Client{}, false
	}
	return m.clients[clamp(m.selected, 0, len(m.clients)-1)], true
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
