package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfapi "vtunnel/internal/cloudflare"
	cf "vtunnel/internal/cloudflared"
	"vtunnel/internal/config"
	"vtunnel/internal/secrets"
)

func TestReportMissingCloudflaredOffersManualInstallAndDomainAction(t *testing.T) {
	tempDir := t.TempDir()
	cfg := config.Default()
	configPath := filepath.Join(tempDir, "config.yml")

	engine := New(Options{ConfigPath: configPath, Config: cfg})
	engine.deps.readToken = func() (string, error) { return "", secrets.ErrNotFound }
	engine.deps.inspectCF = func(context.Context) Cloudflared {
		return Cloudflared{CertPath: filepath.Join(tempDir, "cert.pem"), Err: errors.New("not found")}
	}
	engine.deps.inspectProcess = func(context.Context, string, string, string) ProcessInspection {
		return ProcessInspection{}
	}

	report := engine.Report(context.Background())
	if report.Ready {
		t.Fatal("report should not be ready")
	}
	for _, want := range []string{"save-config", "add-domain", "install-cloudflared"} {
		if !hasAction(report.Actions, want) {
			t.Fatalf("actions = %#v, want %s", report.Actions, want)
		}
	}
	if !reportContains(report, "cloudflared", "not found") {
		t.Fatalf("report does not describe missing cloudflared: %#v", report.Phases)
	}
}

func TestExecuteAddDomainPersistsConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yml")
	engine := New(Options{ConfigPath: configPath, Config: config.Default()})

	notice, err := engine.Execute(context.Background(), "add-domain", "Example.Test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "example.test") {
		t.Fatalf("notice = %q", notice)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultDomain != "example.test" {
		t.Fatalf("default domain = %q", cfg.DefaultDomain)
	}
	if got := strings.Join(cfg.Domains, ","); got != "example.test" {
		t.Fatalf("domains = %q", got)
	}
}

func TestExecuteSetDefaultDomainPersistsConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yml")
	cfg := config.Default()
	cfg.DefaultDomain = "one.test"
	cfg.Domains = []string{"one.test", "two.test"}
	engine := New(Options{ConfigPath: configPath, Config: cfg})

	notice, err := engine.Execute(context.Background(), "set-default-domain", "Two.Test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "two.test") {
		t.Fatalf("notice = %q", notice)
	}

	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DefaultDomain != "two.test" {
		t.Fatalf("default domain = %q", saved.DefaultDomain)
	}
}

func TestExecuteRemoveDomainPersistsConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yml")
	cfg := config.Default()
	cfg.DefaultDomain = "one.test"
	cfg.Domains = []string{"one.test", "two.test"}
	engine := New(Options{ConfigPath: configPath, Config: cfg})

	notice, err := engine.Execute(context.Background(), "remove-domain", "One.Test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "one.test") {
		t.Fatalf("notice = %q", notice)
	}

	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DefaultDomain != "two.test" {
		t.Fatalf("default domain = %q", saved.DefaultDomain)
	}
	if got := strings.Join(saved.Domains, ","); got != "two.test" {
		t.Fatalf("domains = %q", got)
	}
}

func TestExecuteRenameDomainPersistsConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yml")
	cfg := config.Default()
	cfg.DefaultDomain = "one.test"
	cfg.Domains = []string{"one.test", "two.test"}
	engine := New(Options{ConfigPath: configPath, Config: cfg})

	notice, err := engine.Execute(context.Background(), "rename-domain", "One.Test=New.Test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "one.test -> new.test") {
		t.Fatalf("notice = %q", notice)
	}

	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DefaultDomain != "new.test" {
		t.Fatalf("default domain = %q", saved.DefaultDomain)
	}
	if got := strings.Join(saved.Domains, ","); got != "new.test,two.test" {
		t.Fatalf("domains = %q", got)
	}
}

func TestExecuteUseTunnelWritesCloudflaredConfig(t *testing.T) {
	tempDir := t.TempDir()
	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	cfg := config.Default()
	cfg.Cloudflared.ConfigPath = cloudflaredPath
	engine := New(Options{ConfigPath: filepath.Join(tempDir, "config.yml"), Config: cfg})
	engine.deps.inspectCF = func(context.Context) Cloudflared {
		return Cloudflared{
			Path: "/bin/cloudflared",
			Tunnels: []cf.Tunnel{{
				ID:   "11111111-1111-1111-1111-111111111111",
				Name: "vtunnel-main",
			}},
		}
	}
	engine.deps.inspectProcess = func(context.Context, string, string, string) ProcessInspection {
		return ProcessInspection{}
	}

	notice, err := engine.Execute(context.Background(), "use-tunnel", "vtunnel-main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "vtunnel-main") {
		t.Fatalf("notice = %q", notice)
	}

	cloudflaredCfg, err := cf.Load(cloudflaredPath)
	if err != nil {
		t.Fatal(err)
	}
	if cloudflaredCfg.Tunnel != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("tunnel = %q", cloudflaredCfg.Tunnel)
	}
	if !strings.Contains(cloudflaredCfg.CredentialsFile, "11111111-1111-1111-1111-111111111111.json") {
		t.Fatalf("credentials-file = %q", cloudflaredCfg.CredentialsFile)
	}
}

func TestExecuteDeleteTunnelUsesCloudflared(t *testing.T) {
	tempDir := t.TempDir()
	cfg := config.Default()
	cfg.Cloudflared.ConfigPath = filepath.Join(tempDir, "cloudflared.yml")
	engine := New(Options{ConfigPath: filepath.Join(tempDir, "config.yml"), Config: cfg})
	engine.deps.inspectCF = func(context.Context) Cloudflared {
		return Cloudflared{
			Path: "/bin/cloudflared",
			Tunnels: []cf.Tunnel{{
				ID:   "22222222-2222-2222-2222-222222222222",
				Name: "old",
			}},
		}
	}
	engine.deps.inspectProcess = func(context.Context, string, string, string) ProcessInspection {
		return ProcessInspection{}
	}
	deleted := ""
	engine.deps.deleteCloudflaredTunnel = func(_ context.Context, _ string, tunnel string, force bool) error {
		if force {
			t.Fatal("delete should not force by default")
		}
		deleted = tunnel
		return nil
	}

	notice, err := engine.Execute(context.Background(), "delete-tunnel", "old")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("deleted = %q", deleted)
	}
	if !strings.Contains(notice, "old") {
		t.Fatalf("notice = %q", notice)
	}
}

func TestReportBuildsFullWizardStepsEvenWhenDomainsExist(t *testing.T) {
	tempDir := t.TempDir()
	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	engine := New(Options{ConfigPath: filepath.Join(tempDir, "config.yml"), Config: cfg})
	engine.deps.readToken = func() (string, error) { return "", secrets.ErrNotFound }
	engine.deps.inspectCF = func(context.Context) Cloudflared {
		return Cloudflared{CertPath: filepath.Join(tempDir, "cert.pem"), Err: errors.New("not found")}
	}
	engine.deps.inspectProcess = func(context.Context, string, string, string) ProcessInspection {
		return ProcessInspection{}
	}

	report := engine.Report(context.Background())
	if len(report.Steps) != 11 {
		t.Fatalf("steps = %d, want 11: %#v", len(report.Steps), report.Steps)
	}
	if report.Steps[0].ID != StepWelcome || report.Steps[len(report.Steps)-1].ID != StepCompletion {
		t.Fatalf("unexpected first/last step: %s/%s", report.Steps[0].ID, report.Steps[len(report.Steps)-1].ID)
	}
	domainStep := findStep(report.Steps, StepDomains)
	if domainStep == nil {
		t.Fatal("missing domain step")
	}
	if !hasAction(domainStep.Actions, "manage-domains") {
		t.Fatalf("domain actions = %#v, want manage-domains", domainStep.Actions)
	}
	if !hasAction(domainStep.Actions, "add-domain") {
		t.Fatalf("domain actions = %#v, want add-domain", domainStep.Actions)
	}
	tunnelStep := findStep(report.Steps, StepTunnel)
	if tunnelStep == nil {
		t.Fatal("missing tunnel step")
	}
	if !hasAction(tunnelStep.Actions, "manage-tunnels") {
		t.Fatalf("tunnel actions = %#v, want manage-tunnels", tunnelStep.Actions)
	}
}

func TestReportWrongWildcardDNSOffersFixDNS(t *testing.T) {
	tempDir := t.TempDir()
	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	if err := os.WriteFile(cloudflaredPath, []byte(`
tunnel: 11111111-1111-1111-1111-111111111111
credentials-file: /tmp/test-tunnel.json
ingress:
  - hostname: "*.example.test"
    service: http://127.0.0.1:8787
  - service: http_status:404
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	cfg.Cloudflared.ConfigPath = cloudflaredPath

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			writeEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case "/zones":
			writeEnvelope(t, w, []map[string]any{{"id": "zone-id", "name": "example.test", "status": "active"}})
		case "/zones/zone-id/dns_records":
			writeEnvelope(t, w, []map[string]any{{
				"id": "record-id", "type": "CNAME", "name": "*.example.test", "content": "old.cfargotunnel.com", "proxied": true,
			}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	t.Setenv(cfapi.BaseURLEnv, server.URL)

	engine := New(Options{ConfigPath: filepath.Join(tempDir, "config.yml"), Config: cfg})
	engine.deps.readToken = func() (string, error) { return "token", nil }
	engine.deps.inspectCF = func(context.Context) Cloudflared {
		return Cloudflared{
			Path:       "/bin/cloudflared",
			CertPath:   filepath.Join(tempDir, "cert.pem"),
			CertExists: true,
			Tunnels: []cf.Tunnel{{
				ID:   "11111111-1111-1111-1111-111111111111",
				Name: "vtunnel",
			}},
		}
	}
	engine.deps.inspectProcess = func(context.Context, string, string, string) ProcessInspection {
		return ProcessInspection{}
	}

	report := engine.Report(context.Background())
	if !hasAction(report.Actions, "fix-dns") {
		t.Fatalf("actions = %#v, want fix-dns", report.Actions)
	}
	if !reportContains(report, "wildcard DNS", "does not point") {
		t.Fatalf("report does not describe wrong DNS: %#v", report.Phases)
	}
}

func TestReportMalformedCloudflaredConfigShowsActionableCheck(t *testing.T) {
	tempDir := t.TempDir()
	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	if err := os.WriteFile(cloudflaredPath, []byte("ingress: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	cfg.Cloudflared.ConfigPath = cloudflaredPath

	engine := New(Options{ConfigPath: filepath.Join(tempDir, "config.yml"), Config: cfg})
	engine.deps.readToken = func() (string, error) { return "", secrets.ErrNotFound }
	engine.deps.inspectCF = func(context.Context) Cloudflared { return Cloudflared{Err: errors.New("not found")} }
	engine.deps.inspectProcess = func(context.Context, string, string, string) ProcessInspection {
		return ProcessInspection{}
	}

	report := engine.Report(context.Background())
	if !reportContains(report, "cloudflared config", "parse") {
		t.Fatalf("report does not describe malformed config: %#v", report.Phases)
	}
}

func TestEnsureHTTPRuntimeStartsCloudflaredAndDaemon(t *testing.T) {
	tempDir := t.TempDir()
	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	if err := os.WriteFile(cloudflaredPath, []byte(`
tunnel: 11111111-1111-1111-1111-111111111111
credentials-file: /tmp/test-tunnel.json
ingress:
  - hostname: "*.example.test"
    service: http://127.0.0.1:8787
  - service: http_status:404
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	cfg.API.Listen = "127.0.0.1:1"
	cfg.Cloudflared.ConfigPath = cloudflaredPath

	engine := New(Options{ConfigPath: filepath.Join(tempDir, "config.yml"), Config: cfg})
	engine.deps.readToken = func() (string, error) { return "", secrets.ErrNotFound }
	engine.deps.inspectCF = func(context.Context) Cloudflared {
		return Cloudflared{
			Path:       "/bin/cloudflared",
			CertPath:   filepath.Join(tempDir, "cert.pem"),
			CertExists: true,
			Tunnels: []cf.Tunnel{{
				ID:   "11111111-1111-1111-1111-111111111111",
				Name: "vtunnel",
			}},
		}
	}
	engine.deps.inspectProcess = func(context.Context, string, string, string) ProcessInspection {
		return ProcessInspection{}
	}
	var startedCloudflared bool
	engine.deps.startCloudflared = func(context.Context, string, string) (StartResult, error) {
		startedCloudflared = true
		return StartResult{PID: 4242}, nil
	}
	var startedDaemon bool
	engine.deps.startDaemon = func(context.Context, config.Config, string) error {
		startedDaemon = true
		return nil
	}

	result, err := engine.EnsureHTTPRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !startedCloudflared || result.Cloudflared == nil || result.Cloudflared.PID != 4242 {
		t.Fatalf("cloudflared result = %#v, started=%v", result.Cloudflared, startedCloudflared)
	}
	if !startedDaemon || !result.DaemonStarted {
		t.Fatalf("daemon started = %v, result = %#v", startedDaemon, result)
	}
}

func hasAction(actions []Action, id string) bool {
	for _, action := range actions {
		if action.ID == id {
			return true
		}
	}
	return false
}

func findStep(steps []Step, id StepID) *Step {
	for index := range steps {
		if steps[index].ID == id {
			return &steps[index]
		}
	}
	return nil
}

func reportContains(report Report, labelPart string, detailPart string) bool {
	for _, phase := range report.Phases {
		for _, check := range phase.Checks {
			if strings.Contains(check.Label, labelPart) && strings.Contains(check.Detail, detailPart) {
				return true
			}
		}
	}
	return false
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"success":true,"result":`))
	switch value := result.(type) {
	case string:
		_, _ = w.Write([]byte(value))
	default:
		data, err := jsonMarshal(value)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data)
	}
	_, _ = w.Write([]byte(`,"result_info":{"page":1,"per_page":50,"count":1,"total_count":1,"total_pages":1}}`))
}

func jsonMarshal(value any) ([]byte, error) {
	type jsonMarshaler interface {
		MarshalJSON() ([]byte, error)
	}
	if marshaler, ok := value.(jsonMarshaler); ok {
		return marshaler.MarshalJSON()
	}
	return json.Marshal(value)
}
