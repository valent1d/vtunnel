package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
		Width:  110,
		Height: 40,
	}))
	for _, want := range []string{"vtunnel http", "Tunnels", "Overview", "Traffic", "Cloudflare edge", "Requests", "web", "https://web.example.test", "/api/login"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderShowsOrbStackBadgeAndDetail(t *testing.T) {
	route := routes.Route{
		Hostname:  "doli23.example.test",
		Target:    "http://dolibarr-v23.orb.local",
		CreatedAt: time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
		Orbstack: &routes.OrbstackInfo{
			Container:     "dolibarr-v23",
			Image:         "dolibarr/dolibarr:23",
			OrbDomain:     "dolibarr-v23.orb.local",
			CustomDomains: []string{"doli23.local"},
		},
	}
	output := ansi.Strip(Render(Snapshot{
		Routes:        []routes.Route{route},
		SelectedRoute: route,
		Width:         110,
		Height:        40,
	}))
	for _, want := range []string{orbstackBadge, "OrbStack", "dolibarr-v23", "dolibarr/dolibarr:23", "doli23.local"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderShowsAccessBadgeAndDetail(t *testing.T) {
	route := routes.Route{
		Hostname: "doli23.example.test",
		Target:   "http://127.0.0.1:8080",
		Access: &routes.AccessInfo{
			AppID: "app-1",
			Mode:  "sso",
			IdP:   "VLTN Connect",
			Allow: []string{"@progiseize.com"},
		},
	}
	output := ansi.Strip(Render(Snapshot{
		Routes:        []routes.Route{route},
		SelectedRoute: route,
		Width:         110,
		Height:        40,
	}))
	for _, want := range []string{accessBadge, "Protected", "sso · VLTN Connect", "@progiseize.com"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRenderNoAccessBlockForPublicRoute(t *testing.T) {
	route := routes.Route{Hostname: "demo.example.test", Target: "http://127.0.0.1:3000"}
	output := ansi.Strip(Render(Snapshot{Routes: []routes.Route{route}, SelectedRoute: route, Width: 110, Height: 40}))
	if strings.Contains(output, "Protected") {
		t.Fatalf("public route should not render a Protected block:\n%s", output)
	}
}

func TestRenderNoOrbStackBlockForPlainRoute(t *testing.T) {
	route := routes.Route{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}
	output := ansi.Strip(Render(Snapshot{
		Routes:        []routes.Route{route},
		SelectedRoute: route,
		Width:         110,
		Height:        40,
	}))
	if strings.Contains(output, "OrbStack") {
		t.Fatalf("plain route should not render an OrbStack block:\n%s", output)
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
	model := NewModel(client, cfg, "", nil)
	model.mode = modeCreate
	model.createStep = 3 // last step (protect selector) → enter creates
	model.portInput.SetValue("3000")
	model.subInput.SetValue("web")
	model.domainInput.SetValue("example.test")

	updated, cmd := model.Update(press("enter"))
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
	model := NewModel(client, cfg, "web.example.test", nil)
	model.routes = client.routes
	model.syncSelection()
	model.logs = client.logs
	model.focus = focusLogs

	updated, _ := model.Update(press("down"))
	model = updated.(Model)
	if model.logCursor != 1 {
		t.Fatalf("logCursor = %d, want 1", model.logCursor)
	}

	updated, _ = model.Update(press("enter"))
	model = updated.(Model)
	if model.mode != modeRequestDetail {
		t.Fatalf("mode = %v, want request detail", model.mode)
	}
	output := ansi.Strip(model.View().Content)
	if !strings.Contains(output, "Request detail") {
		t.Fatalf("output does not contain request detail:\n%s", output)
	}
}

func TestModelFetchesExchangeDetailAndReplays(t *testing.T) {
	cfg := config.Default()
	client := &fakeClient{
		routes: []routes.Route{{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}},
		logs:   []requestlog.Entry{{ID: 7, Hostname: "web.example.test", Method: "POST", Status: 200, Path: "/submit"}},
		exchanges: map[uint64]requestlog.Exchange{
			7: {
				ID: 7, Method: "POST", Path: "/submit", Status: 200,
				RequestHeaders:  http.Header{"X-Custom": {"abc"}},
				RequestBody:     []byte("ping"),
				ResponseHeaders: http.Header{"X-Upstream": {"yes"}},
				ResponseBody:    []byte("pong"),
			},
		},
	}
	model := NewModel(client, cfg, "web.example.test", nil)
	model.routes = client.routes
	model.syncSelection()
	model.logs = client.logs
	model.focus = focusLogs

	// Enter opens the detail and asks for the captured exchange.
	updated, cmd := model.Update(press("enter"))
	model = updated.(Model)
	if model.mode != modeRequestDetail {
		t.Fatalf("mode = %v, want request detail", model.mode)
	}
	if cmd == nil {
		t.Fatal("expected an exchange fetch command")
	}
	updated, _ = model.Update(cmd()) // exchangeMsg
	model = updated.(Model)
	if model.detail == nil || model.detail.ID != 7 {
		t.Fatalf("detail = %+v", model.detail)
	}

	out := ansi.Strip(model.View().Content)
	for _, want := range []string{"X-Custom", "ping", "X-Upstream", "pong", "r replay"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail view missing %q:\n%s", want, out)
		}
	}

	// r replays the captured request.
	updated, cmd = model.Update(press("r"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a replay command")
	}
	updated, _ = model.Update(cmd()) // replayMsg
	model = updated.(Model)
	if client.replayedID != 7 {
		t.Fatalf("replayedID = %d, want 7", client.replayedID)
	}
	if !strings.Contains(model.notice, "Replayed request #7") {
		t.Fatalf("notice = %q", model.notice)
	}
}

func TestLogScrollMovesPastVisibleWindow(t *testing.T) {
	cfg := config.Default()
	client := &fakeClient{routes: []routes.Route{{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}}}
	for i := 0; i < 50; i++ {
		client.logs = append(client.logs, requestlog.Entry{
			ID: uint64(i + 1), Hostname: "web.example.test", Method: "GET", Status: 200, Path: fmt.Sprintf("/p%d", i),
		})
	}
	model := NewModel(client, cfg, "web.example.test", nil)
	model.routes = client.routes
	model.syncSelection()
	model.logs = client.logs
	model.focus = focusLogs
	model.logCursor = 0
	model.logFollow = false

	// Press down well past one visible window — the old code clamped the cursor
	// inside a fixed 12-row window and never scrolled.
	for i := 0; i < 30; i++ {
		updated, _ := model.Update(press("down"))
		model = updated.(Model)
	}
	if model.logCursor != 30 {
		t.Fatalf("logCursor = %d after 30 downs, want 30 (scroll must advance past the window)", model.logCursor)
	}
	if out := ansi.Strip(model.View().Content); !strings.Contains(out, "/p30") {
		t.Fatalf("scrolled window should show the cursor entry /p30:\n%s", out)
	}
}

func press(value string) tea.KeyPressMsg {
	switch value {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		return tea.KeyPressMsg{Code: []rune(value)[0], Text: value}
	}
}

type fakeAccess struct {
	idps        []string
	protected   map[string]string
	lastAllow   string
	lastIdP     string
	unprotected []string
	paused      []string
	resumed     []string
}

func (f *fakeAccess) IdentityProviders(context.Context) []string { return f.idps }

func (f *fakeAccess) Protect(_ context.Context, hostname, mode, allow, idp string) error {
	if f.protected == nil {
		f.protected = map[string]string{}
	}
	f.protected[hostname] = mode
	f.lastAllow = allow
	f.lastIdP = idp
	return nil
}
func (f *fakeAccess) Unprotect(_ context.Context, hostname string) error {
	f.unprotected = append(f.unprotected, hostname)
	return nil
}
func (f *fakeAccess) Pause(_ context.Context, hostname string) error {
	f.paused = append(f.paused, hostname)
	return nil
}
func (f *fakeAccess) Resume(_ context.Context, hostname string) error {
	f.resumed = append(f.resumed, hostname)
	return nil
}

func TestAccessPanelProtectsSelectedRoute(t *testing.T) {
	cfg := config.Default()
	client := &fakeClient{routes: []routes.Route{{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}}}
	access := &fakeAccess{}
	model := NewModel(client, cfg, "web.example.test", access)
	model.routes = client.routes
	model.syncSelection()
	model.focus = focusTunnels

	// Open the Access panel.
	updated, _ := model.Update(press("a"))
	model = updated.(Model)
	if model.mode != modeAccess {
		t.Fatalf("mode = %v, want access panel", model.mode)
	}
	// No IdPs configured → modes are [public, otp]. Cycle Public → OTP.
	updated, _ = model.Update(press("right"))
	model = updated.(Model)
	if model.modeKey() != "otp" {
		t.Fatalf("modeKey = %q, want otp", model.modeKey())
	}
	updated, _ = model.Update(press("down")) // focus Allow
	model = updated.(Model)
	model.allowInput.SetValue("@progiseize.com")

	// Apply.
	updated, cmd := model.Update(press("enter"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected an apply command")
	}
	updated, _ = model.Update(cmd()) // accessResultMsg
	model = updated.(Model)

	if access.protected["web.example.test"] != "otp" {
		t.Fatalf("protected = %v", access.protected)
	}
	if access.lastAllow != "@progiseize.com" {
		t.Fatalf("allow = %q", access.lastAllow)
	}
	if model.mode != modeDashboard {
		t.Fatalf("panel should close after apply, mode = %v", model.mode)
	}
}

func TestAccessPanelProtectsViaSSOWithoutEmail(t *testing.T) {
	cfg := config.Default()
	client := &fakeClient{routes: []routes.Route{{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}}}
	access := &fakeAccess{idps: []string{"VLTN Connect"}}
	model := NewModel(client, cfg, "web.example.test", access)
	model.routes = client.routes
	model.syncSelection()
	model.focus = focusTunnels
	model.accessIdPs = []string{"VLTN Connect"} // simulate the fetched IdP list

	updated, _ := model.Update(press("a"))
	model = updated.(Model)
	// Modes are [public, sso, otp]; one step right selects SSO.
	updated, _ = model.Update(press("right"))
	model = updated.(Model)
	if model.modeKey() != "sso" {
		t.Fatalf("modeKey = %q, want sso", model.modeKey())
	}

	// Apply with no allow — SSO must not require an email.
	updated, cmd := model.Update(press("enter"))
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected an apply command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	if access.protected["web.example.test"] != "sso" {
		t.Fatalf("protected = %v", access.protected)
	}
	if access.lastIdP != "VLTN Connect" {
		t.Fatalf("idp = %q, want VLTN Connect", access.lastIdP)
	}
	if access.lastAllow != "" {
		t.Fatalf("SSO should not require an allow, got %q", access.lastAllow)
	}
}

type fakeClient struct {
	routes     []routes.Route
	logs       []requestlog.Entry
	exchanges  map[uint64]requestlog.Exchange
	replayedID uint64
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

func (client *fakeClient) GetExchange(_ context.Context, id uint64) (requestlog.Exchange, error) {
	if exchange, ok := client.exchanges[id]; ok {
		return exchange, nil
	}
	return requestlog.Exchange{}, errors.New("not captured")
}

func (client *fakeClient) ReplayRequest(_ context.Context, id uint64) error {
	client.replayedID = id
	return nil
}
