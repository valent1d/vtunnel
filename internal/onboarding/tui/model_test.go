package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"vtunnel/internal/onboarding"
)

func TestRenderOnboardingDashboard(t *testing.T) {
	report := onboarding.Report{
		Phases: []onboarding.Phase{{
			Title:   "Local checks",
			Summary: "Inspect local dependencies.",
			Checks: []onboarding.Check{{
				Label:  "cloudflared",
				Status: onboarding.StatusOK,
				Detail: "/bin/cloudflared",
			}},
		}, {
			Title:   "Cloudflare auth",
			Summary: "Check Cloudflare access.",
		}},
		Actions: []onboarding.Action{{
			ID:          "start-daemon",
			Label:       "Start vtunnel daemon",
			Description: "Start the local proxy/API daemon.",
			Mutates:     true,
		}},
	}

	output := Render(Snapshot{Report: report, Width: 100})
	for _, want := range []string{"vtunnel", "onboarding", "Status", "Setup path", "Current step", "Actions", "Local checks", "cloudflared", "Start vtunnel daemon", "Continue", "enter select"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderBootScreen(t *testing.T) {
	output := Render(Snapshot{Booting: true, BootFrame: 2, Width: 100, Height: 28})
	for _, want := range []string{"Welcome to VTunnel", "Preparing your local Cloudflare Tunnel workspace", "Onboarding", "■"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderReadyScreen(t *testing.T) {
	report := onboarding.Report{Ready: true}
	output := Render(Snapshot{Report: report, Width: 96})
	for _, want := range []string{"vtunnel is ready.", "Commands", "vtunnel http 3000 dev", "Open the request dashboard", "vtunnel --help"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "OK Try") || strings.Contains(output, "OK Dashboard") {
		t.Fatalf("completion step should not render command examples as OK checks:\n%s", output)
	}
}

func TestRenderFitsWidth(t *testing.T) {
	report := onboarding.Report{
		Phases: []onboarding.Phase{{
			Title:   "Local cloudflared config",
			Summary: "Preserve existing ingress and add vtunnel wildcard rules.",
			Checks: []onboarding.Check{{
				Label:  "wildcard ingress",
				Status: onboarding.StatusAction,
				Detail: "*.example.test points to http://localhost:9999, expected http://127.0.0.1:8787",
			}},
		}},
		Actions: []onboarding.Action{{
			ID:          "write-cloudflared",
			Label:       "Write cloudflared config",
			Description: "Backup then apply local ingress changes.",
			Mutates:     true,
		}},
	}
	width := 92
	output := Render(Snapshot{Report: report, Width: width})
	for _, line := range strings.Split(output, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line width = %d, want <= %d:\n%s\n\nfull output:\n%s", got, width, line, output)
		}
	}
}

func TestRenderActionHintWraps(t *testing.T) {
	output := renderStepActions(nil, 0, false, 32)
	if strings.Contains(output, "the...") {
		t.Fatalf("action hint should wrap instead of truncate:\n%s", output)
	}
	for _, want := range []string{"Press enter or tab", "to the next step."} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain wrapped hint %q:\n%s", want, output)
		}
	}
}

func TestModelContinueActionAdvancesStep(t *testing.T) {
	engine := &fakeEngine{
		report: onboarding.Report{
			Steps: []onboarding.Step{
				{
					Title: "Domain selection",
					Actions: []onboarding.Action{{
						ID:          "add-domain",
						Label:       "Add a domain",
						Description: "Add another domain to vtunnel.",
						InputPrompt: "Domain",
					}},
				},
				{Title: "Tunnel"},
			},
		},
	}
	model := NewModel(engine)
	model.report = engine.report
	model.actionIndex = 1

	updated, cmd := model.Update(key("enter"))
	if cmd != nil {
		t.Fatal("continue should not execute a command")
	}
	model = updated.(Model)
	if model.stepIndex != 1 {
		t.Fatalf("stepIndex = %d, want 1", model.stepIndex)
	}
	if model.actionIndex != 0 {
		t.Fatalf("actionIndex = %d, want 0", model.actionIndex)
	}
}

func TestDomainManagerRendersDomains(t *testing.T) {
	report := onboarding.Report{
		Steps: []onboarding.Step{{
			ID:    onboarding.StepDomains,
			Title: "Domain selection",
			Checks: []onboarding.Check{
				{Label: "selected domains", Status: onboarding.StatusOK, Detail: "one.test, two.test"},
				{Label: "default domain", Status: onboarding.StatusOK, Detail: "two.test"},
			},
		}},
	}
	output := Render(Snapshot{Report: report, DomainManager: true, Width: 100})
	for _, want := range []string{"Manage domains", "one.test", "two.test", "default", "r rename", "x remove"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestModelManageDomainsActionOpensPopup(t *testing.T) {
	engine := &fakeEngine{
		report: onboarding.Report{
			Steps: []onboarding.Step{{
				ID:    onboarding.StepDomains,
				Title: "Domain selection",
				Checks: []onboarding.Check{
					{Label: "selected domains", Status: onboarding.StatusOK, Detail: "one.test"},
					{Label: "default domain", Status: onboarding.StatusOK, Detail: "one.test"},
				},
				Actions: []onboarding.Action{{
					ID:          "manage-domains",
					Label:       "Manage domains",
					Description: "Open the domain manager.",
					Command:     "manage domains",
				}},
			}},
		},
	}
	model := NewModel(engine)
	model.report = engine.report

	updated, cmd := model.Update(key("enter"))
	if cmd != nil {
		t.Fatal("manage domains should not execute a command")
	}
	model = updated.(Model)
	if !model.domainManager {
		t.Fatal("expected domain manager to open")
	}
}

func TestTunnelManagerRendersTunnels(t *testing.T) {
	report := onboarding.Report{
		Steps: []onboarding.Step{{
			ID:    onboarding.StepTunnel,
			Title: "Tunnel",
			Checks: []onboarding.Check{
				{Label: "available tunnel", Status: onboarding.StatusOK, Detail: "webapp (11111111-1111-1111-1111-111111111111)"},
				{Label: "available tunnel", Status: onboarding.StatusOK, Detail: "api (22222222-2222-2222-2222-222222222222)"},
				{Label: "configured tunnel exists", Status: onboarding.StatusOK, Detail: "api (22222222-2222-2222-2222-222222222222)"},
			},
		}},
	}
	output := Render(Snapshot{Report: report, TunnelManager: true, Width: 110})
	for _, want := range []string{"Manage tunnels", "webapp", "api", "selected", "a create", "x delete"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestModelManageTunnelsActionOpensPopup(t *testing.T) {
	engine := &fakeEngine{
		report: onboarding.Report{
			Steps: []onboarding.Step{{
				ID:    onboarding.StepTunnel,
				Title: "Tunnel",
				Checks: []onboarding.Check{
					{Label: "available tunnel", Status: onboarding.StatusOK, Detail: "webapp (11111111-1111-1111-1111-111111111111)"},
				},
				Actions: []onboarding.Action{{
					ID:          "manage-tunnels",
					Label:       "Manage tunnels",
					Description: "Open the tunnel manager.",
					Command:     "manage tunnels",
				}},
			}},
		},
	}
	model := NewModel(engine)
	model.report = engine.report

	updated, cmd := model.Update(key("enter"))
	if cmd != nil {
		t.Fatal("manage tunnels should not execute a command")
	}
	model = updated.(Model)
	if !model.tunnelManager {
		t.Fatal("expected tunnel manager to open")
	}
}

func TestModelConfirmsMutatingAction(t *testing.T) {
	engine := &fakeEngine{
		report: onboarding.Report{
			Phases: []onboarding.Phase{{Title: "Runtime"}},
			Actions: []onboarding.Action{{
				ID:          "start-daemon",
				Label:       "Start vtunnel daemon",
				Description: "Start daemon.",
				Mutates:     true,
			}},
		},
	}
	model := NewModel(engine)
	model.report = engine.report

	updated, cmd := model.Update(key("enter"))
	if cmd != nil {
		t.Fatal("enter should only enter confirmation mode")
	}
	model = updated.(Model)
	if !model.confirming {
		t.Fatal("expected confirmation mode")
	}

	updated, cmd = model.Update(key("y"))
	if cmd == nil {
		t.Fatal("confirm should execute action")
	}
	msg := cmd()
	updated, _ = updated.Update(msg)
	model = updated.(Model)
	if engine.executed != "start-daemon" {
		t.Fatalf("executed = %q", engine.executed)
	}
	if !strings.Contains(model.notice, "started") {
		t.Fatalf("notice = %q", model.notice)
	}
}

func TestFinishOnLastStepQuitsAndRecaps(t *testing.T) {
	report := onboarding.Report{
		Steps: []onboarding.Step{
			{Title: "Runtime", Checks: []onboarding.Check{{Label: "daemon", Status: onboarding.StatusOK, Detail: "running"}}},
			{ID: onboarding.StepCompletion, Title: "Completion", Summary: "vtunnel is ready.", Commands: onboarding.CompletionCommands("")},
		},
	}
	model := NewModel(&fakeEngine{report: report})
	model.report = report
	model.stepIndex = 1 // last step: the "Finish" action

	updated, cmd := model.Update(key("enter"))
	model = updated.(Model)

	if !model.finished {
		t.Fatal("expected model.finished after pressing enter on the last step")
	}
	if cmd == nil {
		t.Fatal("expected a quit command from Finish")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", cmd())
	}

	out := model.farewell()
	for _, want := range []string{"onboarding complete", "Excited to see you soon", "ok 2", "warning 0", "to fix 0", "vtunnel http 3000 dev"} {
		if !strings.Contains(out, want) {
			t.Fatalf("farewell missing %q:\n%s", want, out)
		}
	}
}

func TestConfirmButtonsRespectFocus(t *testing.T) {
	engine := &fakeEngine{
		report: onboarding.Report{
			Phases:  []onboarding.Phase{{Title: "Runtime"}},
			Actions: []onboarding.Action{{ID: "start-daemon", Label: "Start vtunnel daemon", Mutates: true}},
		},
	}
	model := NewModel(engine)
	model.report = engine.report

	// enter opens the confirmation with Confirm focused (non-destructive default).
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	if !model.confirming || model.confirmCancel {
		t.Fatalf("want confirming with Confirm focused, got confirming=%v cancel=%v", model.confirming, model.confirmCancel)
	}

	// "l" toggles focus to Cancel.
	updated, _ = model.Update(key("l"))
	model = updated.(Model)
	if !model.confirmCancel {
		t.Fatal("want Cancel focused after toggle")
	}

	// enter on Cancel closes without executing.
	updated, cmd := model.Update(key("enter"))
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("enter on Cancel must not execute")
	}
	if model.confirming {
		t.Fatal("want confirmation closed")
	}
	if engine.executed != "" {
		t.Fatalf("nothing should have executed, got %q", engine.executed)
	}
}

func key(value string) tea.KeyPressMsg {
	switch value {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		return tea.KeyPressMsg{Code: []rune(value)[0], Text: value}
	}
}

type fakeEngine struct {
	report   onboarding.Report
	executed string
	input    string
}

func (engine *fakeEngine) Report(context.Context) onboarding.Report {
	return engine.report
}

func (engine *fakeEngine) Execute(_ context.Context, actionID string, input string) (string, error) {
	engine.executed = actionID
	engine.input = input
	return "started", nil
}
