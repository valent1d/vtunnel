package tcpui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeManager struct {
	tunnels []Tunnel
	added   string
	removed string
}

func (f *fakeManager) List(context.Context) ([]Tunnel, error) { return f.tunnels, nil }
func (f *fakeManager) Add(_ context.Context, target, sub string) error {
	f.added = target + "|" + sub
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

func TestTCPCreateCallsAdd(t *testing.T) {
	fake := &fakeManager{}
	model := NewModel(fake)

	updated, _ := model.Update(press("n"))
	model = updated.(Model)
	if model.mode != modeCreate {
		t.Fatalf("mode = %v, want create", model.mode)
	}
	model.targetInput.SetValue("3306")
	updated, _ = model.Update(press("enter")) // step 0 → 1
	model = updated.(Model)
	model.subInput.SetValue("db")

	updated, cmd := model.Update(press("enter")) // create
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected an add command")
	}
	updated, _ = model.Update(cmd()) // opMsg
	model = updated.(Model)

	if fake.added != "3306|db" {
		t.Fatalf("added = %q, want 3306|db", fake.added)
	}
	if model.mode != modeDashboard {
		t.Fatalf("should return to dashboard, mode = %v", model.mode)
	}
}

func TestTCPRemoveConfirmCallsRemove(t *testing.T) {
	fake := &fakeManager{}
	model := NewModel(fake)
	model.tunnels = []Tunnel{{Hostname: "db.example.test", Target: "127.0.0.1:3306", ConnectCmd: "vtunnel tcp connect db.example.test"}}

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
	model.Update(cmd()) // cmd() runs Remove and returns opMsg
	if fake.removed != "db.example.test" {
		t.Fatalf("removed = %q", fake.removed)
	}
}

func TestTCPListRenders(t *testing.T) {
	fake := &fakeManager{}
	model := NewModel(fake)
	model.tunnels = []Tunnel{{Hostname: "db.example.test", Target: "127.0.0.1:3306", ConnectCmd: "vtunnel tcp connect db.example.test"}}

	out := ansi.Strip(model.View().Content)
	for _, want := range []string{"vtunnel tcp", "TCP tunnels", "db.example.test", "tcp://127.0.0.1:3306", "vtunnel tcp connect"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}
