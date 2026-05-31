package mcpsetupui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeManager struct {
	clients []Client
	added   string
	removed string
	tools   []string
}

func (f *fakeManager) List(context.Context) ([]Client, error) { return f.clients, nil }
func (f *fakeManager) Add(_ context.Context, id string) error { f.added = id; return nil }
func (f *fakeManager) Remove(_ context.Context, id string) error {
	f.removed = id
	return nil
}
func (f *fakeManager) SelfCheck(context.Context) ([]string, error) { return f.tools, nil }
func (f *fakeManager) Scope() string                               { return "user" }

func press(v string) tea.KeyPressMsg {
	switch v {
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		return tea.KeyPressMsg{Code: []rune(v)[0], Text: v}
	}
}

func sampleClients() []Client {
	return []Client{
		{ID: "claude", Name: "Claude Code", Installed: true, Configured: true, Location: "claude CLI", ManualCommand: "claude mcp add --scope user vtunnel -- vtunnel mcp serve"},
		{ID: "cursor", Name: "Cursor", Installed: true, Configured: false, Location: "~/.cursor/mcp.json", ManualPath: "~/.cursor/mcp.json", ManualSnippet: "{\n  \"mcpServers\": {}\n}"},
	}
}

func TestAddCallsManager(t *testing.T) {
	fake := &fakeManager{clients: sampleClients()}
	model := NewModel(fake)
	model.clients = fake.clients
	model.selected = 1 // Cursor

	updated, cmd := model.Update(press("a"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected an add command")
	}
	model.Update(cmd()) // runs Add, returns opMsg
	if fake.added != "cursor" {
		t.Fatalf("added = %q, want cursor", fake.added)
	}
}

func TestRemoveCallsManager(t *testing.T) {
	fake := &fakeManager{clients: sampleClients()}
	model := NewModel(fake)
	model.clients = fake.clients
	model.selected = 0 // Claude

	updated, cmd := model.Update(press("x"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a remove command")
	}
	model.Update(cmd())
	if fake.removed != "claude" {
		t.Fatalf("removed = %q, want claude", fake.removed)
	}
}

func TestSelfCheckShowsToolCount(t *testing.T) {
	fake := &fakeManager{clients: sampleClients(), tools: []string{"a", "b", "c"}}
	model := NewModel(fake)
	model.clients = fake.clients

	updated, cmd := model.Update(press("c"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a self-check command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !strings.Contains(model.check, "3 tools") {
		t.Fatalf("check line = %q, want it to mention 3 tools", model.check)
	}
}

func TestRenderShowsClientsAndManual(t *testing.T) {
	fake := &fakeManager{clients: sampleClients()}
	model := NewModel(fake)
	model.clients = fake.clients
	model.selected = 0

	out := ansi.Strip(model.View().Content)
	for _, want := range []string{"vtunnel", "mcp", "Claude Code", "Cursor", "Manual setup", "claude mcp add"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}
