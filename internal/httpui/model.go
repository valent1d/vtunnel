package httpui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"vtunnel/internal/api"
	"vtunnel/internal/cloudflared"
	"vtunnel/internal/config"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

// keyMap drives the dashboard's key handling (key.Matches) and the help bar.
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Focus    key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding
	Open     key.Binding
	New      key.Binding
	Access   key.Binding
	Stop     key.Binding
	Copy     key.Binding
	Refresh  key.Binding
	Help     key.Binding
	Quit     key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.New, k.Access, k.Stop, k.Copy, k.Refresh, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Focus, k.Open},
		{k.New, k.Access, k.Stop, k.Copy, k.Refresh},
		{k.PageUp, k.PageDown, k.Home, k.End},
		{k.Help, k.Quit},
	}
}

var keys = keyMap{
	Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "navigate")),
	Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
	Focus:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch pane")),
	PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "scroll up")),
	PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "scroll down")),
	Home:     key.NewBinding(key.WithKeys("home"), key.WithHelp("home", "logs start")),
	End:      key.NewBinding(key.WithKeys("end"), key.WithHelp("end", "logs end")),
	Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "request detail")),
	New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
	Access:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "access")),
	Stop:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop")),
	Copy:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "url")),
	Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	Quit:     key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit")),
}

type client interface {
	ListRoutes(context.Context) ([]routes.Route, error)
	AddRoute(context.Context, routes.Route) error
	DeleteRoute(context.Context, string) error
	ListLogs(context.Context, requestlog.Filter) ([]requestlog.Entry, error)
	GetExchange(context.Context, uint64) (requestlog.Exchange, error)
	ReplayRequest(context.Context, uint64) error
}

type Model struct {
	client client
	cfg    config.Config

	routes           []routes.Route
	logs             []requestlog.Entry
	edge             cloudflared.EdgeStatus
	selected         int
	selectedHostname string
	lastLogID        uint64
	logCursor        int  // absolute index into logs (0 = oldest, len-1 = newest)
	logFollow        bool // keep the cursor pinned to the newest request as logs stream in
	focus            focus
	width            int
	height           int

	mode          mode
	createStep    int
	createProtect string // protection mode key chosen in the create flow ("public" = none)
	portInput     textinput.Model
	subInput      textinput.Model
	domainInput   textinput.Model

	access             AccessController
	accessModes        []string // available mode keys in panel order (public/sso/otp)
	accessMode         int      // index into accessModes
	accessIdPs         []string // SSO identity-provider names
	accessIdPIndex     int      // selected identity provider
	accessFocus        int      // index into the focusable widgets of the Access form
	accessBusy         bool
	accessErr          string
	accessTarget       string // hostname the panel operates on (stable across refreshes)
	accessPaused       bool   // whether the target is currently paused
	accessWasProtected bool   // whether the target was protected when the panel opened
	allowInput         textinput.Model

	notice        string
	err           string
	quit          bool
	confirmCancel bool
	helpExpanded  bool

	detail    *requestlog.Exchange // captured headers/body for the open request detail
	detailErr string
}

type mode int

const (
	modeDashboard mode = iota
	modeCreate
	modeConfirmStop
	modeRequestDetail
	modeAccess
)

type focus int

const (
	focusTunnels focus = iota
	focusLogs
)

type routesMsg struct {
	routes []routes.Route
	err    error
}

type logsMsg struct {
	hostname string
	logs     []requestlog.Entry
	err      error
}

type createMsg struct {
	hostname string
	err      error
}

type stopMsg struct {
	hostname string
	err      error
}

type edgeMsg struct {
	status cloudflared.EdgeStatus
}

type exchangeMsg struct {
	id       uint64
	exchange requestlog.Exchange
	err      error
}

type replayMsg struct {
	id  uint64
	err error
}

type tickMsg time.Time

func Run(ctx context.Context, cfg config.Config, selectedHostname string, access AccessController) error {
	program := tea.NewProgram(
		NewModel(api.New(cfg), cfg, selectedHostname, access),
		tea.WithContext(ctx),
	)
	_, err := program.Run()
	return err
}

func NewModel(client client, cfg config.Config, selectedHostname string, access AccessController) Model {
	portInput := textinput.New()
	portInput.Placeholder = "3000"
	portInput.CharLimit = 5
	portInput.SetWidth(12)

	subInput := textinput.New()
	subInput.Placeholder = "dev"
	subInput.CharLimit = 63
	subInput.SetWidth(24)

	domainInput := textinput.New()
	domainInput.Placeholder = cfg.DefaultDomain
	domainInput.CharLimit = 253
	domainInput.SetWidth(32)

	allowInput := textinput.New()
	allowInput.Placeholder = "you@example.com or @example.com"
	allowInput.CharLimit = 253
	allowInput.SetWidth(34)

	return Model{
		client:           client,
		cfg:              cfg,
		access:           access,
		selectedHostname: routes.NormalizeHostname(selectedHostname),
		width:            100,
		height:           30,
		logFollow:        true,
		portInput:        portInput,
		subInput:         subInput,
		domainInput:      domainInput,
		allowInput:       allowInput,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchRoutes(), m.fetchEdge(), m.fetchAccessIdPs(), m.tick())
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
		case modeConfirmStop:
			return m.updateConfirmStop(msg)
		case modeAccess:
			return m.updateAccess(msg)
		case modeRequestDetail:
			switch msg.String() {
			case "esc", "enter", "q":
				m.mode = modeDashboard
				m.detail = nil
				m.detailErr = ""
			case "r":
				if m.detail != nil {
					id := m.detail.ID
					return m, m.replayRequest(id)
				}
			}
			return m, nil
		}
		return m.updateDashboard(msg)
	case routesMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.routes = msg.routes
		m.syncSelection()
		return m, m.fetchLogs()
	case logsMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		if msg.hostname != m.currentHostname() {
			return m, nil
		}
		m.logs = msg.logs
		m.lastLogID = maxLogID(msg.logs)
		if m.logFollow {
			m.logCursor = max(0, len(m.logs)-1)
		} else {
			m.logCursor = clamp(m.logCursor, 0, max(0, len(m.logs)-1))
		}
		m.err = ""
		return m, nil
	case createMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.notice = "Created " + msg.hostname
		m.selectedHostname = msg.hostname
		m.focus = focusLogs
		m.logCursor = 0
		m.logFollow = true
		// If the user chose protection in the create form, jump straight to the
		// Access panel (pre-filled) to finish setting allow/idp.
		if m.createProtect != "public" && m.access != nil {
			m.prepareAccessForCreate(msg.hostname, m.createProtect)
		} else {
			m.mode = modeDashboard
		}
		return m, m.fetchRoutes()
	case exchangeMsg:
		// Ignore a stale fetch if the user already closed/changed the detail.
		if m.mode != modeRequestDetail {
			return m, nil
		}
		if msg.err != nil {
			m.detailErr = msg.err.Error()
			return m, nil
		}
		exchange := msg.exchange
		m.detail = &exchange
		m.detailErr = ""
		return m, nil
	case replayMsg:
		if msg.err != nil {
			m.err = "replay failed: " + msg.err.Error()
			return m, nil
		}
		m.notice = fmt.Sprintf("Replayed request #%d", msg.id)
		m.err = ""
		return m, m.fetchLogs()
	case idpsMsg:
		m.accessIdPs = msg.names
		if m.mode == modeAccess {
			m.rebuildAccessModes()
		}
		return m, nil
	case accessResultMsg:
		m.accessBusy = false
		if msg.err != nil {
			m.accessErr = msg.err.Error()
			return m, nil
		}
		m.mode = modeDashboard
		m.notice = msg.notice
		m.err = ""
		return m, m.fetchRoutes()
	case stopMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.notice = "Stopped " + msg.hostname
		m.mode = modeDashboard
		m.selectAfterStop(msg.hostname)
		return m, m.fetchRoutes()
	case edgeMsg:
		m.edge = msg.status
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.fetchRoutes(), m.fetchEdge(), m.tick())
	}
	return m, nil
}

func (m Model) updateDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Help):
		m.helpExpanded = !m.helpExpanded
		return m, nil
	case key.Matches(msg, keys.Quit):
		m.quit = true
		return m, tea.Quit
	case key.Matches(msg, keys.Focus):
		if m.focus == focusTunnels {
			m.focus = focusLogs
		} else {
			m.focus = focusTunnels
		}
		return m, nil
	case key.Matches(msg, keys.Down):
		return m.moveDown()
	case key.Matches(msg, keys.Up):
		return m.moveUp()
	case key.Matches(msg, keys.PageDown):
		m.moveCursor(10)
		return m, nil
	case key.Matches(msg, keys.PageUp):
		m.moveCursor(-10)
		return m, nil
	case key.Matches(msg, keys.Home):
		m.logCursor = 0
		m.logFollow = false
		return m, nil
	case key.Matches(msg, keys.End):
		m.logCursor = max(0, len(m.logs)-1)
		m.logFollow = true
		return m, nil
	case key.Matches(msg, keys.Refresh):
		return m, m.fetchRoutes()
	case key.Matches(msg, keys.New):
		m.mode = modeCreate
		m.createStep = 0
		m.createProtect = "public"
		m.portInput.SetValue("")
		m.subInput.SetValue("")
		m.domainInput.SetValue(defaultDomain(m.cfg))
		m.focusCreateInput()
		return m, nil
	case key.Matches(msg, keys.Access):
		if m.currentHostname() == "" || m.access == nil {
			return m, nil
		}
		return m.openAccessPanel()
	case key.Matches(msg, keys.Stop):
		if m.currentHostname() == "" {
			return m, nil
		}
		m.mode = modeConfirmStop
		m.confirmCancel = true // stopping is destructive: default to Cancel
		return m, nil
	case key.Matches(msg, keys.Copy):
		if host := m.currentHostname(); host != "" {
			url := "https://" + host
			if err := copyToClipboard(url); err != nil {
				m.notice = "Public URL: " + url
				m.err = "copy failed: " + err.Error()
				return m, nil
			}
			m.notice = "Copied " + url
			m.err = ""
		}
		return m, nil
	case key.Matches(msg, keys.Open):
		if entry, ok := m.currentLog(); ok {
			m.mode = modeRequestDetail
			m.detail = nil
			m.detailErr = ""
			return m, m.fetchExchange(entry.ID)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) moveDown() (tea.Model, tea.Cmd) {
	if m.focus == focusLogs {
		m.moveCursor(1)
		return m, nil
	}
	m.selected = clamp(m.selected+1, 0, len(m.routes)-1)
	m.selectedHostname = m.currentHostname()
	m.logCursor = 0
	m.logFollow = true
	return m, m.fetchLogs()
}

func (m Model) moveUp() (tea.Model, tea.Cmd) {
	if m.focus == focusLogs {
		m.moveCursor(-1)
		return m, nil
	}
	m.selected = clamp(m.selected-1, 0, len(m.routes)-1)
	m.selectedHostname = m.currentHostname()
	m.logCursor = 0
	m.logFollow = true
	return m, m.fetchLogs()
}

// moveCursor moves the request-list cursor by delta and toggles follow mode:
// reaching the newest entry resumes auto-follow; moving up pauses it so the
// view doesn't jump while the user is reading older requests.
func (m *Model) moveCursor(delta int) {
	if len(m.logs) == 0 {
		m.logCursor = 0
		return
	}
	m.logCursor = clamp(m.logCursor+delta, 0, len(m.logs)-1)
	m.logFollow = m.logCursor >= len(m.logs)-1
}

func (m Model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeDashboard
		return m, nil
	case "tab", "down":
		m.createStep = clamp(m.createStep+1, 0, 3)
		m.focusCreateInput()
		return m, nil
	case "shift+tab", "up":
		m.createStep = clamp(m.createStep-1, 0, 3)
		m.focusCreateInput()
		return m, nil
	case "left":
		if m.createStep == 3 {
			m.createProtect = cycleMode(m.createProtect, -1, len(m.accessIdPs) > 0)
		}
		return m, nil
	case "right":
		if m.createStep == 3 {
			m.createProtect = cycleMode(m.createProtect, 1, len(m.accessIdPs) > 0)
		}
		return m, nil
	case "enter":
		if m.createStep < 3 {
			m.createStep++
			m.focusCreateInput()
			return m, nil
		}
		route, err := m.createRoute()
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		return m, m.addRoute(route)
	}
	var cmd tea.Cmd
	switch m.createStep {
	case 0:
		m.portInput, cmd = m.portInput.Update(msg)
	case 1:
		m.subInput, cmd = m.subInput.Update(msg)
	case 2:
		m.domainInput, cmd = m.domainInput.Update(msg)
	}
	return m, cmd
}

func (m Model) updateConfirmStop(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "h", "right", "l", "tab":
		m.confirmCancel = !m.confirmCancel
		return m, nil
	case "esc", "n":
		m.mode = modeDashboard
		return m, nil
	case "enter":
		if m.confirmCancel {
			m.mode = modeDashboard
			return m, nil
		}
		fallthrough
	case "y":
		hostname := m.currentHostname()
		if hostname == "" {
			m.mode = modeDashboard
			return m, nil
		}
		return m, m.stopRoute(hostname)
	}
	return m, nil
}

func (m *Model) syncSelection() {
	if len(m.routes) == 0 {
		m.selected = 0
		m.selectedHostname = ""
		m.logs = nil
		m.lastLogID = 0
		m.logCursor = 0
		m.logFollow = true
		return
	}
	if m.selectedHostname != "" {
		for index, route := range m.routes {
			if route.Hostname == m.selectedHostname {
				m.selected = index
				return
			}
		}
	}
	m.selected = clamp(m.selected, 0, len(m.routes)-1)
	m.selectedHostname = m.routes[m.selected].Hostname
}

func (m *Model) selectAfterStop(hostname string) {
	if len(m.routes) == 0 {
		m.selectedHostname = ""
		m.selected = 0
		return
	}
	index := m.selected
	for routeIndex, route := range m.routes {
		if route.Hostname == hostname {
			index = routeIndex
			break
		}
	}
	if index >= len(m.routes)-1 {
		index = max(0, len(m.routes)-2)
	}
	m.selected = index
	m.selectedHostname = ""
	for routeIndex, route := range m.routes {
		if route.Hostname != hostname && routeIndex >= index {
			m.selectedHostname = route.Hostname
			return
		}
	}
}

func (m *Model) focusCreateInput() {
	m.portInput.Blur()
	m.subInput.Blur()
	m.domainInput.Blur()
	switch m.createStep {
	case 0:
		m.portInput.Focus()
	case 1:
		m.subInput.Focus()
	case 2:
		m.domainInput.Focus()
		// step 3 = protect selector, no text input focused
	}
}

func (m Model) currentRoute() (routes.Route, bool) {
	if len(m.routes) == 0 {
		return routes.Route{}, false
	}
	index := clamp(m.selected, 0, len(m.routes)-1)
	return m.routes[index], true
}

func (m Model) currentHostname() string {
	route, ok := m.currentRoute()
	if !ok {
		return ""
	}
	return route.Hostname
}

func (m Model) createRoute() (routes.Route, error) {
	port, err := normalizePort(m.portInput.Value())
	if err != nil {
		return routes.Route{}, err
	}
	host, err := hostnameForCreate(m.subInput.Value(), m.domainInput.Value(), m.cfg)
	if err != nil {
		return routes.Route{}, err
	}
	return routes.Route{Hostname: host, Target: "http://127.0.0.1:" + port}, nil
}

func (m Model) fetchRoutes() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		list, err := m.client.ListRoutes(ctx)
		return routesMsg{routes: list, err: err}
	}
}

// currentLog returns the request log entry under the cursor, if any.
func (m Model) currentLog() (requestlog.Entry, bool) {
	if m.focus != focusLogs || len(m.logs) == 0 {
		return requestlog.Entry{}, false
	}
	return m.logs[clamp(m.logCursor, 0, len(m.logs)-1)], true
}

func (m Model) fetchExchange(id uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		exchange, err := m.client.GetExchange(ctx, id)
		return exchangeMsg{id: id, exchange: exchange, err: err}
	}
}

func (m Model) replayRequest(id uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		return replayMsg{id: id, err: m.client.ReplayRequest(ctx, id)}
	}
}

func (m Model) fetchLogs() tea.Cmd {
	hostname := m.currentHostname()
	if hostname == "" {
		return func() tea.Msg { return logsMsg{} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		logs, err := m.client.ListLogs(ctx, requestlog.Filter{Hostname: hostname, Limit: 500})
		return logsMsg{hostname: hostname, logs: logs, err: err}
	}
}

// fetchEdge parses cloudflared's log for current edge connections. Read errors
// (e.g. the log doesn't exist yet) yield an empty status rather than an error.
func (m Model) fetchEdge() tea.Cmd {
	return func() tea.Msg {
		var paths []string
		if p, err := config.CloudflaredLogPath(); err == nil {
			paths = append(paths, p)
		}
		if p, err := config.CloudflaredServiceLogPath(); err == nil {
			paths = append(paths, p)
		}
		status, _ := cloudflared.ParseEdgeStatus(paths...)
		return edgeMsg{status: status}
	}
}

func (m Model) addRoute(route routes.Route) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := m.client.AddRoute(ctx, route)
		return createMsg{hostname: route.Hostname, err: err}
	}
}

func (m Model) stopRoute(hostname string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := m.client.DeleteRoute(ctx, hostname)
		return stopMsg{hostname: hostname, err: err}
	}
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func normalizePort(value string) (string, error) {
	value = strings.TrimSpace(value)
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port %q", value)
	}
	return strconv.Itoa(port), nil
}

func hostnameForCreate(subdomain string, domain string, cfg config.Config) (string, error) {
	subdomain = routes.NormalizeHostname(subdomain)
	domain = routes.NormalizeHostname(domain)
	if domain == "" {
		domain = routes.NormalizeHostname(cfg.DefaultDomain)
	}
	if subdomain == "" {
		return "", fmt.Errorf("subdomain is required")
	}
	if strings.Contains(subdomain, ".") && domain == "" {
		return subdomain, nil
	}
	if strings.Contains(subdomain, ".") && domain != "" {
		return subdomain, nil
	}
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}
	return subdomain + "." + domain, nil
}

func defaultDomain(cfg config.Config) string {
	if cfg.DefaultDomain != "" {
		return cfg.DefaultDomain
	}
	if len(cfg.Domains) > 0 {
		return cfg.Domains[0]
	}
	return ""
}

func maxLogID(logs []requestlog.Entry) uint64 {
	var maxID uint64
	for _, entry := range logs {
		if entry.ID > maxID {
			maxID = entry.ID
		}
	}
	return maxID
}

func copyToClipboard(value string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	default:
		switch {
		case clipboardToolAvailable("xclip"):
			cmd = exec.Command("xclip", "-selection", "clipboard")
		case clipboardToolAvailable("xsel"):
			cmd = exec.Command("xsel", "--clipboard", "--input")
		case clipboardToolAvailable("wl-copy"):
			cmd = exec.Command("wl-copy")
		default:
			return fmt.Errorf("no clipboard tool found (install xclip, xsel, or wl-clipboard)")
		}
	}
	cmd.Stdin = bytes.NewBufferString(value)
	return cmd.Run()
}

func clipboardToolAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
