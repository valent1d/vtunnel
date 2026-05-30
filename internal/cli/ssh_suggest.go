package cli

import (
	"context"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sshSuggestResult is what the port-22 suggestion prompt returns.
type sshSuggestResult struct {
	useSSH    bool   // user chose browser SSH over a plain TCP tunnel
	allow     string // who may sign in (email or @domain), when useSSH
	remember  bool   // persist "don't ask again"
	cancelled bool   // user aborted entirely (Ctrl+C / esc on the choice)
}

type sshSuggestPhase int

const (
	suggestChoice sshSuggestPhase = iota
	suggestEmail
)

type sshSuggestModel struct {
	phase     sshSuggestPhase
	yes       bool
	remember  bool
	email     textinput.Model
	subdomain string
	result    sshSuggestResult
	done      bool
}

func newSSHSuggestModel(subdomain string) sshSuggestModel {
	email := textinput.New()
	email.Placeholder = "you@example.com or @example.com"
	email.CharLimit = 254
	email.SetWidth(40)
	return sshSuggestModel{yes: true, email: email, subdomain: subdomain}
}

func (m sshSuggestModel) Init() tea.Cmd { return nil }

func (m sshSuggestModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if m.phase == suggestEmail {
		switch key.String() {
		case "enter":
			value := strings.TrimSpace(m.email.Value())
			if value == "" {
				return m, nil
			}
			m.result = sshSuggestResult{useSSH: true, allow: value, remember: m.remember}
			m.done = true
			return m, tea.Quit
		case "esc":
			m.phase = suggestChoice
			m.email.Blur()
			return m, nil
		case "ctrl+c":
			m.result = sshSuggestResult{cancelled: true}
			m.done = true
			return m, tea.Quit
		}
		var cmd tea.Cmd
		m.email, cmd = m.email.Update(msg)
		return m, cmd
	}

	switch key.String() {
	case "left", "right", "tab", "h", "l", "up", "down", "j", "k":
		m.yes = !m.yes
		return m, nil
	case " ", "space", "x":
		m.remember = !m.remember
		return m, nil
	case "y":
		m.yes = true
		return m.confirm()
	case "n":
		m.yes = false
		return m.confirm()
	case "enter":
		return m.confirm()
	case "esc", "ctrl+c", "q":
		m.result = sshSuggestResult{cancelled: true}
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m sshSuggestModel) confirm() (tea.Model, tea.Cmd) {
	if m.yes {
		m.phase = suggestEmail
		m.email.Focus()
		return m, textinput.Blink
	}
	m.result = sshSuggestResult{useSSH: false, remember: m.remember}
	m.done = true
	return m, tea.Quit
}

func (m sshSuggestModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	var b strings.Builder
	b.WriteString(suggestBrand.Render("🔐 Port 22 looks like SSH") + "\n\n")
	b.WriteString("vtunnel ssh opens a " + suggestAccent.Render("browser terminal") + ": SSH in a browser tab with\n")
	b.WriteString("no client and no cloudflared, gated by a Cloudflare Access login.\n")
	b.WriteString(suggestMuted.Render("A plain TCP tunnel needs cloudflared running on every machine that connects.") + "\n\n")

	if m.phase == suggestEmail {
		b.WriteString(suggestAccent.Render("Who may sign in?") + suggestMuted.Render("  (an email like you@you.com, or @your-domain.com)") + "\n")
		b.WriteString("  " + m.email.View() + "\n\n")
		b.WriteString(suggestMuted.Render("enter confirm · esc back"))
		return tea.NewView(b.String())
	}

	b.WriteString(suggestRadio(m.yes, "Use vtunnel ssh (browser terminal)") + "\n")
	b.WriteString(suggestRadio(!m.yes, "Keep the plain TCP tunnel I asked for") + "\n\n")
	b.WriteString("  " + suggestCheckbox(m.remember) + " Don't ask me again\n\n")
	b.WriteString(suggestMuted.Render("y/n choose · space toggle remember · enter confirm · esc cancel"))
	return tea.NewView(b.String())
}

func suggestRadio(on bool, label string) string {
	if on {
		return suggestAccent.Render("  ● " + label)
	}
	return suggestMuted.Render("  ○ " + label)
}

func suggestCheckbox(on bool) string {
	if on {
		return suggestAccent.Render("[x]")
	}
	return suggestMuted.Render("[ ]")
}

// runSSHSuggestion shows the interactive prompt and returns the user's choice.
func runSSHSuggestion(ctx context.Context, subdomain string) (sshSuggestResult, error) {
	program := tea.NewProgram(newSSHSuggestModel(subdomain), tea.WithContext(ctx))
	final, err := program.Run()
	if err != nil {
		return sshSuggestResult{}, err
	}
	model, _ := final.(sshSuggestModel)
	return model.result, nil
}

// interactiveTerminal reports whether stdin and stdout are both a TTY, so we
// only show the suggestion prompt when a human can answer it.
func interactiveTerminal() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

var (
	suggestBrand  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	suggestAccent = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	suggestMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)
