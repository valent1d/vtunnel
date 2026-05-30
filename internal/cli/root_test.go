package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"vtunnel/internal/api"
	cfapi "vtunnel/internal/cloudflare"
	cf "vtunnel/internal/cloudflared"
	"vtunnel/internal/config"
	"vtunnel/internal/daemon"
	"vtunnel/internal/httpui"
	"vtunnel/internal/routes"
	"vtunnel/internal/secrets"
)

func TestHTTPListStopStatusCommands(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	proxyAddr, proxyLn := reserveLoopback(t)
	apiAddr, apiLn := reserveLoopback(t)
	cfg.Proxy.Listen = proxyAddr
	cfg.API.Listen = apiAddr
	cfg.Cloudflared.ConfigPath = writeReadyCloudflaredConfig(t, tempDir, "11111111-1111-1111-1111-111111111111", "example.test", cfg.Proxy.Listen)

	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	cancel := startDaemon(t, cfg, proxyLn, apiLn)
	defer cancel()

	upstream := newCLIUpstream(t)
	defer upstream.close()

	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})
	stubCloudflaredProcess(t, cloudflaredProcessInspection{
		Processes: []cloudflaredProcess{{PID: 1234, Command: "/bin/cloudflared --config " + cfg.Cloudflared.ConfigPath + " tunnel run"}},
		Matches:   []cloudflaredProcess{{PID: 1234, Command: "/bin/cloudflared --config " + cfg.Cloudflared.ConfigPath + " tunnel run"}},
	})

	out, err := executeCommand(context.Background(), "--config", configPath, "http", upstream.port, "dev", "--detach")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Forwarding https://dev.example.test -> http://127.0.0.1:" + upstream.port; !strings.Contains(out, want) {
		t.Fatalf("http output = %q, want it to contain %q", out, want)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+cfg.Proxy.Listen+"/api/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "dev.example.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	out, err = executeCommand(context.Background(), "--config", configPath, "status")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Daemon: running"; !strings.Contains(out, want) {
		t.Fatalf("status output = %q, want it to contain %q", out, want)
	}
	if want := "Active routes: 1"; !strings.Contains(out, want) {
		t.Fatalf("status output = %q, want it to contain %q", out, want)
	}
	if want := "Logged requests: 1"; !strings.Contains(out, want) {
		t.Fatalf("status output = %q, want it to contain %q", out, want)
	}

	out, err = executeCommand(context.Background(), "--config", configPath, "list")
	if err != nil {
		t.Fatal(err)
	}
	if want := "dev.example.test"; !strings.Contains(out, want) {
		t.Fatalf("list output = %q, want it to contain %q", out, want)
	}

	out, err = executeCommand(context.Background(), "--config", configPath, "logs", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if want := "GET    200"; !strings.Contains(out, want) {
		t.Fatalf("logs output = %q, want it to contain %q", out, want)
	}
	if want := "/api/login"; !strings.Contains(out, want) {
		t.Fatalf("logs output = %q, want it to contain %q", out, want)
	}

	out, err = executeCommand(context.Background(), "--config", configPath, "stop", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Stopped dev.example.test"; !strings.Contains(out, want) {
		t.Fatalf("stop output = %q, want it to contain %q", out, want)
	}

	out, err = executeCommand(context.Background(), "--config", configPath, "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "dev.example.test") {
		t.Fatalf("list output = %q, route should have been removed", out)
	}
}

func TestDaemonStopCommand(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cfg := config.Default()
	proxyAddr, proxyLn := reserveLoopback(t)
	apiAddr, apiLn := reserveLoopback(t)
	cfg.Proxy.Listen = proxyAddr
	cfg.API.Listen = apiAddr

	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	cleanup := startDaemon(t, cfg, proxyLn, apiLn)
	defer cleanup()

	out, err := executeCommand(context.Background(), "--config", configPath, "daemon", "stop")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Daemon: stopped"; !strings.Contains(out, want) {
		t.Fatalf("daemon stop output = %q, want it to contain %q", out, want)
	}

	out, err = executeCommand(context.Background(), "--config", configPath, "status")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Daemon: stopped"; !strings.Contains(out, want) {
		t.Fatalf("status output = %q, want it to contain %q", out, want)
	}
}

func TestServiceInstallWritesLaunchAgents(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("vtunnel services are supported on macOS only")
	}
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	stubLaunchdRunner(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		t.Fatal("launchctl should not run with --no-start")
		return nil, nil
	})

	cfg := config.Default()
	cfg.Cloudflared.ConfigPath = filepath.Join(tempDir, ".cloudflared", "config.yml")
	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	out, err := executeCommand(
		context.Background(),
		"--config", configPath,
		"service", "install",
		"--vtunnel-bin", "/tmp/vtunnel",
		"--cloudflared-bin", "/tmp/cloudflared",
		"--no-start",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Installed vtunnel services:", "Services installed but not started.", "sh.vltn.vtunnel.daemon.plist", "sh.vltn.vtunnel.cloudflared.plist"} {
		if !strings.Contains(out, want) {
			t.Fatalf("service install output = %q, want %q", out, want)
		}
	}

	daemonPlist := filepath.Join(tempDir, "Library", "LaunchAgents", "sh.vltn.vtunnel.daemon.plist")
	cloudflaredPlist := filepath.Join(tempDir, "Library", "LaunchAgents", "sh.vltn.vtunnel.cloudflared.plist")
	for _, path := range []string{daemonPlist, cloudflaredPlist} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "<key>RunAtLoad</key>") || !strings.Contains(string(data), "<key>KeepAlive</key>") {
			t.Fatalf("plist %s = %q, want RunAtLoad and KeepAlive", path, string(data))
		}
	}
}

func TestServiceStatusShowsLaunchAgentState(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("vtunnel services are supported on macOS only")
	}
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	stubLaunchdRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "print" {
			return []byte("state = running"), nil
		}
		return nil, nil
	})

	cfg := config.Default()
	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	out, err := executeCommand(context.Background(), "--config", configPath, "service", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vtunnel services:", "vtunnel daemon: loaded", "cloudflared: loaded"} {
		if !strings.Contains(out, want) {
			t.Fatalf("service status output = %q, want %q", out, want)
		}
	}
}

func TestOnboardingCommandHelp(t *testing.T) {
	out, err := executeCommand(context.Background(), "onboarding", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Open the guided first-run setup wizard", "onboarding"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output = %q, want %q", out, want)
		}
	}
}

func TestHelpIncludesBrandLogo(t *testing.T) {
	out, err := executeCommand(context.Background(), "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_   __________", "Pleasant local tunnels powered by Cloudflare Tunnel", "Available Commands:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output = %q, want %q", out, want)
		}
	}
}

func TestRootCommandVersion(t *testing.T) {
	out, err := executeCommand(context.Background(), "--version")
	if err != nil {
		t.Fatal(err)
	}
	if want := "vtunnel dev"; !strings.Contains(out, want) {
		t.Fatalf("version output = %q, want %q", out, want)
	}
}

func TestSubcommandHelpIncludesBrandLogo(t *testing.T) {
	out, err := executeCommand(context.Background(), "http", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_   __________", "Open the HTTP tunnel dashboard", "Usage:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output = %q, want %q", out, want)
		}
	}
}

func TestRootCommandPrintsWelcome(t *testing.T) {
	out, err := executeCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vtunnel", "Version:", "Useful commands:", "vtunnel http", "vtunnel onboarding"} {
		if !strings.Contains(out, want) {
			t.Fatalf("welcome output = %q, want %q", out, want)
		}
	}
}

func TestRootCommandShowsOnboardingBannerWhenUnconfigured(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.yml")
	out, err := executeCommand(context.Background(), "--config", missing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "one step left") {
		t.Fatalf("expected onboarding banner, got %q", out)
	}
}

func TestRootCommandHidesOnboardingBannerWhenConfigured(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(existing, []byte("proxy:\n  listen: 127.0.0.1:8787\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := executeCommand(context.Background(), "--config", existing)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "one step left") {
		t.Fatalf("did not expect onboarding banner, got %q", out)
	}
}

// isolateUninstallEnv points config/home at a temp dir and stubs the Keychain so
// uninstall tests never touch the real machine.
func isolateUninstallEnv(t *testing.T) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Pin the daemon API/proxy to freshly-closed ports so daemon detection is
	// deterministic — never picking up a real vtunnel daemon on the default
	// :8788 that may be running on the test machine.
	cfg := config.Default()
	cfg.Proxy.Listen = freeLoopbackAddr(t)
	cfg.API.Listen = freeLoopbackAddr(t)
	cfgPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	previousRead := readCloudflareTokenFromKeychain
	previousDelete := deleteCloudflareTokenFromKeychain
	previousRunner := launchdRunner
	readCloudflareTokenFromKeychain = func() (string, error) { return "", secrets.ErrNotFound }
	deleteCloudflareTokenFromKeychain = func() error { return nil }
	launchdRunner = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not loaded")
	}
	t.Cleanup(func() {
		readCloudflareTokenFromKeychain = previousRead
		deleteCloudflareTokenFromKeychain = previousDelete
		launchdRunner = previousRunner
	})
}

func TestUninstallDryRunKeepsCloudflareAndChangesNothing(t *testing.T) {
	isolateUninstallEnv(t)

	out, err := executeCommand(context.Background(), "uninstall", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"vtunnel uninstall will remove:",
		"macOS LaunchAgents",
		"Cloudflare account resources: kept (pass --cloudflare",
		"Dry run — nothing was removed.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("uninstall --dry-run output = %q, want %q", out, want)
		}
	}
}

func TestUninstallKeepConfigShownInPlan(t *testing.T) {
	isolateUninstallEnv(t)

	out, err := executeCommand(context.Background(), "uninstall", "--dry-run", "--keep-config")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Local data: kept (--keep-config)") {
		t.Fatalf("expected kept local data, got %q", out)
	}
}

func TestUninstallCloudflareFlagShowsSection(t *testing.T) {
	isolateUninstallEnv(t)

	out, err := executeCommand(context.Background(), "uninstall", "--dry-run", "--cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Cloudflare account resources") || strings.Contains(out, "kept (pass --cloudflare") {
		t.Fatalf("expected Cloudflare section to be active, got %q", out)
	}
	if !strings.Contains(out, "cloudflared config not found") {
		t.Fatalf("expected cloudflared config note, got %q", out)
	}
}

func TestUninstallExecutesWithNothingToRemove(t *testing.T) {
	isolateUninstallEnv(t)

	out, err := executeCommand(context.Background(), "uninstall", "--yes", "--keep-config")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Nothing to remove.", "brew uninstall vtunnel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("uninstall output = %q, want %q", out, want)
		}
	}
}

func TestHTTPCommandStartsCloudflaredWhenStopped(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	proxyAddr, proxyLn := reserveLoopback(t)
	apiAddr, apiLn := reserveLoopback(t)
	cfg.Proxy.Listen = proxyAddr
	cfg.API.Listen = apiAddr
	cfg.Cloudflared.ConfigPath = writeReadyCloudflaredConfig(t, tempDir, "11111111-1111-1111-1111-111111111111", "example.test", cfg.Proxy.Listen)

	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	cancel := startDaemon(t, cfg, proxyLn, apiLn)
	defer cancel()

	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})

	processCalls := 0
	previousProcess := inspectCloudflaredProcessForCLI
	inspectCloudflaredProcessForCLI = func(_ context.Context, _ string, _ string, _ string) cloudflaredProcessInspection {
		processCalls++
		return cloudflaredProcessInspection{}
	}
	t.Cleanup(func() {
		inspectCloudflaredProcessForCLI = previousProcess
	})

	previousStart := startCloudflaredForCLI
	startCloudflaredForCLI = func(_ context.Context, path string, configPath string) (cloudflaredStartResult, error) {
		if path != "/bin/cloudflared" {
			t.Fatalf("cloudflared path = %q", path)
		}
		if configPath != cfg.Cloudflared.ConfigPath {
			t.Fatalf("cloudflared config path = %q", configPath)
		}
		return cloudflaredStartResult{PID: 4242}, nil
	}
	t.Cleanup(func() {
		startCloudflaredForCLI = previousStart
	})

	out, err := executeCommand(context.Background(), "--config", configPath, "http", "3000", "dev", "--detach")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cloudflared: started (pid 4242)",
		"Forwarding https://dev.example.test -> http://127.0.0.1:3000",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("http output = %q, want it to contain %q", out, want)
		}
	}
	if processCalls == 0 {
		t.Fatal("expected cloudflared process inspection")
	}
}

func TestHTTPCommandWithoutArgsOpensDashboard(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cfg := config.Default()
	proxyAddr, proxyLn := reserveLoopback(t)
	apiAddr, apiLn := reserveLoopback(t)
	cfg.Proxy.Listen = proxyAddr
	cfg.API.Listen = apiAddr

	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	cancel := startDaemon(t, cfg, proxyLn, apiLn)
	defer cancel()

	previousRun := runHTTPUI
	var opened bool
	runHTTPUI = func(_ context.Context, got config.Config, selected string, _ httpui.AccessController) error {
		opened = true
		if got.API.Listen != cfg.API.Listen {
			t.Fatalf("API listen = %q", got.API.Listen)
		}
		if selected != "" {
			t.Fatalf("selected = %q, want empty", selected)
		}
		return nil
	}
	t.Cleanup(func() {
		runHTTPUI = previousRun
	})

	if _, err := executeCommand(context.Background(), "--config", configPath, "http"); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("expected HTTP dashboard to open")
	}
}

func TestHTTPCommandWithRouteOpensDashboardSelected(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	proxyAddr, proxyLn := reserveLoopback(t)
	apiAddr, apiLn := reserveLoopback(t)
	cfg.Proxy.Listen = proxyAddr
	cfg.API.Listen = apiAddr
	cfg.Cloudflared.ConfigPath = writeReadyCloudflaredConfig(t, tempDir, "11111111-1111-1111-1111-111111111111", "example.test", cfg.Proxy.Listen)

	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	cancel := startDaemon(t, cfg, proxyLn, apiLn)
	defer cancel()

	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})
	stubCloudflaredProcess(t, cloudflaredProcessInspection{
		Processes: []cloudflaredProcess{{PID: 1234, Command: "/bin/cloudflared --config " + cfg.Cloudflared.ConfigPath + " tunnel run"}},
		Matches:   []cloudflaredProcess{{PID: 1234, Command: "/bin/cloudflared --config " + cfg.Cloudflared.ConfigPath + " tunnel run"}},
	})

	previousRun := runHTTPUI
	var selectedHostname string
	runHTTPUI = func(_ context.Context, _ config.Config, selected string, _ httpui.AccessController) error {
		selectedHostname = selected
		return nil
	}
	t.Cleanup(func() {
		runHTTPUI = previousRun
	})

	out, err := executeCommand(context.Background(), "--config", configPath, "http", "3000", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if selectedHostname != "dev.example.test" {
		t.Fatalf("selected = %q", selectedHostname)
	}
	if want := "Forwarding https://dev.example.test -> http://127.0.0.1:3000"; !strings.Contains(out, want) {
		t.Fatalf("http output = %q, want %q", out, want)
	}
}

func TestHTTPCommandExplainsMissingCloudflaredIngress(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	if err := os.WriteFile(cloudflaredPath, []byte(`
tunnel: 11111111-1111-1111-1111-111111111111
credentials-file: /tmp/test-tunnel.json
ingress:
  - service: http_status:404
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test"}
	cfg.Proxy.Listen = freeLoopbackAddr(t)
	cfg.API.Listen = freeLoopbackAddr(t)
	cfg.Cloudflared.ConfigPath = cloudflaredPath

	configPath, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})

	out, err := executeCommand(context.Background(), "--config", configPath, "http", "3000", "dev", "--detach")
	if err == nil {
		t.Fatalf("expected error, output = %q", out)
	}
	for _, want := range []string{
		"cloudflared config is not ready for this route.",
		"Run: vtunnel setup --write-cloudflared",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestSetupCommandAddsDomainsAndPrintsIngress(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	out, err := executeCommand(
		context.Background(),
		"--config", configPath,
		"setup",
		"--domain", "Example.Test",
		"--domain", "app.test",
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"vtunnel setup",
		"vtunnel config created:",
		"vtunnel domains: example.test, app.test",
		"add ingress: *.example.test -> http://127.0.0.1:8787",
		"add ingress: *.app.test -> http://127.0.0.1:8787",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultDomain != "example.test" {
		t.Fatalf("default domain = %q", cfg.DefaultDomain)
	}
	if got, want := strings.Join(cfg.Domains, ","), "example.test,app.test"; got != want {
		t.Fatalf("domains = %q, want %q", got, want)
	}
}

func TestSetupCommandReportsCloudflaredIngressDiagnostics(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)

	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	if err := os.WriteFile(cloudflaredPath, []byte(`
tunnel: test-tunnel
credentials-file: /tmp/test-tunnel.json
ingress:
  - hostname: "*.example.test"
    service: http://localhost:8787
  - hostname: "*.bad.test"
    service: http://localhost:9999
  - service: http_status:404
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test", "bad.test", "missing.test"}
	cfg.Cloudflared.ConfigPath = cloudflaredPath

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	out, err := executeCommand(context.Background(), "--config", configPath, "setup")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"vtunnel setup",
		"Setup status: needs attention",
		"[OK] tunnel id/name: test-tunnel",
		`[OK] wildcard ingress: *.example.test -> http://localhost:8787`,
		`[ACTION] wildcard ingress: *.bad.test points to http://localhost:9999, expected http://127.0.0.1:8787`,
		`[ACTION] wildcard ingress: missing *.missing.test -> http://127.0.0.1:8787`,
		"[ACTION] cloudflared config needs changes; run: vtunnel setup --write-cloudflared",
		"update ingress: *.bad.test from http://localhost:9999 to http://127.0.0.1:8787",
		"add ingress: *.missing.test -> http://127.0.0.1:8787",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}
}

func TestSetupCommandWritesCloudflaredIngress(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)

	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	if err := os.WriteFile(cloudflaredPath, []byte(`
ingress:
  - hostname: "keep.example.test"
    service: http://localhost:3000
  - hostname: "*.example.test"
    service: http://localhost:9999
  - service: http_status:404
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DefaultDomain = "example.test"
	cfg.Domains = []string{"example.test", "app.test"}
	cfg.Cloudflared.ConfigPath = cloudflaredPath

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	out, err := executeCommand(context.Background(), "--config", configPath, "setup", "--write-cloudflared")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[OK] cloudflared config updated",
		"update ingress: *.example.test from http://localhost:9999 to http://127.0.0.1:8787",
		"add ingress: *.app.test -> http://127.0.0.1:8787",
		"backup:",
		"wrote:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}

	data, err := os.ReadFile(cloudflaredPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"keep.example.test",
		`hostname: '*.example.test'`,
		`hostname: '*.app.test'`,
		"service: http://127.0.0.1:8787",
		"service: http_status:404",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("cloudflared config = %q, want it to contain %q", string(data), want)
		}
	}

	backups, err := filepath.Glob(cloudflaredPath + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %#v, want exactly one", backups)
	}
}

func TestSetupCommandFixesWildcardDNSWithAPI(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

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

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	updated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/tokens/verify":
			writeCloudflareEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			writeCloudflareEnvelope(t, w, []map[string]any{{
				"id": "zone-id", "name": "example.test", "status": "active",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-id/dns_records":
			content := "old.cfargotunnel.com"
			if updated {
				content = "11111111-1111-1111-1111-111111111111.cfargotunnel.com"
			}
			writeCloudflareEnvelope(t, w, []map[string]any{{
				"id": "record-id", "type": "CNAME", "name": "*.example.test", "content": content, "proxied": true,
			}})
		case r.Method == http.MethodPut && r.URL.Path == "/zones/zone-id/dns_records/record-id":
			var input cfapi.DNSRecordInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Name != "*.example.test" || input.Content != "11111111-1111-1111-1111-111111111111.cfargotunnel.com" || !input.Proxied {
				t.Fatalf("dns input = %#v", input)
			}
			updated = true
			writeCloudflareEnvelope(t, w, map[string]any{
				"id": "record-id", "type": "CNAME", "name": input.Name, "content": input.Content, "proxied": input.Proxied,
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	stubCloudflareKeychainToken(t, "test-token", nil)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})
	t.Setenv(cfapi.BaseURLEnv, server.URL)

	out, err := executeCommand(context.Background(), "--config", configPath, "setup", "--fix-dns")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[OK] DNS fix applied via Cloudflare API",
		"updated *.example.test CNAME -> 11111111-1111-1111-1111-111111111111.cfargotunnel.com",
		"[OK] wildcard DNS: *.example.test CNAME -> 11111111-1111-1111-1111-111111111111.cfargotunnel.com",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}
	if !updated {
		t.Fatal("expected DNS record to be updated")
	}
}

func TestSetupCommandFixesMissingConfiguredTunnel(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)

	cloudflaredPath := filepath.Join(tempDir, "cloudflared.yml")
	if err := os.WriteFile(cloudflaredPath, []byte(`
tunnel: old-missing-id
credentials-file: /tmp/old-missing-id.json
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
	cfg.Cloudflared.TunnelName = "vtunnel-dev"

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	inspectionCalls := 0
	stubCloudflaredInspectionFunc(t, func() cloudflaredInspection {
		inspectionCalls++
		inspection := cloudflaredInspection{
			Path:       "/bin/cloudflared",
			CertPath:   filepath.Join(tempDir, "cert.pem"),
			CertExists: true,
		}
		if inspectionCalls > 1 {
			inspection.Tunnels = []cf.Tunnel{{ID: "new-tunnel-id", Name: "vtunnel-dev"}}
		}
		return inspection
	})

	previousCreate := createCloudflaredTunnel
	createCloudflaredTunnel = func(_ context.Context, path string, name string) (cf.CreateTunnelResult, error) {
		if path != "/bin/cloudflared" {
			t.Fatalf("cloudflared path = %q", path)
		}
		if name != "vtunnel-dev" {
			t.Fatalf("tunnel name = %q", name)
		}
		return cf.CreateTunnelResult{
			ID:              "new-tunnel-id",
			Name:            name,
			CredentialsFile: "/tmp/new-tunnel-id.json",
		}, nil
	}
	t.Cleanup(func() {
		createCloudflaredTunnel = previousCreate
	})

	previousWrite := writeCloudflaredTunnelConfig
	writeCloudflaredTunnelConfig = func(update cf.TunnelConfigUpdate) (cf.WriteResult, error) {
		return cf.WriteTunnelConfigUpdate(update, time.Date(2026, 5, 17, 15, 4, 5, 0, time.UTC))
	}
	t.Cleanup(func() {
		writeCloudflaredTunnelConfig = previousWrite
	})

	out, err := executeCommand(context.Background(), "--config", configPath, "setup", "--fix-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[OK] tunnel created: vtunnel-dev (new-tunnel-id)",
		"credentials-file: /tmp/new-tunnel-id.json",
		"[OK] configured tunnel exists: vtunnel-dev (new-tunnel-id)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}

	updated, err := cf.Load(cloudflaredPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Tunnel != "new-tunnel-id" {
		t.Fatalf("tunnel = %q", updated.Tunnel)
	}
	if updated.CredentialsFile != "/tmp/new-tunnel-id.json" {
		t.Fatalf("credentials-file = %q", updated.CredentialsFile)
	}
}

func TestSetupCommandSuggestsStartingCloudflaredWhenConfigIsReady(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

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

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/tokens/verify":
			writeCloudflareEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "zone-id", "name": "example.test", "status": "active"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-id/dns_records":
			writeCloudflareEnvelope(t, w, []map[string]any{{
				"id": "record-id", "type": "CNAME", "name": "*.example.test", "content": "11111111-1111-1111-1111-111111111111.cfargotunnel.com", "proxied": true,
			}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	stubCloudflareKeychainToken(t, "test-token", nil)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})
	stubCloudflaredProcess(t, cloudflaredProcessInspection{})
	t.Setenv(cfapi.BaseURLEnv, server.URL)

	out, err := executeCommand(context.Background(), "--config", configPath, "setup")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Setup status: needs attention",
		"[ACTION] cloudflared process: not running",
		"Start cloudflared with this config: go run ./cmd/vtunnel setup --start-cloudflared",
		"Once installed: vtunnel setup --start-cloudflared",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}
}

func TestSetupCommandStartsCloudflared(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)

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

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	processCalls := 0
	previousProcess := inspectCloudflaredProcessForCLI
	inspectCloudflaredProcessForCLI = func(_ context.Context, _ string, _ string, _ string) cloudflaredProcessInspection {
		processCalls++
		if processCalls > 1 {
			return cloudflaredProcessInspection{
				Processes: []cloudflaredProcess{{PID: 4242, Command: "/bin/cloudflared --config " + cloudflaredPath + " tunnel run"}},
				Matches:   []cloudflaredProcess{{PID: 4242, Command: "/bin/cloudflared --config " + cloudflaredPath + " tunnel run"}},
			}
		}
		return cloudflaredProcessInspection{}
	}
	t.Cleanup(func() {
		inspectCloudflaredProcessForCLI = previousProcess
	})

	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})

	previousStart := startCloudflaredForCLI
	started := false
	startCloudflaredForCLI = func(_ context.Context, path string, configPath string) (cloudflaredStartResult, error) {
		if path != "/bin/cloudflared" {
			t.Fatalf("cloudflared path = %q", path)
		}
		if configPath != cloudflaredPath {
			t.Fatalf("cloudflared config path = %q", configPath)
		}
		started = true
		return cloudflaredStartResult{PID: 4242, LogPath: filepath.Join(tempDir, "cloudflared.log")}, nil
	}
	t.Cleanup(func() {
		startCloudflaredForCLI = previousStart
	})

	out, err := executeCommand(context.Background(), "--config", configPath, "setup", "--start-cloudflared")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[OK] cloudflared started: pid 4242",
		"[OK] cloudflared process: running with this config (pid 4242)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}
	if !started {
		t.Fatal("expected cloudflared to be started")
	}
}

func TestParseCloudflaredProcessLineIgnoresVtunnelCommandWithCloudflaredFlag(t *testing.T) {
	if process, ok := parseCloudflaredProcessLine("12345 /tmp/go-build/vtunnel setup --start-cloudflared"); ok {
		t.Fatalf("process = %#v, want line ignored", process)
	}
	process, ok := parseCloudflaredProcessLine("23456 /opt/homebrew/bin/cloudflared --config /tmp/config.yml tunnel run")
	if !ok {
		t.Fatal("expected cloudflared line to be parsed")
	}
	if process.PID != 23456 {
		t.Fatalf("pid = %d", process.PID)
	}
}

func TestSetupCommandSuggestsFixDNSWhenWildcardDNSIsWrong(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

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

	configPath := filepath.Join(tempDir, "vtunnel", "config.yml")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/tokens/verify":
			writeCloudflareEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "zone-id", "name": "example.test", "status": "active"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-id/dns_records":
			writeCloudflareEnvelope(t, w, []map[string]any{{
				"id": "record-id", "type": "CNAME", "name": "*.example.test", "content": "old.cfargotunnel.com", "proxied": true,
			}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	stubCloudflareKeychainToken(t, "test-token", nil)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Path:       "/bin/cloudflared",
		CertPath:   filepath.Join(tempDir, "cert.pem"),
		CertExists: true,
		Tunnels: []cf.Tunnel{{
			ID:   "11111111-1111-1111-1111-111111111111",
			Name: "vtunnel",
		}},
	})
	t.Setenv(cfapi.BaseURLEnv, server.URL)

	out, err := executeCommand(context.Background(), "--config", configPath, "setup")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[ACTION] wildcard DNS: *.example.test exists but does not point to 11111111-1111-1111-1111-111111111111.cfargotunnel.com",
		"Fix Cloudflare wildcard DNS: go run ./cmd/vtunnel setup --fix-dns",
		"Once installed: vtunnel setup --fix-dns",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output = %q, want it to contain %q", out, want)
		}
	}
}

func TestCloudflareStatusCommandWithoutToken(t *testing.T) {
	stubCloudflareKeychainToken(t, "", secrets.ErrNotFound)
	out, err := executeCommand(context.Background(), "cloudflare", "status")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Cloudflare API token: missing"; !strings.Contains(out, want) {
		t.Fatalf("cloudflare status output = %q, want it to contain %q", out, want)
	}
}

func TestCloudflareStatusCommandDiscoversResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			writeCloudflareEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case "/accounts":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "account-id", "name": "Example", "type": "standard"}})
		case "/zones":
			writeCloudflareEnvelope(t, w, []map[string]any{{
				"id": "zone-id", "name": "example.test", "status": "active",
				"account": map[string]any{"id": "account-id", "name": "Example"},
			}})
		case "/zones/zone-id/dns_records":
			if got, want := r.URL.Query().Get("name"), "*.example.test"; got != want {
				t.Fatalf("name = %q, want %q", got, want)
			}
			writeCloudflareEnvelope(t, w, []map[string]any{{
				"id": "record-id", "type": "CNAME", "name": "*.example.test", "content": "tunnel-id.cfargotunnel.com", "proxied": true,
			}})
		default:
			t.Fatalf("unexpected Cloudflare API path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	stubCloudflareKeychainToken(t, "test-token", nil)
	stubCloudflaredInspection(t, cloudflaredInspection{
		Tunnels: []cf.Tunnel{{ID: "tunnel-id", Name: "dev"}},
	})
	t.Setenv(cfapi.BaseURLEnv, server.URL)

	out, err := executeCommand(context.Background(), "cloudflare", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Cloudflare API token: active",
		"Accounts: 1",
		"Example (account-id)",
		"Zones: 1",
		"example.test [active]",
		"Local cloudflared tunnels: 1",
		"dev [stopped] tunnel-id",
		"Wildcard DNS records: 1",
		"CNAME *.example.test -> tunnel-id.cfargotunnel.com [proxied]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("cloudflare status output = %q, want it to contain %q", out, want)
		}
	}
}

func TestCloudflareStatusCommandIgnoresAccountIDEnv(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			writeCloudflareEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case "/accounts":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "account-id", "name": "Example", "type": "standard"}})
		case "/zones":
			writeCloudflareEnvelope(t, w, []map[string]any{})
		default:
			t.Fatalf("unexpected Cloudflare API path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	stubCloudflareKeychainToken(t, "test-token", nil)
	stubCloudflaredInspection(t, cloudflaredInspection{})
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "ignored-account-id")
	t.Setenv(cfapi.BaseURLEnv, server.URL)

	out, err := executeCommand(context.Background(), "cloudflare", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Cloudflare API token: active",
		"Example (account-id)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("cloudflare status output = %q, want it to contain %q", out, want)
		}
	}
}

func TestCloudflareAuthVerifiesUserTokenWithoutAccountHint(t *testing.T) {
	var storedToken string
	previousRead := readCloudflareTokenFromKeychain
	previousWrite := writeCloudflareTokenToKeychain
	readCloudflareTokenFromKeychain = func() (string, error) {
		return storedToken, nil
	}
	writeCloudflareTokenToKeychain = func(token string) error {
		storedToken = token
		return nil
	}
	t.Cleanup(func() {
		readCloudflareTokenFromKeychain = previousRead
		writeCloudflareTokenToKeychain = previousWrite
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			writeCloudflareEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		default:
			t.Fatalf("unexpected Cloudflare API path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "ignored-account-id")
	t.Setenv(cfapi.BaseURLEnv, server.URL)

	out, err := executeCommand(context.Background(), "cloudflare", "auth", "--token", "test-token", "--no-open")
	if err != nil {
		t.Fatal(err)
	}
	if storedToken != "test-token" {
		t.Fatalf("stored token = %q, want test-token", storedToken)
	}
	for _, want := range []string{
		"Stored Cloudflare API token in macOS Keychain.",
		"Token status: active",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("cloudflare auth output = %q, want it to contain %q", out, want)
		}
	}
}

func executeCommand(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	root := NewRootCommand()
	root.SetArgs(args)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetContext(ctx)

	err := root.Execute()
	if err != nil && stderr.Len() > 0 {
		return stdout.String() + stderr.String(), err
	}
	return stdout.String(), err
}

func writeCloudflareEnvelope(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"result":  result,
		"result_info": map[string]any{
			"page":        1,
			"per_page":    50,
			"count":       1,
			"total_count": 1,
			"total_pages": 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func stubCloudflareKeychainToken(t *testing.T, token string, err error) {
	t.Helper()
	previous := readCloudflareTokenFromKeychain
	readCloudflareTokenFromKeychain = func() (string, error) {
		return token, err
	}
	t.Cleanup(func() {
		readCloudflareTokenFromKeychain = previous
	})
}

func stubLaunchdRunner(t *testing.T, runner func(context.Context, string, ...string) ([]byte, error)) {
	t.Helper()
	previous := launchdRunner
	launchdRunner = runner
	t.Cleanup(func() {
		launchdRunner = previous
	})
}

func stubCloudflaredInspection(t *testing.T, inspection cloudflaredInspection) {
	t.Helper()
	stubCloudflaredInspectionFunc(t, func() cloudflaredInspection {
		return inspection
	})
}

func stubCloudflaredInspectionFunc(t *testing.T, inspect func() cloudflaredInspection) {
	t.Helper()
	previous := inspectCloudflaredForCLI
	inspectCloudflaredForCLI = inspect
	t.Cleanup(func() {
		inspectCloudflaredForCLI = previous
	})
}

func stubCloudflaredProcess(t *testing.T, inspection cloudflaredProcessInspection) {
	t.Helper()
	previous := inspectCloudflaredProcessForCLI
	inspectCloudflaredProcessForCLI = func(context.Context, string, string, string) cloudflaredProcessInspection {
		return inspection
	}
	t.Cleanup(func() {
		inspectCloudflaredProcessForCLI = previous
	})
}

func writeReadyCloudflaredConfig(t *testing.T, tempDir string, tunnelID string, domain string, proxyListen string) string {
	t.Helper()
	path := filepath.Join(tempDir, "cloudflared.yml")
	data := []byte(`
tunnel: ` + tunnelID + `
credentials-file: /tmp/test-tunnel.json
ingress:
  - hostname: "*.` + domain + `"
    service: http://` + proxyListen + `
  - service: http_status:404
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func startDaemon(t *testing.T, cfg config.Config, proxyLn, apiLn net.Listener) context.CancelFunc {
	t.Helper()

	routesPath, err := config.RoutesPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := routes.NewStore(routesPath)
	if err != nil {
		t.Fatal(err)
	}
	logStore, err := savedRequestLogStore()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		errs <- daemon.New(cfg, store, logStore, logger, daemon.WithListeners(proxyLn, apiLn)).Run(ctx)
	}()

	waitForHealth(t, api.New(cfg))
	return func() {
		cancel()
		select {
		case err := <-errs:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("daemon returned unexpected error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("daemon did not stop")
		}
	}
}

type cliUpstream struct {
	port  string
	close func()
}

func newCLIUpstream(t *testing.T) cliUpstream {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))

	url := strings.TrimPrefix(server.URL, "http://")
	_, port, err := net.SplitHostPort(url)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	if _, err := strconv.Atoi(port); err != nil {
		server.Close()
		t.Fatal(err)
	}
	return cliUpstream{port: port, close: server.Close}
}

func waitForHealth(t *testing.T, client *api.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for {
		_, err := client.Health(ctx)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("daemon did not become healthy: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

// reserveLoopback binds a loopback listener and keeps it open (closed on
// cleanup). Handing the listener to the daemon via daemon.WithListeners avoids
// the bind-after-close port race that flakes under parallel test execution.
func reserveLoopback(t *testing.T) (string, net.Listener) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String(), listener
}
