package httpui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// AccessController performs Cloudflare Access operations on behalf of the TUI.
// The real implementation lives in the CLI so httpui stays free of the
// Cloudflare client. All methods may do network I/O and run in tea.Cmd
// goroutines.
type AccessController interface {
	Protect(ctx context.Context, hostname, mode, allow, idp string) error
	Unprotect(ctx context.Context, hostname string) error
	Pause(ctx context.Context, hostname string) error
	Resume(ctx context.Context, hostname string) error
	// IdentityProviders returns the names of SSO identity providers configured
	// in Cloudflare Access (excluding the built-in one-time PIN).
	IdentityProviders(ctx context.Context) []string
}

// accessModeOrder lists the protection modes in panel order. SSO appears right
// after Public when at least one identity provider is configured.
func accessModeOrder(hasIdP bool) []string {
	if hasIdP {
		return []string{"public", "sso", "otp"}
	}
	return []string{"public", "otp"}
}

var (
	accessModeLabel = map[string]string{"public": "Public", "sso": "SSO", "otp": "OTP"}
	accessModeHelp  = map[string]string{
		"public": "No login — anyone with the link.",
		"sso":    "Sign in with your identity provider.",
		"otp":    "Email one-time PIN. Allow emails or @domains.",
	}
)

// accessWidget is a focusable element of the Access form.
type accessWidget int

const (
	wMode accessWidget = iota
	wIdP
	wAllow
	wSave
	wPauseResume
	wRemove
	wCancel
)

// accessWidgets returns the focusable elements for the current mode.
func accessWidgets(modeKey string, protected bool) []accessWidget {
	widgets := []accessWidget{wMode}
	switch modeKey {
	case "sso":
		widgets = append(widgets, wIdP, wAllow)
	case "otp":
		widgets = append(widgets, wAllow)
	}
	widgets = append(widgets, wSave)
	if protected {
		widgets = append(widgets, wPauseResume, wRemove)
	}
	widgets = append(widgets, wCancel)
	return widgets
}

type accessResultMsg struct {
	err    error
	notice string
}

type idpsMsg struct{ names []string }

func (m Model) fetchAccessIdPs() tea.Cmd {
	controller := m.access
	if controller == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return idpsMsg{names: controller.IdentityProviders(ctx)}
	}
}

func (m Model) modeKey() string {
	if m.accessMode < 0 || m.accessMode >= len(m.accessModes) {
		return "public"
	}
	return m.accessModes[m.accessMode]
}

// rebuildAccessModes recomputes the available modes (SSO depends on whether IdPs
// exist) while keeping the current mode selected when still available.
func (m *Model) rebuildAccessModes() {
	current := m.modeKey()
	m.accessModes = accessModeOrder(len(m.accessIdPs) > 0)
	m.accessMode = 0
	for i, key := range m.accessModes {
		if key == current {
			m.accessMode = i
		}
	}
}

// openAccessPanel switches to the Access form, pre-filled from the selected
// route's current protection, and refreshes the identity-provider list.
func (m Model) openAccessPanel() (tea.Model, tea.Cmd) {
	hostname := m.currentHostname()
	if hostname == "" {
		return m, nil
	}
	route := selectedRoute(m.routes, m.selected)
	m.mode = modeAccess
	m.accessTarget = hostname
	m.accessFocus = 0
	m.accessErr = ""
	m.accessBusy = false
	m.accessIdPIndex = 0
	m.allowInput.SetValue("")
	m.accessPaused = false
	m.accessWasProtected = false

	current := "public"
	if route.Access != nil {
		m.accessWasProtected = true
		m.accessPaused = route.Access.Paused
		current = route.Access.Mode
		m.allowInput.SetValue(joinAllow(route.Access.Allow))
		for i, name := range m.accessIdPs {
			if name == route.Access.IdP {
				m.accessIdPIndex = i
			}
		}
	}
	m.accessModes = accessModeOrder(len(m.accessIdPs) > 0)
	m.accessMode = 0
	for i, key := range m.accessModes {
		if key == current {
			m.accessMode = i
		}
	}
	return m, tea.Batch(m.fetchAccessIdPs(), m.refreshAccessFocus())
}

// prepareAccessForCreate opens the panel for a freshly-created route, focused on
// the first editable field.
func (m *Model) prepareAccessForCreate(hostname, modeKey string) {
	m.mode = modeAccess
	m.accessTarget = hostname
	m.accessWasProtected = false
	m.accessPaused = false
	m.accessBusy = false
	m.accessErr = ""
	m.accessIdPIndex = 0
	m.allowInput.SetValue("")
	m.accessModes = accessModeOrder(len(m.accessIdPs) > 0)
	m.accessMode = 0
	for i, key := range m.accessModes {
		if key == modeKey {
			m.accessMode = i
		}
	}
	m.accessFocus = 1 // first field after the mode selector
	_ = m.refreshAccessFocus()
}

func (m Model) updateAccess(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.accessBusy {
		return m, nil
	}
	widgets := accessWidgets(m.modeKey(), m.accessWasProtected)
	m.accessFocus = clamp(m.accessFocus, 0, len(widgets)-1)
	focused := widgets[m.accessFocus]

	switch msg.String() {
	case "esc":
		m.mode = modeDashboard
		m.accessErr = ""
		return m, nil
	case "tab", "down":
		m.accessFocus = (m.accessFocus + 1) % len(widgets)
		return m, m.refreshAccessFocus()
	case "shift+tab", "up":
		m.accessFocus = (m.accessFocus - 1 + len(widgets)) % len(widgets)
		return m, m.refreshAccessFocus()
	case "left", "right":
		delta := 1
		if msg.String() == "left" {
			delta = -1
		}
		switch focused {
		case wMode:
			m.accessMode = clamp(m.accessMode+delta, 0, len(m.accessModes)-1)
			widgets = accessWidgets(m.modeKey(), m.accessWasProtected)
			m.accessFocus = clamp(m.accessFocus, 0, len(widgets)-1)
			return m, m.refreshAccessFocus()
		case wIdP:
			if len(m.accessIdPs) > 0 {
				m.accessIdPIndex = clamp(m.accessIdPIndex+delta, 0, len(m.accessIdPs)-1)
			}
			return m, nil
		}
	case "enter":
		switch focused {
		case wPauseResume:
			return m.togglePause()
		case wRemove:
			m.accessMode = indexOfMode(m.accessModes, "public")
			return m.applyAccess()
		case wCancel:
			m.mode = modeDashboard
			m.accessErr = ""
			return m, nil
		default:
			return m.applyAccess()
		}
	}

	if focused == wAllow {
		var cmd tea.Cmd
		m.allowInput, cmd = m.allowInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// cycleMode moves through the available modes by delta and returns the new key.
func cycleMode(current string, delta int, hasIdP bool) string {
	order := accessModeOrder(hasIdP)
	idx := clamp(indexOfMode(order, current)+delta, 0, len(order)-1)
	return order[idx]
}

func indexOfMode(modes []string, key string) int {
	for i, m := range modes {
		if m == key {
			return i
		}
	}
	return 0
}

// refreshAccessFocus focuses the allow input when it is the active widget.
func (m *Model) refreshAccessFocus() tea.Cmd {
	m.allowInput.Blur()
	widgets := accessWidgets(m.modeKey(), m.accessWasProtected)
	if m.accessFocus < len(widgets) && widgets[m.accessFocus] == wAllow {
		return m.allowInput.Focus()
	}
	return nil
}

func (m Model) applyAccess() (tea.Model, tea.Cmd) {
	hostname := m.accessTarget
	if hostname == "" || m.access == nil {
		m.accessErr = "Access control is unavailable here"
		return m, nil
	}
	mode := m.modeKey()
	allow := m.allowInput.Value()
	idp := ""
	if mode == "sso" {
		if len(m.accessIdPs) == 0 {
			m.accessErr = "no identity provider configured — add one with `vtunnel access idp add`"
			return m, nil
		}
		idp = m.accessIdPs[clamp(m.accessIdPIndex, 0, len(m.accessIdPs)-1)]
	}
	controller := m.access
	m.accessBusy = true
	m.accessErr = ""
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if mode == "public" {
			return accessResultMsg{err: controller.Unprotect(ctx, hostname), notice: "Unprotected " + hostname}
		}
		return accessResultMsg{err: controller.Protect(ctx, hostname, mode, allow, idp), notice: "Protected " + hostname + " (" + mode + ")"}
	}
}

func (m Model) togglePause() (tea.Model, tea.Cmd) {
	hostname := m.accessTarget
	if hostname == "" || m.access == nil {
		return m, nil
	}
	controller := m.access
	paused := m.accessPaused
	m.accessBusy = true
	m.accessErr = ""
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if paused {
			return accessResultMsg{err: controller.Resume(ctx, hostname), notice: "Resumed " + hostname}
		}
		return accessResultMsg{err: controller.Pause(ctx, hostname), notice: "Paused " + hostname + " (public)"}
	}
}

func joinAllow(allow []string) string {
	out := ""
	for i, a := range allow {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

// renderAccessPanel draws the Access form: a segmented mode selector, an IdP
// picker (SSO), an optional allow field, and action buttons — all with focus.
func renderAccessPanel(snapshot Snapshot, width int) string {
	modeKey := "public"
	if snapshot.AccessMode >= 0 && snapshot.AccessMode < len(snapshot.AccessModes) {
		modeKey = snapshot.AccessModes[snapshot.AccessMode]
	}
	widgets := accessWidgets(modeKey, snapshot.AccessProtected)
	focus := clamp(snapshot.AccessField, 0, len(widgets)-1)
	focused := widgets[focus]

	chips := make([]string, len(snapshot.AccessModes))
	for i, key := range snapshot.AccessModes {
		style := accessChipStyle
		if i == snapshot.AccessMode {
			style = accessChipActiveStyle
		}
		chips[i] = style.Render(accessModeLabel[key])
	}
	modeLabel := "  Protection"
	if focused == wMode {
		modeLabel = accessFocusMarker + " Protection" + mutedStyle.Render("   ◂/▸ choose")
	}
	rows := []string{
		mutedStyle.Render(modeLabel),
		"  " + lipgloss.JoinHorizontal(lipgloss.Top, chips...),
		mutedStyle.Render("  " + accessModeHelp[modeKey]),
	}

	if modeKey == "sso" {
		rows = append(rows, "", accessIdPRow(snapshot, focused == wIdP))
		rows = append(rows, accessInputRow("Restrict (optional)", snapshot.AllowInput, focused == wAllow))
	} else if modeKey == "otp" {
		rows = append(rows, "", accessInputRow("Allow", snapshot.AllowInput, focused == wAllow))
	}

	buttons := []string{accessButton("Save", focused == wSave)}
	if snapshot.AccessProtected {
		pause := "Pause"
		if snapshot.AccessPaused {
			pause = "Resume"
		}
		buttons = append(buttons, accessButton(pause, focused == wPauseResume), accessButton("Remove", focused == wRemove))
	}
	buttons = append(buttons, accessButton("Cancel", focused == wCancel))
	rows = append(rows, "", "  "+lipgloss.JoinHorizontal(lipgloss.Top, buttons...), "")

	switch {
	case snapshot.AccessBusy:
		rows = append(rows, "  "+okStyle.Render("working…"))
	case snapshot.AccessErr != "":
		rows = append(rows, "  "+actionStyle.Render(snapshot.AccessErr))
	default:
		rows = append(rows, "  "+mutedStyle.Render("tab move · ◂/▸ choose · enter apply · esc cancel"))
	}

	title := "Access"
	if snapshot.AccessHost != "" {
		title = "Access · " + snapshot.AccessHost
	}
	return renderBox(title, lipgloss.JoinVertical(lipgloss.Left, rows...), width)
}

func accessIdPRow(snapshot Snapshot, focused bool) string {
	prefix := "  "
	labelStyle := mutedStyle
	if focused {
		prefix = accessFocusMarker + " "
		labelStyle = brandStyle
	}
	value := mutedStyle.Render("(none — add with `vtunnel access idp add`)")
	if len(snapshot.AccessIdPs) > 0 {
		idx := clamp(snapshot.AccessIdPIndex, 0, len(snapshot.AccessIdPs)-1)
		value = commandStyle.Render("‹ "+snapshot.AccessIdPs[idx]+" ›") + mutedStyle.Render("   ◂/▸")
	}
	return prefix + labelStyle.Render("IdP") + "  " + value
}

func accessInputRow(label, value string, focused bool) string {
	prefix := "  "
	labelStyle := mutedStyle
	if focused {
		prefix = accessFocusMarker + " "
		labelStyle = brandStyle
	}
	return prefix + labelStyle.Render(label) + "  " + value
}

func accessButton(label string, focused bool) string {
	if focused {
		return activeButtonStyle.Render(label) + " "
	}
	return buttonStyle.Render(label) + " "
}

const accessFocusMarker = "›"

var (
	accessChipStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 2)
	accessChipActiveStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("16")).Background(lipgloss.Color("48")).Padding(0, 2)
)
