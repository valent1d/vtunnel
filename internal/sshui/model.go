// Package sshui is the bubbletea dashboard for browser-SSH endpoints (vtunnel
// ssh). Like the TCP dashboard it's a focused manager — list, create, remove,
// copy URL — but every endpoint is gated by a Cloudflare Access login, so the
// create form also collects who may sign in and (optionally) which identity
// provider to use.
package sshui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// Endpoint is a browser-SSH endpoint surfaced in the dashboard.
type Endpoint struct {
	Hostname string // box.example.com
	Target   string // host:port (no scheme), e.g. localhost:22
	URL      string // https://box.example.com
}

// Manager performs the SSH operations on behalf of the TUI. The real
// implementation lives in the CLI so this package stays free of the Cloudflare
// client. Add/Remove provision Access and restart cloudflared, so they may take
// a few seconds.
type Manager interface {
	List(ctx context.Context) ([]Endpoint, error)
	Add(ctx context.Context, subdomain, target, allow, idp string) error
	Remove(ctx context.Context, hostname string) error
}

type mode int

const (
	modeDashboard mode = iota
	modeCreate
	modeConfirmRemove
)

// Create form fields, in tab order.
const (
	fSub = iota
	fTarget
	fAllow
	fIdP
	fieldCount
)

type Model struct {
	manager   Manager
	endpoints []Endpoint
	selected  int
	width     int
	height    int

	mode       mode
	createStep int
	inputs     []textinput.Model

	busy   bool
	err    string
	notice string
	quit   bool
}

// Run launches the browser-SSH dashboard.
func Run(ctx context.Context, manager Manager) error {
	program := tea.NewProgram(NewModel(manager), tea.WithContext(ctx))
	_, err := program.Run()
	return err
}

func NewModel(manager Manager) Model {
	sub := textinput.New()
	sub.Placeholder = "box"
	sub.CharLimit = 63
	sub.SetWidth(34)

	target := textinput.New()
	target.Placeholder = "localhost:22"
	target.SetValue("localhost:22")
	target.CharLimit = 253
	target.SetWidth(34)

	allow := textinput.New()
	allow.Placeholder = "you@example.com or @example.com"
	allow.CharLimit = 254
	allow.SetWidth(34)

	idp := textinput.New()
	idp.Placeholder = "blank = email one-time PIN"
	idp.CharLimit = 63
	idp.SetWidth(34)

	return Model{
		manager: manager,
		width:   100,
		height:  30,
		inputs:  []textinput.Model{sub, target, allow, idp},
	}
}

func (m Model) Init() tea.Cmd { return m.fetch() }

type endpointsMsg struct {
	endpoints []Endpoint
	err       error
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
		endpoints, err := manager.List(ctx)
		return endpointsMsg{endpoints: endpoints, err: err}
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
	case endpointsMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.endpoints = msg.endpoints
		m.selected = clamp(m.selected, 0, max(0, len(m.endpoints)-1))
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
		m.selected = clamp(m.selected+1, 0, max(0, len(m.endpoints)-1))
		return m, nil
	case "up", "k":
		m.selected = clamp(m.selected-1, 0, max(0, len(m.endpoints)-1))
		return m, nil
	case "r":
		m.notice = ""
		return m, m.fetch()
	case "n":
		m.mode = modeCreate
		m.createStep = 0
		m.inputs[fSub].SetValue("")
		m.inputs[fTarget].SetValue("localhost:22")
		m.inputs[fAllow].SetValue("")
		m.inputs[fIdP].SetValue("")
		m.err = ""
		m.focusCreate()
		return m, textinput.Blink
	case "x", "d":
		if len(m.endpoints) > 0 {
			m.mode = modeConfirmRemove
		}
		return m, nil
	case "c":
		if endpoint, ok := m.current(); ok {
			if err := copyToClipboard(endpoint.URL); err != nil {
				m.notice = "URL: " + endpoint.URL
			} else {
				m.notice = "Copied: " + endpoint.URL
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
		m.createStep = clamp(m.createStep+1, 0, fieldCount-1)
		m.focusCreate()
		return m, nil
	case "shift+tab", "up":
		m.createStep = clamp(m.createStep-1, 0, fieldCount-1)
		m.focusCreate()
		return m, nil
	case "enter":
		if m.createStep < fieldCount-1 {
			m.createStep++
			m.focusCreate()
			return m, nil
		}
		return m.submitCreate()
	}
	var cmd tea.Cmd
	m.inputs[m.createStep], cmd = m.inputs[m.createStep].Update(msg)
	return m, cmd
}

func (m Model) submitCreate() (tea.Model, tea.Cmd) {
	sub := strings.TrimSpace(m.inputs[fSub].Value())
	target := strings.TrimSpace(m.inputs[fTarget].Value())
	allow := strings.TrimSpace(m.inputs[fAllow].Value())
	idp := strings.TrimSpace(m.inputs[fIdP].Value())
	if sub == "" {
		m.err = "subdomain is required"
		return m, nil
	}
	if allow == "" {
		m.err = "allow is required — an email or @domain (the prefix must match the SSH username)"
		return m, nil
	}
	manager := m.manager
	m.busy = true
	m.err = ""
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return opMsg{notice: "Browser SSH ready: https://" + sub, err: manager.Add(ctx, sub, target, allow, idp)}
	}
}

func (m Model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		endpoint, ok := m.current()
		if !ok {
			m.mode = modeDashboard
			return m, nil
		}
		manager := m.manager
		host := endpoint.Hostname
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
	for i := range m.inputs {
		m.inputs[i].Blur()
	}
	m.inputs[m.createStep].Focus()
}

func (m Model) current() (Endpoint, bool) {
	if len(m.endpoints) == 0 {
		return Endpoint{}, false
	}
	return m.endpoints[clamp(m.selected, 0, len(m.endpoints)-1)], true
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
