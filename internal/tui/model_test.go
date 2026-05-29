package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"vtunnel/internal/api"
	"vtunnel/internal/config"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

func TestRenderDashboard(t *testing.T) {
	output := Render(Snapshot{
		DaemonRunning: true,
		Health:        api.Health{Routes: 1, Logs: 1},
		Routes: []routes.Route{{
			Hostname: "dev.example.test",
			Target:   "http://127.0.0.1:3000",
		}},
		Logs: []requestlog.Entry{{
			Time:     time.Date(2026, 5, 17, 14, 30, 0, 0, time.UTC),
			Hostname: "dev.example.test",
			Method:   "GET",
			Path:     "/api/login",
			Status:   200,
			Duration: 12 * time.Millisecond,
		}},
		UpdatedAt: time.Date(2026, 5, 17, 14, 31, 0, 0, time.UTC),
		Width:     100,
		Height:    32,
		Tab:       tabRoutes,
		LogFilter: "dev.example.test",
	})

	for _, want := range []string{
		"vtunnel",
		"running",
		"Active tunnels",
		"1 Routes",
		"2 Logs",
		"dev.example.test",
		"http://127.0.0.1:3000",
		"Request logs: dev.example.test",
		"n new",
		"r refresh",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderSelectedRoute(t *testing.T) {
	output := Render(Snapshot{
		DaemonRunning: true,
		Health:        api.Health{Routes: 2},
		SelectedRoute: 1,
		Routes: []routes.Route{
			{Hostname: "dev.example.test", Target: "http://127.0.0.1:3000"},
			{Hostname: "api.example.test", Target: "http://127.0.0.1:8000"},
		},
		Width: 100,
	})

	if !strings.Contains(output, "> * api.example.test") {
		t.Fatalf("selected route marker missing:\n%s", output)
	}
}

func TestRenderStoppedDashboard(t *testing.T) {
	output := Render(Snapshot{
		DaemonRunning: false,
		Error:         "daemon stopped",
		UpdatedAt:     time.Date(2026, 5, 17, 14, 31, 0, 0, time.UTC),
		Width:         80,
	})

	if !strings.Contains(output, "daemon") || !strings.Contains(output, "stopped") {
		t.Fatalf("output does not show stopped daemon:\n%s", output)
	}
	if !strings.Contains(output, "No active routes") {
		t.Fatalf("output does not show empty routes:\n%s", output)
	}
}

func TestRenderDoesNotExceedViewportWidth(t *testing.T) {
	width := 96
	output := Render(Snapshot{
		DaemonRunning: false,
		Error:         "daemon stopped",
		Config:        testConfig(),
		UpdatedAt:     time.Date(2026, 5, 17, 14, 31, 0, 0, time.UTC),
		Width:         width,
		Height:        24,
	})

	for _, line := range strings.Split(output, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line width = %d, want <= %d:\n%s", got, width, line)
		}
	}
}

func TestRenderLogsTab(t *testing.T) {
	output := Render(Snapshot{
		DaemonRunning: true,
		Health:        api.Health{Routes: 1, Logs: 1},
		Tab:           tabLogs,
		Logs: []requestlog.Entry{{
			Time:     time.Date(2026, 5, 17, 14, 30, 0, 0, time.UTC),
			Hostname: "dev.example.test",
			Method:   "POST",
			Path:     "/webhook",
			Status:   201,
			Duration: 8 * time.Millisecond,
		}},
		Width: 100,
	})

	for _, want := range []string{"Request logs", "POST", "201", "/webhook"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRenderConfigAndSetupTabs(t *testing.T) {
	cfg := testConfig()

	configOutput := Render(Snapshot{Config: cfg, Tab: tabConfig, Width: 100})
	for _, want := range []string{"Config", "Default domain: example.test", "Proxy: 127.0.0.1:8787"} {
		if !strings.Contains(configOutput, want) {
			t.Fatalf("config output does not contain %q:\n%s", want, configOutput)
		}
	}

	setupOutput := Render(Snapshot{Config: cfg, Tab: tabSetup, Width: 100})
	for _, want := range []string{"Setup", `hostname: "*.example.test"`, "service: http://127.0.0.1:8787"} {
		if !strings.Contains(setupOutput, want) {
			t.Fatalf("setup output does not contain %q:\n%s", want, setupOutput)
		}
	}
}

func TestRenderCreateForm(t *testing.T) {
	output := Render(Snapshot{
		Creating: true,
		Fields: []CreateField{
			{Label: "Subdomain", Value: "dev", Focus: true},
			{Label: "Port", Value: "3000"},
			{Label: "Domain", Value: "example.test"},
		},
		Width: 100,
	})

	for _, want := range []string{"New tunnel", "Subdomain:", "dev", "Port:", "3000", "enter next/create"} {
		if !strings.Contains(output, want) {
			t.Fatalf("create output does not contain %q:\n%s", want, output)
		}
	}
}

func TestRouteFromInputs(t *testing.T) {
	cfg := testConfig()
	inputs := newCreateInputs(cfg)
	inputs[0].SetValue("dev")
	inputs[1].SetValue("3000")

	route, err := routeFromInputs(inputs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if route.Hostname != "dev.example.test" {
		t.Fatalf("hostname = %q", route.Hostname)
	}
	if route.Target != "http://127.0.0.1:3000" {
		t.Fatalf("target = %q", route.Target)
	}
}

func TestDeleteRouteCommand(t *testing.T) {
	client := &fakeClient{}
	model := NewModelWithConfig(client, testConfig())
	cmd := model.deleteRoute("dev.example.test")

	msg := cmd()
	deleted, ok := msg.(routeDeletedMsg)
	if !ok {
		t.Fatalf("message type = %T", msg)
	}
	if deleted.err != "" {
		t.Fatalf("delete returned error: %s", deleted.err)
	}
	if client.deletedHostname != "dev.example.test" {
		t.Fatalf("deleted hostname = %q", client.deletedHostname)
	}
}

func TestRouteSelectionHelpers(t *testing.T) {
	routeList := []routes.Route{
		{Hostname: "dev.example.test"},
		{Hostname: "api.example.test"},
	}
	if got := selectedRouteHostname(routeList, 99); got != "api.example.test" {
		t.Fatalf("selected hostname = %q", got)
	}
	if got := selectedRouteHostname(nil, 0); got != "" {
		t.Fatalf("selected hostname = %q", got)
	}
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	return cfg
}

type fakeClient struct {
	deletedHostname string
}

func (f *fakeClient) Health(context.Context) (api.Health, error) {
	return api.Health{}, nil
}

func (f *fakeClient) ListRoutes(context.Context) ([]routes.Route, error) {
	return nil, nil
}

func (f *fakeClient) ListLogs(context.Context, requestlog.Filter) ([]requestlog.Entry, error) {
	return nil, nil
}

func (f *fakeClient) AddRoute(context.Context, routes.Route) error {
	return nil
}

func (f *fakeClient) DeleteRoute(_ context.Context, hostname string) error {
	f.deletedHostname = hostname
	return nil
}

func (f *fakeClient) Shutdown(context.Context) error {
	return nil
}
