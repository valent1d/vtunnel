package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
		}},
		Actions: []onboarding.Action{{
			ID:          "start-daemon",
			Label:       "Start vtunnel daemon",
			Description: "Start the local proxy/API daemon.",
			Mutates:     true,
		}},
	}

	output := Render(Snapshot{Report: report, Width: 100})
	for _, want := range []string{"vtunnel", "onboarding", "Status", "Setup", "Details", "Actions", "Local checks", "cloudflared", "Start vtunnel daemon", "enter select"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderReadyScreen(t *testing.T) {
	report := onboarding.Report{Ready: true}
	output := Render(Snapshot{Report: report, Width: 96})
	for _, want := range []string{"vtunnel is ready.", "vtunnel http 3000 dev", "vtunnel"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
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

func key(value string) tea.KeyMsg {
	if value == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
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
