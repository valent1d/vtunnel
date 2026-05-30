package uninstall

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recorder builds Actions whose every call appends a label to a shared slice so
// tests can assert ordering and which steps ran.
type recorder struct {
	calls []string
}

func (r *recorder) actions() Actions {
	return Actions{
		StopDaemon:     func(context.Context) error { r.calls = append(r.calls, "stop-daemon"); return nil },
		RemoveServices: func(context.Context) error { r.calls = append(r.calls, "remove-services"); return nil },
		DeleteDNS:      func(_ context.Context, rec DNSRecord) error { r.calls = append(r.calls, "dns:"+rec.Hostname); return nil },
		DeleteTunnel:   func(_ context.Context, ref string) error { r.calls = append(r.calls, "tunnel:"+ref); return nil },
		RemovePath:     func(path string) error { r.calls = append(r.calls, "path:"+path); return nil },
		DeleteToken:    func() error { r.calls = append(r.calls, "token"); return nil },
	}
}

func fullPlan(opts Options) Plan {
	return Plan{
		Options:       opts,
		DaemonRunning: true,
		Services: []Service{
			{Name: "vtunnel daemon", Installed: true, Loaded: true},
			{Name: "cloudflared", Installed: true},
		},
		Local: []LocalPath{
			{Label: "Config directory", Path: "/home/u/.config/vtunnel", Present: true},
		},
		Token: true,
		Cloudflare: Cloudflare{
			Resolved:   true,
			TunnelRef:  "tunnel-id",
			TunnelName: "vtunnel",
			Records: []DNSRecord{
				{ZoneID: "z1", RecordID: "r1", Hostname: "*.example.com"},
			},
			Creds: LocalPath{Label: "creds", Path: "/home/u/.cloudflared/tunnel-id.json", Present: true},
		},
	}
}

func TestExecuteFullOrderWithCloudflare(t *testing.T) {
	rec := &recorder{}
	report := Execute(context.Background(), fullPlan(Options{IncludeCloudflare: true}), rec.actions())

	want := []string{
		"stop-daemon",
		"remove-services",
		"dns:*.example.com",
		"tunnel:tunnel-id",
		"path:/home/u/.cloudflared/tunnel-id.json",
		"path:/home/u/.config/vtunnel",
		"token",
	}
	if strings.Join(rec.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("call order = %v, want %v", rec.calls, want)
	}
	if report.Failed() {
		t.Fatal("report should not have failed")
	}
}

func TestExecuteSkipsCloudflareWhenNotRequested(t *testing.T) {
	rec := &recorder{}
	Execute(context.Background(), fullPlan(Options{IncludeCloudflare: false}), rec.actions())

	for _, call := range rec.calls {
		if strings.HasPrefix(call, "dns:") || strings.HasPrefix(call, "tunnel:") || strings.Contains(call, ".cloudflared") {
			t.Fatalf("cloudflare step ran without opt-in: %q (calls=%v)", call, rec.calls)
		}
	}
	// Local + token must still run.
	if !contains(rec.calls, "token") || !contains(rec.calls, "path:/home/u/.config/vtunnel") {
		t.Fatalf("local cleanup missing: %v", rec.calls)
	}
}

func TestExecuteKeepConfigPreservesLocalAndToken(t *testing.T) {
	rec := &recorder{}
	Execute(context.Background(), fullPlan(Options{IncludeCloudflare: true, KeepConfig: true}), rec.actions())

	if contains(rec.calls, "token") {
		t.Fatalf("token should be kept with --keep-config: %v", rec.calls)
	}
	for _, call := range rec.calls {
		if call == "path:/home/u/.config/vtunnel" {
			t.Fatalf("config dir should be kept with --keep-config: %v", rec.calls)
		}
	}
	// Cloudflare credentials removal is part of CF teardown, not local config.
	if !contains(rec.calls, "remove-services") {
		t.Fatalf("services should still be removed: %v", rec.calls)
	}
}

func TestExecuteSkipsAbsentResources(t *testing.T) {
	rec := &recorder{}
	plan := Plan{
		Options:  Options{},
		Services: []Service{{Name: "vtunnel daemon", Installed: false}},
		Local:    []LocalPath{{Label: "Config", Path: "/gone", Present: false}},
		Token:    false,
	}
	Execute(context.Background(), plan, rec.actions())
	if len(rec.calls) != 0 {
		t.Fatalf("nothing should run when nothing is present: %v", rec.calls)
	}
}

func TestExecuteContinuesAfterFailure(t *testing.T) {
	rec := &recorder{}
	actions := rec.actions()
	actions.RemoveServices = func(context.Context) error { return errors.New("boom") }

	report := Execute(context.Background(), fullPlan(Options{IncludeCloudflare: true}), actions)

	if !report.Failed() {
		t.Fatal("report should record the failure")
	}
	// Later steps still ran despite the earlier failure.
	if !contains(rec.calls, "token") {
		t.Fatalf("teardown aborted after failure: %v", rec.calls)
	}
	var failed int
	for _, step := range report.Steps {
		if step.Outcome == Failed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("expected exactly one failed step, got %d", failed)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
