package httpui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"vtunnel/internal/config"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

func TestRenderDashboard(t *testing.T) {
	route := routes.Route{
		Hostname:  "web.example.test",
		Target:    "http://127.0.0.1:3000",
		CreatedAt: time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}
	output := ansi.Strip(Render(Snapshot{
		Routes:        []routes.Route{route},
		SelectedRoute: route,
		Logs: []requestlog.Entry{{
			Method:   "GET",
			Status:   200,
			Path:     "/api/login",
			Duration: 12 * time.Millisecond,
		}},
		Width: 110,
	}))
	for _, want := range []string{"vtunnel http", "Tunnels", "Details", "Logs", "Metrics", "web", "https://web.example.test", "/api/login"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderFitsWidth(t *testing.T) {
	route := routes.Route{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}
	width := 96
	output := Render(Snapshot{
		Routes:        []routes.Route{route},
		SelectedRoute: route,
		Logs: []requestlog.Entry{{
			Method:   "POST",
			Status:   500,
			Path:     "/a/very/long/path/that/should/not/destroy/the/terminal/layout",
			Duration: time.Second,
		}},
		Width: width,
	})
	for _, line := range strings.Split(output, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line width = %d, want <= %d:\n%s\n\nfull output:\n%s", got, width, line, output)
		}
	}
}

func TestModelCreateRoute(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	client := &fakeClient{}
	model := NewModel(client, cfg, "")
	model.mode = modeCreate
	model.createStep = 2
	model.portInput.SetValue("3000")
	model.subInput.SetValue("web")
	model.domainInput.SetValue("example.test")

	updated, cmd := model.Update(key("enter"))
	if cmd == nil {
		t.Fatal("expected create command")
	}
	msg := cmd()
	updated, _ = updated.Update(msg)
	model = updated.(Model)

	if len(client.routes) != 1 {
		t.Fatalf("routes = %#v", client.routes)
	}
	if client.routes[0].Hostname != "web.example.test" {
		t.Fatalf("hostname = %q", client.routes[0].Hostname)
	}
	if model.selectedHostname != "web.example.test" {
		t.Fatalf("selectedHostname = %q", model.selectedHostname)
	}
}

func TestModelScrollsLogsAndOpensRequestDetail(t *testing.T) {
	cfg := config.Default()
	client := &fakeClient{
		routes: []routes.Route{{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}},
	}
	for index := 0; index < 20; index++ {
		client.logs = append(client.logs, requestlog.Entry{
			ID:       uint64(index + 1),
			Hostname: "web.example.test",
			Method:   "GET",
			Status:   200,
			Path:     fmt.Sprintf("/%d", index),
		})
	}
	model := NewModel(client, cfg, "web.example.test")
	model.routes = client.routes
	model.syncSelection()
	model.logs = client.logs
	model.focus = focusLogs

	updated, _ := model.Update(key("down"))
	model = updated.(Model)
	if model.logSelected != 1 {
		t.Fatalf("logSelected = %d", model.logSelected)
	}

	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	if model.mode != modeRequestDetail {
		t.Fatalf("mode = %v, want request detail", model.mode)
	}
	output := ansi.Strip(model.View().Content)
	if !strings.Contains(output, "Request detail") {
		t.Fatalf("output does not contain request detail:\n%s", output)
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

type fakeClient struct {
	routes []routes.Route
	logs   []requestlog.Entry
}

func (client *fakeClient) ListRoutes(context.Context) ([]routes.Route, error) {
	return client.routes, nil
}

func (client *fakeClient) AddRoute(_ context.Context, route routes.Route) error {
	client.routes = append(client.routes, route)
	return nil
}

func (client *fakeClient) DeleteRoute(_ context.Context, hostname string) error {
	next := client.routes[:0]
	for _, route := range client.routes {
		if route.Hostname != hostname {
			next = append(next, route)
		}
	}
	client.routes = next
	return nil
}

func (client *fakeClient) ListLogs(context.Context, requestlog.Filter) ([]requestlog.Entry, error) {
	return client.logs, nil
}
