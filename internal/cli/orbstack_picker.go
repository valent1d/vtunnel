package cli

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/spf13/cobra"

	"vtunnel/internal/config"
	"vtunnel/internal/orbstack"
)

// runOrbstackPicker lists HTTP-exposable containers, lets the user pick one and
// name its subdomain, then exposes it and opens the dashboard.
func runOrbstackPicker(cmd *cobra.Command, configPath *string) error {
	all, err := newOrbstackClient().List(cmd.Context())
	if err != nil {
		return err
	}
	containers := make([]orbstack.Container, 0, len(all))
	for _, container := range all {
		if container.HTTP {
			containers = append(containers, container)
		}
	}
	if len(containers) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No HTTP-exposable OrbStack containers found.")
		fmt.Fprintln(cmd.OutOrStdout(), "Run `vtunnel orbstack list` to see all containers, or use --target.")
		return nil
	}

	cfg, _ := config.Load(*configPath)
	model := newOrbstackPickerModel(containers, cfg.DefaultDomain)
	program := tea.NewProgram(model, tea.WithContext(cmd.Context()))
	final, err := program.Run()
	if err != nil {
		return err
	}

	picker, ok := final.(orbstackPickerModel)
	if !ok || !picker.confirmed {
		fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
		return nil
	}
	return exposeOrbstackContainer(cmd, configPath, picker.selected(), picker.sub.Value(), "", "", false)
}

type orbstackPickerModel struct {
	containers []orbstack.Container
	domain     string
	cursor     int
	sub        textinput.Model
	width      int
	confirmed  bool
}

func newOrbstackPickerModel(containers []orbstack.Container, domain string) orbstackPickerModel {
	sub := textinput.New()
	sub.CharLimit = 63
	sub.SetWidth(24)
	sub.SetValue(containers[0].DefaultSubdomain())
	sub.Focus()

	return orbstackPickerModel{
		containers: containers,
		domain:     strings.TrimSpace(domain),
		sub:        sub,
		width:      80,
	}
}

func (m orbstackPickerModel) selected() orbstack.Container {
	return m.containers[m.cursor]
}

func (m orbstackPickerModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m orbstackPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "ctrl+p":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n":
			m.move(1)
			return m, nil
		case "enter":
			if strings.TrimSpace(m.sub.Value()) == "" {
				m.sub.SetValue(m.selected().DefaultSubdomain())
			}
			m.confirmed = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.sub, cmd = m.sub.Update(msg)
	return m, cmd
}

// move changes the highlighted container and resets the subdomain to that
// container's default, since the proposed name is container-specific.
func (m *orbstackPickerModel) move(delta int) {
	next := m.cursor + delta
	if next < 0 || next >= len(m.containers) {
		return
	}
	m.cursor = next
	m.sub.SetValue(m.containers[m.cursor].DefaultSubdomain())
}

func (m orbstackPickerModel) View() tea.View {
	var b strings.Builder
	b.WriteString(pickerTitleStyle.Render("Expose an OrbStack container") + "\n\n")

	for i, container := range m.containers {
		prefix := "  "
		nameStyle := pickerRowStyle
		if i == m.cursor {
			prefix = pickerAccentStyle.Render("› ")
			nameStyle = pickerSelectedStyle
		}
		detail := container.OrbDomain
		if len(container.CustomDomains) > 0 {
			detail += "  " + pickerMutedStyle.Render("("+strings.Join(container.CustomDomains, ", ")+")")
		}
		b.WriteString(fmt.Sprintf("%s%-22s %s\n", prefix, nameStyle.Render(container.Name), detail))
	}

	preview := m.sub.Value()
	if m.domain != "" {
		preview = m.sub.Value() + "." + m.domain
	}
	b.WriteString("\n")
	b.WriteString(pickerMutedStyle.Render("Subdomain") + "  " + m.sub.View() + "\n")
	b.WriteString(pickerMutedStyle.Render("Public URL") + " " + pickerAccentStyle.Render("https://"+preview) + "\n\n")
	b.WriteString(pickerMutedStyle.Render("↑/↓ choose · type to rename · enter expose · esc cancel"))
	return tea.View{AltScreen: true, Content: pickerPageStyle.Render(b.String())}
}

var (
	pickerPageStyle     = lipgloss.NewStyle().Padding(1, 2)
	pickerTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	pickerAccentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("48"))
	pickerSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	pickerRowStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("151"))
	pickerMutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)
