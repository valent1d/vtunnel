package cloudflared

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiagnoseFindsValidWildcardIngress(t *testing.T) {
	path := writeConfig(t, `
tunnel: valent1
credentials-file: /Users/example/.cloudflared/abc.json
ingress:
  - hostname: "*.example.test"
    service: http://localhost:8787
  - service: http_status:404
`)

	diag, err := Diagnose(path, []string{"example.test"}, "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if !diag.Exists {
		t.Fatal("expected config to exist")
	}
	if diag.Config.Tunnel != "valent1" {
		t.Fatalf("tunnel = %q", diag.Config.Tunnel)
	}
	if len(diag.DomainResults) != 1 {
		t.Fatalf("domain results = %d", len(diag.DomainResults))
	}
	result := diag.DomainResults[0]
	if !result.Found || !result.ServiceOK {
		t.Fatalf("result = %#v", result)
	}
}

func TestDiagnoseReportsWrongService(t *testing.T) {
	path := writeConfig(t, `
ingress:
  - hostname: "*.example.test"
    service: http://localhost:9999
  - service: http_status:404
`)

	diag, err := Diagnose(path, []string{"example.test"}, "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	missing := MissingIngressRules(diag)
	if len(missing) != 1 {
		t.Fatalf("missing = %d, want 1", len(missing))
	}
	if !missing[0].Found || missing[0].ServiceOK {
		t.Fatalf("missing result = %#v", missing[0])
	}
}

func TestDiagnoseReportsMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yml")
	diag, err := Diagnose(path, []string{"example.test"}, "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if diag.Exists {
		t.Fatal("config should not exist")
	}
}

func TestPlanConfigCreatesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	plan, err := PlanConfig(path, []string{"example.test"}, "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Exists {
		t.Fatal("plan should mark config as missing")
	}
	if len(plan.Changes) != 3 {
		t.Fatalf("changes = %#v", plan.Changes)
	}
	if got := plan.Config.Ingress[0].Hostname; got != "*.example.test" {
		t.Fatalf("hostname = %q", got)
	}
	if got := plan.Config.Ingress[len(plan.Config.Ingress)-1].Service; got != "http_status:404" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestPlanConfigPreservesRulesAndUpdatesWrongService(t *testing.T) {
	path := writeConfig(t, `
tunnel: valent1
credentials-file: /tmp/creds.json
ingress:
  - hostname: "keep.example.test"
    service: http://localhost:3000
  - hostname: "*.example.test"
    service: http://localhost:9999
  - service: http_status:404
`)

	plan, err := PlanConfig(path, []string{"example.test", "app.test"}, "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("changes = %#v", plan.Changes)
	}
	if plan.Config.Ingress[0].Hostname != "keep.example.test" {
		t.Fatalf("first rule was not preserved: %#v", plan.Config.Ingress)
	}
	if plan.Config.Ingress[1].Service != "http://127.0.0.1:8787" {
		t.Fatalf("example service = %q", plan.Config.Ingress[1].Service)
	}
	if plan.Config.Ingress[2].Hostname != "*.app.test" {
		t.Fatalf("app rule position = %#v", plan.Config.Ingress)
	}
	if plan.Config.Ingress[3].Service != "http_status:404" {
		t.Fatalf("fallback moved incorrectly = %#v", plan.Config.Ingress)
	}
}

func TestWritePlanCreatesBackupAndWritesConfig(t *testing.T) {
	path := writeConfig(t, `
ingress:
  - hostname: "*.example.test"
    service: http://localhost:9999
  - service: http_status:404
`)
	plan, err := PlanConfig(path, []string{"example.test"}, "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}

	result, err := WritePlan(plan, time.Date(2026, 5, 17, 15, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected backup path")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ingress[0].Service != "http://127.0.0.1:8787" {
		t.Fatalf("service = %q", cfg.Ingress[0].Service)
	}
}

func TestWriteTunnelConfigUpdateCreatesBackupAndWritesTunnelFields(t *testing.T) {
	path := writeConfig(t, `
tunnel: old
credentials-file: /tmp/old.json
ingress:
  - service: http_status:404
`)

	result, err := WriteTunnelConfigUpdate(TunnelConfigUpdate{
		Path:            path,
		Tunnel:          "new-id",
		CredentialsFile: "/tmp/new-id.json",
	}, time.Date(2026, 5, 17, 15, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected backup path")
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunnel != "new-id" {
		t.Fatalf("tunnel = %q", cfg.Tunnel)
	}
	if cfg.CredentialsFile != "/tmp/new-id.json" {
		t.Fatalf("credentials-file = %q", cfg.CredentialsFile)
	}
	if len(cfg.Ingress) != 1 || cfg.Ingress[0].Service != "http_status:404" {
		t.Fatalf("ingress = %#v", cfg.Ingress)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlanAddIngressInsertsTCPBeforeWildcard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	seed := `tunnel: t-1
credentials-file: /creds.json
ingress:
  - hostname: "*.example.test"
    service: http://127.0.0.1:8787
  - service: http_status:404
`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanAddIngress(path, "db.example.test", "tcp://127.0.0.1:3306")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Kind != "add-ingress" {
		t.Fatalf("changes = %+v", plan.Changes)
	}
	if _, err := WritePlan(plan, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Order must be: db (tcp) → *.example.test → fallback.
	if len(cfg.Ingress) != 3 ||
		cfg.Ingress[0].Hostname != "db.example.test" ||
		!isWildcardRule(cfg.Ingress[1]) ||
		!isFallbackRule(cfg.Ingress[2]) {
		t.Fatalf("ingress order wrong: %+v", cfg.Ingress)
	}
	if cfg.Ingress[0].Service != "tcp://127.0.0.1:3306" {
		t.Fatalf("tcp service = %q", cfg.Ingress[0].Service)
	}

	tcp, err := TCPIngress(path)
	if err != nil || len(tcp) != 1 || tcp[0].Hostname != "db.example.test" {
		t.Fatalf("TCPIngress = %+v err = %v", tcp, err)
	}

	// Removing it.
	rmPlan, err := PlanRemoveIngress(path, "db.example.test")
	if err != nil || len(rmPlan.Changes) != 1 {
		t.Fatalf("remove plan = %+v err = %v", rmPlan, err)
	}
	if _, err := WritePlan(rmPlan, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if got, _ := TCPIngress(path); len(got) != 0 {
		t.Fatalf("tcp rule should be gone: %+v", got)
	}
}

func TestPlanAddIngressRequiresTunnel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("ingress: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanAddIngress(path, "db.example.test", "tcp://127.0.0.1:3306"); err == nil {
		t.Fatal("expected error when no tunnel configured")
	}
}
