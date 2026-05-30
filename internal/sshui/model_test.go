package sshui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeManager struct {
	endpoints []Endpoint
	added     string
	removed   string
}

func (f *fakeManager) List(context.Context) ([]Endpoint, error) { return f.endpoints, nil }
func (f *fakeManager) Add(_ context.Context, sub, target, allow, idp string) error {
	f.added = strings.Join([]string{sub, target, allow, idp}, "|")
	return nil
}
func (f *fakeManager) Remove(_ context.Context, host string) error {
	f.removed = host
	return nil
}

func press(v string) tea.KeyPressMsg {
	switch v {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		return tea.KeyPressMsg{Code: []rune(v)[0], Text: v}
	}
}

// walkAndSubmit presses enter to step through the form to the last field, then
// once more to submit, returning the resulting model and the submit command.
func walkAndSubmit(m Model) (Model, tea.Cmd) {
	for i := 0; i < fieldCount-1; i++ {
		updated, _ := m.Update(press("enter"))
		m = updated.(Model)
	}
	updated, cmd := m.Update(press("enter"))
	return updated.(Model), cmd
}

func TestSSHCreateCallsAdd(t *testing.T) {
	fake := &fakeManager{}
	model := NewModel(fake)

	updated, _ := model.Update(press("n"))
	model = updated.(Model)
	if model.mode != modeCreate {
		t.Fatalf("mode = %v, want create", model.mode)
	}
	model.inputs[fSub].SetValue("box")
	model.inputs[fTarget].SetValue("localhost:22")
	model.inputs[fAllow].SetValue("me@example.com")

	model, cmd := walkAndSubmit(model)
	if cmd == nil {
		t.Fatal("expected an add command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	if fake.added != "box|localhost:22|me@example.com|" {
		t.Fatalf("added = %q, want box|localhost:22|me@example.com|", fake.added)
	}
	if model.mode != modeDashboard {
		t.Fatalf("should return to dashboard, mode = %v", model.mode)
	}
}

func TestSSHCreateRequiresAllow(t *testing.T) {
	fake := &fakeManager{}
	model := NewModel(fake)

	updated, _ := model.Update(press("n"))
	model = updated.(Model)
	model.inputs[fSub].SetValue("box")
	model.inputs[fAllow].SetValue("")

	model, cmd := walkAndSubmit(model)
	if cmd != nil {
		t.Fatal("should not submit without an allow value")
	}
	if model.err == "" {
		t.Fatal("expected a validation error")
	}
	if fake.added != "" {
		t.Fatalf("Add should not run, got %q", fake.added)
	}
}

func TestSSHRemoveConfirmCallsRemove(t *testing.T) {
	fake := &fakeManager{}
	model := NewModel(fake)
	model.endpoints = []Endpoint{{Hostname: "box.example.test", Target: "localhost:22", URL: "https://box.example.test"}}

	updated, _ := model.Update(press("x"))
	model = updated.(Model)
	if model.mode != modeConfirmRemove {
		t.Fatalf("mode = %v, want confirm", model.mode)
	}
	updated, cmd := model.Update(press("y"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a remove command")
	}
	model.Update(cmd())
	if fake.removed != "box.example.test" {
		t.Fatalf("removed = %q", fake.removed)
	}
}

func TestSSHListRenders(t *testing.T) {
	fake := &fakeManager{}
	model := NewModel(fake)
	model.endpoints = []Endpoint{{Hostname: "box.example.test", Target: "localhost:22", URL: "https://box.example.test"}}

	out := ansi.Strip(model.View().Content)
	for _, want := range []string{"vtunnel", "ssh", "Browser SSH", "box.example.test", "https://box.example.test", "ssh://localhost:22"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}
