package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"vtunnel/internal/api"
	cfapi "vtunnel/internal/cloudflare"
	cf "vtunnel/internal/cloudflared"
	"vtunnel/internal/config"
	"vtunnel/internal/routes"
	"vtunnel/internal/secrets"
)

type Status string

const (
	StatusOK     Status = "ok"
	StatusWarn   Status = "warn"
	StatusAction Status = "action"
)

type Check struct {
	Label  string
	Status Status
	Detail string
}

type Phase struct {
	Title   string
	Summary string
	Checks  []Check
}

type StepID string

const (
	StepWelcome    StepID = "welcome"
	StepLocal      StepID = "local"
	StepAuth       StepID = "auth"
	StepDiscovery  StepID = "discovery"
	StepDomains    StepID = "domains"
	StepTunnel     StepID = "tunnel"
	StepDNS        StepID = "dns"
	StepConfig     StepID = "config"
	StepServices   StepID = "services"
	StepHealth     StepID = "health"
	StepCompletion StepID = "completion"
)

type Step struct {
	ID          StepID
	Title       string
	Summary     string
	Description string
	Checks      []Check
	Actions     []Action
	Commands    []Command
	Manual      []string
}

type Action struct {
	ID          string
	Label       string
	Description string
	Command     string
	Mutates     bool
	InputPrompt string
}

// Command is a reference invocation surfaced on the final step. Unlike a Check,
// it carries no status: it is something the user runs, not a diagnostic.
type Command struct {
	Invocation  string
	Description string
}

type Report struct {
	Ready   bool
	Phases  []Phase
	Steps   []Step
	Actions []Action
	Updated time.Time
}

type Options struct {
	ConfigPath string
	Config     config.Config
	Hooks      Hooks
}

type Engine struct {
	options Options
	deps    deps
}

type Hooks struct {
	ReadToken               func() (string, error)
	WriteToken              func(string) error
	OpenURL                 func(string) error
	CloudflareClient        func(string) (*cfapi.Client, error)
	InspectCloudflared      func(context.Context) Cloudflared
	InspectProcess          func(context.Context, string, string, string) ProcessInspection
	StartCloudflared        func(context.Context, string, string) (StartResult, error)
	StartDaemon             func(context.Context, config.Config, string) error
	CreateCloudflaredTunnel func(context.Context, string, string) (cf.CreateTunnelResult, error)
	DeleteCloudflaredTunnel func(context.Context, string, string, bool) error
	WriteTunnelConfig       func(cf.TunnelConfigUpdate) (cf.WriteResult, error)
	WriteCloudflaredPlan    func(cf.Plan) (cf.WriteResult, error)
}

type deps struct {
	readToken               func() (string, error)
	writeToken              func(string) error
	openURL                 func(string) error
	cloudflareClient        func(string) (*cfapi.Client, error)
	inspectCF               func(context.Context) Cloudflared
	inspectProcess          func(context.Context, string, string, string) ProcessInspection
	startCloudflared        func(context.Context, string, string) (StartResult, error)
	startDaemon             func(context.Context, config.Config, string) error
	createCloudflaredTunnel func(context.Context, string, string) (cf.CreateTunnelResult, error)
	deleteCloudflaredTunnel func(context.Context, string, string, bool) error
	writeTunnelConfig       func(cf.TunnelConfigUpdate) (cf.WriteResult, error)
	writeCloudflaredPlan    func(cf.Plan) (cf.WriteResult, error)
}

type Cloudflared struct {
	Path          string
	VersionOutput string
	LocalVersion  string
	LatestVersion string
	CertPath      string
	CertExists    bool
	Tunnels       []cf.Tunnel
	TunnelListErr error
	Err           error
	LatestErr     error
}

func (inspection Cloudflared) UpdateAvailable() bool {
	return inspection.LocalVersion != "" &&
		inspection.LatestVersion != "" &&
		inspection.LocalVersion != inspection.LatestVersion
}

type Process struct {
	PID     int
	Command string
}

type ProcessInspection struct {
	Processes []Process
	Matches   []Process
	Err       error
}

func (inspection ProcessInspection) RunningForConfig() bool {
	return len(inspection.Matches) > 0
}

type StartResult struct {
	PID     int
	LogPath string
	Skipped bool
}

type snapshot struct {
	configPath     string
	cfg            config.Config
	configExists   bool
	diag           cf.Diagnostic
	diagErr        error
	plan           cf.Plan
	planErr        error
	cloudflared    Cloudflared
	process        ProcessInspection
	tokenMissing   bool
	tokenErr       error
	tokenStatus    cfapi.TokenStatus
	zones          []cfapi.Zone
	zonesErr       error
	dns            DNSInspection
	daemonHealth   api.Health
	daemonRunning  bool
	daemonErr      error
	proxyReachable bool
	proxyErr       error
}

type DNSInspection struct {
	TokenMissing bool
	Err          error
	Results      []WildcardDNSResult
}

func (inspection DNSInspection) Ready() bool {
	if inspection.TokenMissing || inspection.Err != nil || len(inspection.Results) == 0 {
		return false
	}
	for _, result := range inspection.Results {
		if !result.Ready() {
			return false
		}
	}
	return true
}

type WildcardDNSResult struct {
	Domain          string
	Zone            cfapi.Zone
	ZoneFound       bool
	ExpectedName    string
	ExpectedContent string
	Records         []cfapi.DNSRecord
	Err             error
}

func (result WildcardDNSResult) Ready() bool {
	return result.ZoneFound && result.Err == nil && len(result.MatchingRecords()) > 0
}

func (result WildcardDNSResult) MatchingRecords() []cfapi.DNSRecord {
	matches := make([]cfapi.DNSRecord, 0, len(result.Records))
	for _, record := range result.Records {
		if dnsRecordMatchesTunnel(record, result.ExpectedContent) {
			matches = append(matches, record)
		}
	}
	return matches
}

func New(options Options) *Engine {
	options.ConfigPath = strings.TrimSpace(options.ConfigPath)
	if options.ConfigPath == "" {
		path, _ := config.ConfigPath()
		options.ConfigPath = path
	}
	engine := &Engine{options: options}
	engine.deps = deps{
		readToken:        func() (string, error) { return secrets.DefaultStore().Get(secrets.CloudflareToken) },
		writeToken:       func(token string) error { return secrets.DefaultStore().Set(secrets.CloudflareToken, token) },
		openURL:          func(rawURL string) error { return exec.Command("open", rawURL).Start() },
		cloudflareClient: newCloudflareClient,
		inspectCF:        InspectCloudflared,
		inspectProcess:   InspectProcess,
		startCloudflared: StartCloudflared,
		startDaemon:      StartDaemon,
		createCloudflaredTunnel: func(ctx context.Context, path string, name string) (cf.CreateTunnelResult, error) {
			return cf.NewTunnelRunner(path).Create(ctx, name, "")
		},
		deleteCloudflaredTunnel: func(ctx context.Context, path string, tunnel string, force bool) error {
			return cf.NewTunnelRunner(path).Delete(ctx, tunnel, force)
		},
		writeTunnelConfig: func(update cf.TunnelConfigUpdate) (cf.WriteResult, error) {
			return cf.WriteTunnelConfigUpdate(update, time.Now())
		},
		writeCloudflaredPlan: func(plan cf.Plan) (cf.WriteResult, error) { return cf.WritePlan(plan, time.Now()) },
	}
	engine.applyHooks(options.Hooks)
	return engine
}

func (engine *Engine) applyHooks(hooks Hooks) {
	if hooks.ReadToken != nil {
		engine.deps.readToken = hooks.ReadToken
	}
	if hooks.WriteToken != nil {
		engine.deps.writeToken = hooks.WriteToken
	}
	if hooks.OpenURL != nil {
		engine.deps.openURL = hooks.OpenURL
	}
	if hooks.CloudflareClient != nil {
		engine.deps.cloudflareClient = hooks.CloudflareClient
	}
	if hooks.InspectCloudflared != nil {
		engine.deps.inspectCF = hooks.InspectCloudflared
	}
	if hooks.InspectProcess != nil {
		engine.deps.inspectProcess = hooks.InspectProcess
	}
	if hooks.StartCloudflared != nil {
		engine.deps.startCloudflared = hooks.StartCloudflared
	}
	if hooks.StartDaemon != nil {
		engine.deps.startDaemon = hooks.StartDaemon
	}
	if hooks.CreateCloudflaredTunnel != nil {
		engine.deps.createCloudflaredTunnel = hooks.CreateCloudflaredTunnel
	}
	if hooks.DeleteCloudflaredTunnel != nil {
		engine.deps.deleteCloudflaredTunnel = hooks.DeleteCloudflaredTunnel
	}
	if hooks.WriteTunnelConfig != nil {
		engine.deps.writeTunnelConfig = hooks.WriteTunnelConfig
	}
	if hooks.WriteCloudflaredPlan != nil {
		engine.deps.writeCloudflaredPlan = hooks.WriteCloudflaredPlan
	}
}

func (engine *Engine) Config() config.Config {
	return engine.options.Config
}

func (engine *Engine) Report(ctx context.Context) Report {
	s := engine.snapshot(ctx)
	report := Report{
		Ready:   reportReady(s),
		Updated: time.Now(),
	}
	report.Phases = []Phase{
		welcomePhase(),
		localPhase(s),
		cloudflarePhase(s),
		discoveryPhase(s),
		domainPhase(s),
		tunnelPhase(s),
		dnsPhase(s),
		configPhase(s),
		servicesPhase(s),
		runtimePhase(s),
		completionPhase(s),
	}
	report.Actions = availableActions(s)
	report.Steps = wizardSteps(s)
	return report
}

func (engine *Engine) Execute(ctx context.Context, actionID string, input string) (string, error) {
	actionID = strings.TrimSpace(actionID)
	input = strings.TrimSpace(input)
	switch actionID {
	case "save-config":
		if err := config.Save(engine.options.ConfigPath, engine.options.Config); err != nil {
			return "", err
		}
		return "Config saved.", nil
	case "add-domain":
		domain := routes.NormalizeHostname(input)
		if domain == "" {
			return "", errors.New("domain is required")
		}
		cfg := addDomain(engine.options.Config, domain)
		if err := config.Save(engine.options.ConfigPath, cfg); err != nil {
			return "", err
		}
		engine.options.Config = cfg
		return "Domain added: " + domain, nil
	case "set-default-domain":
		domain := routes.NormalizeHostname(input)
		if domain == "" {
			return "", errors.New("domain is required")
		}
		cfg, ok := setDefaultDomain(engine.options.Config, domain)
		if !ok {
			return "", fmt.Errorf("domain %q is not configured yet", domain)
		}
		if err := config.Save(engine.options.ConfigPath, cfg); err != nil {
			return "", err
		}
		engine.options.Config = cfg
		return "Default domain set: " + domain, nil
	case "remove-domain":
		domain := routes.NormalizeHostname(input)
		if domain == "" {
			return "", errors.New("domain is required")
		}
		cfg, ok := removeDomain(engine.options.Config, domain)
		if !ok {
			return "", fmt.Errorf("domain %q is not configured yet", domain)
		}
		if err := config.Save(engine.options.ConfigPath, cfg); err != nil {
			return "", err
		}
		engine.options.Config = cfg
		return "Domain removed: " + domain, nil
	case "rename-domain":
		from, to, ok := parseDomainRename(input)
		if !ok {
			return "", errors.New("rename input must be old-domain=new-domain")
		}
		cfg, ok := renameDomain(engine.options.Config, from, to)
		if !ok {
			return "", fmt.Errorf("domain %q is not configured yet", from)
		}
		if err := config.Save(engine.options.ConfigPath, cfg); err != nil {
			return "", err
		}
		engine.options.Config = cfg
		return "Domain renamed: " + from + " -> " + to, nil
	case "store-token":
		if input == "" {
			return "", errors.New("cloudflare API token is required")
		}
		client, err := engine.deps.cloudflareClient(input)
		if err != nil {
			return "", err
		}
		status, err := client.VerifyToken(ctx)
		if err != nil {
			return "", fmt.Errorf("verify Cloudflare API token: %w", err)
		}
		if err := engine.deps.writeToken(input); err != nil {
			return "", err
		}
		if status.Status != "" {
			return "Cloudflare API token stored. Status: " + status.Status, nil
		}
		return "Cloudflare API token stored.", nil
	case "open-token-url":
		if err := engine.deps.openURL(TokenTemplateURL()); err != nil {
			return "", err
		}
		return "Opened Cloudflare token template URL.", nil
	case "write-cloudflared":
		s := engine.snapshot(ctx)
		if s.planErr != nil {
			return "", s.planErr
		}
		result, err := engine.deps.writeCloudflaredPlan(s.plan)
		if err != nil {
			return "", err
		}
		lines := []string{"cloudflared config updated"}
		for _, change := range s.plan.Changes {
			lines = append(lines, formatCloudflaredChange(change))
		}
		if result.BackupPath != "" {
			lines = append(lines, "backup: "+result.BackupPath)
		}
		if result.Plan.Path != "" {
			lines = append(lines, "wrote: "+result.Plan.Path)
		}
		return strings.Join(lines, "\n  "), nil
	case "fix-tunnel":
		s := engine.snapshot(ctx)
		result, err := engine.fixTunnel(ctx, s)
		if err != nil {
			return "", err
		}
		if result.Skipped {
			return "tunnel already exists: " + result.TunnelName, nil
		}
		return "tunnel created: " + result.TunnelName + " (" + result.TunnelID + ")", nil
	case "use-tunnel":
		tunnelRef := strings.TrimSpace(input)
		if tunnelRef == "" {
			return "", errors.New("tunnel is required")
		}
		s := engine.snapshot(ctx)
		tunnel, found := cf.FindTunnel(s.cloudflared.Tunnels, tunnelRef)
		if !found {
			return "", fmt.Errorf("tunnel %q is not visible via cloudflared", tunnelRef)
		}
		if err := engine.writeConfiguredTunnel(tunnel.ID, defaultTunnelCredentialsFile(tunnel.ID)); err != nil {
			return "", err
		}
		return "Tunnel selected: " + tunnel.Name + " (" + tunnel.ID + ")", nil
	case "create-tunnel":
		name := strings.TrimSpace(input)
		if name == "" {
			return "", errors.New("tunnel name is required")
		}
		s := engine.snapshot(ctx)
		result, err := engine.deps.createCloudflaredTunnel(ctx, s.cloudflared.Path, name)
		if err != nil {
			return "", err
		}
		if err := engine.writeConfiguredTunnel(result.ID, result.CredentialsFile); err != nil {
			return "", err
		}
		return "Tunnel created and selected: " + result.Name + " (" + result.ID + ")", nil
	case "delete-tunnel":
		tunnelRef := strings.TrimSpace(input)
		if tunnelRef == "" {
			return "", errors.New("tunnel is required")
		}
		s := engine.snapshot(ctx)
		tunnel, found := cf.FindTunnel(s.cloudflared.Tunnels, tunnelRef)
		if !found {
			return "", fmt.Errorf("tunnel %q is not visible via cloudflared", tunnelRef)
		}
		if s.diag.Config.Tunnel == tunnel.ID || s.diag.Config.Tunnel == tunnel.Name {
			return "", errors.New("cannot delete the configured tunnel; select another tunnel first")
		}
		if err := engine.deps.deleteCloudflaredTunnel(ctx, s.cloudflared.Path, tunnel.ID, false); err != nil {
			return "", err
		}
		return "Tunnel deleted: " + tunnel.Name + " (" + tunnel.ID + ")", nil
	case "fix-dns":
		s := engine.snapshot(ctx)
		result, err := engine.fixDNS(ctx, s)
		if err != nil {
			return "", err
		}
		if len(result.Changes) == 0 && len(result.Skipped) == 0 {
			return "DNS already ready.", nil
		}
		lines := []string{"DNS fix applied via " + result.Method}
		lines = append(lines, result.Changes...)
		lines = append(lines, result.Skipped...)
		return strings.Join(lines, "\n  "), nil
	case "start-cloudflared":
		s := engine.snapshot(ctx)
		result, err := startConfiguredCloudflared(ctx, engine.options.Config, s.cloudflared, s.process, s.diag, s.diagErr, s.plan, s.planErr, engine.deps.startCloudflared)
		if err != nil {
			return "", err
		}
		if result.Skipped {
			return "cloudflared already running.", nil
		}
		return "cloudflared started: pid " + strconv.Itoa(result.PID), nil
	case "start-daemon":
		if err := engine.deps.startDaemon(ctx, engine.options.Config, engine.options.ConfigPath); err != nil {
			return "", err
		}
		return "vtunnel daemon started.", nil
	default:
		return "", fmt.Errorf("unknown onboarding action %q", actionID)
	}
}

func (engine *Engine) writeConfiguredTunnel(tunnelID string, credentialsFile string) error {
	tunnelID = strings.TrimSpace(tunnelID)
	credentialsFile = strings.TrimSpace(credentialsFile)
	if tunnelID == "" {
		return errors.New("tunnel id is required")
	}
	if credentialsFile == "" {
		credentialsFile = defaultTunnelCredentialsFile(tunnelID)
	}
	_, err := engine.deps.writeTunnelConfig(cf.TunnelConfigUpdate{
		Path:            config.ExpandPath(engine.options.Config.Cloudflared.ConfigPath),
		Tunnel:          tunnelID,
		CredentialsFile: credentialsFile,
	})
	return err
}

func (engine *Engine) snapshot(ctx context.Context) snapshot {
	cfg := engine.options.Config
	configPath := engine.options.ConfigPath
	_, statErr := os.Stat(configPath)
	configExists := statErr == nil
	cloudflaredPath := config.ExpandPath(cfg.Cloudflared.ConfigPath)
	diag, diagErr := cf.Diagnose(cloudflaredPath, cfg.Domains, cfg.Proxy.Listen)
	plan, planErr := cf.PlanConfig(cloudflaredPath, cfg.Domains, cfg.Proxy.Listen)
	cloudflared := engine.deps.inspectCF(ctx)
	process := engine.deps.inspectProcess(ctx, cloudflared.Path, cloudflaredPath, diag.Config.Tunnel)
	token, tokenReadErr := engine.deps.readToken()
	s := snapshot{
		configPath:   configPath,
		cfg:          cfg,
		configExists: configExists,
		diag:         diag,
		diagErr:      diagErr,
		plan:         plan,
		planErr:      planErr,
		cloudflared:  cloudflared,
		process:      process,
	}
	if errors.Is(tokenReadErr, secrets.ErrNotFound) || errors.Is(tokenReadErr, cfapi.ErrMissingToken) {
		s.tokenMissing = true
	} else if tokenReadErr != nil {
		s.tokenErr = tokenReadErr
	} else {
		client, err := engine.deps.cloudflareClient(token)
		if err != nil {
			s.tokenErr = err
		} else {
			s.tokenStatus, s.tokenErr = client.VerifyToken(ctx)
			if s.tokenErr == nil {
				s.zones, s.zonesErr = client.ListZones(ctx)
				s.dns = inspectDNSWithClient(ctx, client, cfg, diag, s.zones, s.zonesErr)
			}
		}
	}
	if s.tokenMissing {
		s.dns = DNSInspection{TokenMissing: true}
	}
	client := api.New(cfg)
	health, err := client.Health(ctx)
	if err == nil {
		s.daemonRunning = true
		s.daemonHealth = health
		s.proxyReachable, s.proxyErr = checkProxy(ctx, cfg)
	} else {
		s.daemonErr = err
	}
	return s
}

func welcomePhase() Phase {
	return Phase{
		Title:   "Welcome",
		Summary: "vtunnel uses Cloudflare Tunnel for traffic and manages local routes dynamically.",
		Checks: []Check{
			{Label: "Cloudflare setup", Status: StatusOK, Detail: "configured once with wildcard DNS and ingress"},
			{Label: "Daily use", Status: StatusOK, Detail: "run vtunnel http <port> <subdomain>"},
		},
	}
}

func localPhase(s snapshot) Phase {
	checks := []Check{
		check(runtime.GOOS == "darwin", StatusWarn, "OS", runtime.GOOS, "macOS is the first supported target"),
		check(s.configExists, StatusAction, "vtunnel config", s.configPath, "not found; vtunnel can create it"),
		check(len(s.cfg.Domains) > 0, StatusAction, "vtunnel domains", strings.Join(s.cfg.Domains, ", "), "none configured"),
	}
	if apiListenIsLoopback(s.cfg) {
		checks = append(checks, Check{Label: "local API binding", Status: StatusOK, Detail: s.cfg.API.Listen})
	} else {
		checks = append(checks, Check{Label: "local API binding", Status: StatusWarn, Detail: "should bind to 127.0.0.1, got " + s.cfg.API.Listen})
	}
	if s.daemonRunning {
		checks = append(checks, Check{Label: "vtunnel daemon", Status: StatusOK, Detail: fmt.Sprintf("running, routes=%d logs=%d", s.daemonHealth.Routes, s.daemonHealth.Logs)})
	} else {
		checks = append(checks, Check{Label: "vtunnel daemon", Status: StatusAction, Detail: "stopped"})
	}
	if s.proxyReachable {
		checks = append(checks, Check{Label: "vtunnel proxy", Status: StatusOK, Detail: s.cfg.Proxy.Listen})
	} else if s.daemonRunning {
		checks = append(checks, Check{Label: "vtunnel proxy", Status: StatusWarn, Detail: errDetail(s.proxyErr)})
	}
	if s.cloudflared.Err != nil {
		checks = append(checks, Check{Label: "cloudflared", Status: StatusAction, Detail: "not found; run brew install cloudflared"})
	} else {
		checks = append(checks, Check{Label: "cloudflared", Status: StatusOK, Detail: s.cloudflared.Path})
		if s.cloudflared.LocalVersion != "" {
			checks = append(checks, Check{Label: "cloudflared local version", Status: StatusOK, Detail: s.cloudflared.LocalVersion})
		}
		if s.cloudflared.LatestErr != nil {
			checks = append(checks, Check{Label: "cloudflared latest Homebrew version", Status: StatusWarn, Detail: errDetail(s.cloudflared.LatestErr)})
		} else if s.cloudflared.UpdateAvailable() {
			checks = append(checks, Check{Label: "cloudflared update", Status: StatusAction, Detail: "local " + s.cloudflared.LocalVersion + ", latest " + s.cloudflared.LatestVersion})
		} else if s.cloudflared.LatestVersion != "" {
			checks = append(checks, Check{Label: "cloudflared latest Homebrew version", Status: StatusOK, Detail: s.cloudflared.LatestVersion})
		}
		if s.cloudflared.CertExists {
			checks = append(checks, Check{Label: "cloudflared login", Status: StatusOK, Detail: s.cloudflared.CertPath})
		} else {
			checks = append(checks, Check{Label: "cloudflared login", Status: StatusAction, Detail: "missing cert.pem; run vtunnel cloudflared login"})
		}
	}
	return Phase{Title: "Local checks", Summary: "Local dependencies, config, daemon and proxy status.", Checks: checks}
}

func cloudflarePhase(s snapshot) Phase {
	checks := []Check{}
	switch {
	case s.tokenMissing:
		checks = append(checks, Check{Label: "Cloudflare API token", Status: StatusWarn, Detail: "missing; DNS verification and API fixes are limited"})
	case s.tokenErr != nil:
		checks = append(checks, Check{Label: "Cloudflare API token", Status: StatusAction, Detail: errDetail(s.tokenErr)})
	default:
		status := s.tokenStatus.Status
		if status == "" {
			status = "verified"
		}
		checks = append(checks, Check{Label: "Cloudflare API token", Status: StatusOK, Detail: status})
	}
	if s.zonesErr != nil {
		checks = append(checks, Check{Label: "Cloudflare zones", Status: StatusAction, Detail: errDetail(s.zonesErr)})
	} else if len(s.zones) > 0 {
		checks = append(checks, Check{Label: "Cloudflare zones", Status: StatusOK, Detail: fmt.Sprintf("%d accessible", len(s.zones))})
	} else if !s.tokenMissing && s.tokenErr == nil {
		checks = append(checks, Check{Label: "Cloudflare zones", Status: StatusAction, Detail: "none accessible"})
	}
	return Phase{Title: "Cloudflare auth", Summary: "Token, permissions and accessible zones.", Checks: checks}
}

func discoveryPhase(s snapshot) Phase {
	checks := []Check{}
	if s.tokenMissing {
		checks = append(checks, Check{Label: "Cloudflare API", Status: StatusWarn, Detail: "limited discovery without token"})
	} else if s.tokenErr != nil {
		checks = append(checks, Check{Label: "Cloudflare API", Status: StatusAction, Detail: errDetail(s.tokenErr)})
	} else {
		checks = append(checks, Check{Label: "Cloudflare API", Status: StatusOK, Detail: "token verified"})
	}

	if s.zonesErr != nil {
		checks = append(checks, Check{Label: "zones", Status: StatusAction, Detail: errDetail(s.zonesErr)})
	} else if len(s.zones) == 0 && !s.tokenMissing && s.tokenErr == nil {
		checks = append(checks, Check{Label: "zones", Status: StatusAction, Detail: "no accessible zones"})
	} else if len(s.zones) > 0 {
		checks = append(checks, Check{Label: "zones", Status: StatusOK, Detail: fmt.Sprintf("%d accessible", len(s.zones))})
	}

	if s.cloudflared.TunnelListErr != nil {
		checks = append(checks, Check{Label: "local tunnels", Status: StatusAction, Detail: errDetail(s.cloudflared.TunnelListErr)})
	} else if len(s.cloudflared.Tunnels) > 0 {
		checks = append(checks, Check{Label: "local tunnels", Status: StatusOK, Detail: fmt.Sprintf("%d visible via cloudflared", len(s.cloudflared.Tunnels))})
	} else if s.cloudflared.CertExists {
		checks = append(checks, Check{Label: "local tunnels", Status: StatusWarn, Detail: "none visible via cloudflared"})
	}

	if s.dns.TokenMissing {
		checks = append(checks, Check{Label: "wildcard DNS records", Status: StatusWarn, Detail: "not inspected without API token"})
	} else if s.dns.Err != nil {
		checks = append(checks, Check{Label: "wildcard DNS records", Status: StatusWarn, Detail: errDetail(s.dns.Err)})
	} else if len(s.dns.Results) > 0 {
		ready := 0
		for _, result := range s.dns.Results {
			if result.Ready() {
				ready++
			}
		}
		checks = append(checks, Check{Label: "wildcard DNS records", Status: checkStatus(ready == len(s.dns.Results), StatusAction), Detail: fmt.Sprintf("%d/%d ready", ready, len(s.dns.Results))})
	}
	return Phase{Title: "Cloudflare discovery", Summary: "Discover accounts, zones, tunnels and wildcard DNS before making changes.", Checks: checks}
}

func domainPhase(s snapshot) Phase {
	checks := []Check{}
	if len(s.cfg.Domains) == 0 {
		checks = append(checks, Check{Label: "selected domains", Status: StatusAction, Detail: "none configured"})
	} else {
		checks = append(checks, Check{Label: "selected domains", Status: StatusOK, Detail: strings.Join(s.cfg.Domains, ", ")})
		if s.cfg.DefaultDomain != "" {
			checks = append(checks, Check{Label: "default domain", Status: StatusOK, Detail: s.cfg.DefaultDomain})
		}
	}
	if len(s.zones) > 0 {
		names := make([]string, 0, len(s.zones))
		for _, zone := range s.zones {
			names = append(names, zone.Name)
		}
		checks = append(checks, Check{Label: "available zones", Status: StatusOK, Detail: strings.Join(names, ", ")})
	}
	return Phase{Title: "Domain selection", Summary: "Pick the domains vtunnel can route wildcard traffic for.", Checks: checks}
}

func tunnelPhase(s snapshot) Phase {
	checks := []Check{}
	if s.cloudflared.TunnelListErr != nil {
		checks = append(checks, Check{Label: "cloudflared tunnels", Status: StatusAction, Detail: errDetail(s.cloudflared.TunnelListErr)})
	} else if len(s.cloudflared.Tunnels) > 0 {
		checks = append(checks, Check{Label: "cloudflared tunnels", Status: StatusOK, Detail: fmt.Sprintf("%d visible", len(s.cloudflared.Tunnels))})
		for _, tunnel := range s.cloudflared.Tunnels {
			if strings.TrimSpace(tunnel.DeletedAt) != "" {
				continue
			}
			checks = append(checks, Check{Label: "available tunnel", Status: StatusOK, Detail: tunnel.Name + " (" + tunnel.ID + ")"})
		}
	} else if s.cloudflared.CertExists {
		checks = append(checks, Check{Label: "cloudflared tunnels", Status: StatusAction, Detail: "none visible"})
	}
	if s.diag.Config.Tunnel == "" {
		checks = append(checks, Check{Label: "configured tunnel", Status: StatusAction, Detail: "missing from cloudflared config"})
	} else if tunnel, found := cf.FindTunnel(s.cloudflared.Tunnels, s.diag.Config.Tunnel); found {
		checks = append(checks, Check{Label: "configured tunnel exists", Status: StatusOK, Detail: tunnel.Name + " (" + tunnel.ID + ")"})
	} else {
		checks = append(checks, Check{Label: "configured tunnel", Status: StatusAction, Detail: s.diag.Config.Tunnel + " does not exist"})
	}
	return Phase{Title: "Tunnel", Summary: "Reuse an existing Cloudflare Tunnel or create a safe replacement.", Checks: checks}
}

func dnsPhase(s snapshot) Phase {
	checks := []Check{}
	if s.dns.TokenMissing {
		checks = append(checks, Check{Label: "wildcard DNS", Status: StatusWarn, Detail: "not verified without Cloudflare API token"})
		return Phase{Title: "DNS", Summary: "Wildcard DNS should point at the selected tunnel.", Checks: checks}
	}
	if s.dns.Err != nil {
		checks = append(checks, Check{Label: "wildcard DNS", Status: StatusWarn, Detail: errDetail(s.dns.Err)})
		return Phase{Title: "DNS", Summary: "Wildcard DNS should point at the selected tunnel.", Checks: checks}
	}
	for _, result := range s.dns.Results {
		switch {
		case result.Ready():
			checks = append(checks, Check{Label: "wildcard DNS", Status: StatusOK, Detail: result.ExpectedName + " CNAME -> " + expectedDNSContentLabel(result.ExpectedContent)})
		case !result.ZoneFound:
			checks = append(checks, Check{Label: "wildcard DNS", Status: StatusAction, Detail: result.ExpectedName + " zone not found"})
		case result.Err != nil:
			checks = append(checks, Check{Label: "wildcard DNS", Status: StatusAction, Detail: result.ExpectedName + " " + errDetail(result.Err)})
		case len(result.Records) == 0:
			checks = append(checks, Check{Label: "wildcard DNS", Status: StatusAction, Detail: "missing " + result.ExpectedName + " CNAME -> " + expectedDNSContentLabel(result.ExpectedContent)})
		default:
			checks = append(checks, Check{Label: "wildcard DNS", Status: StatusAction, Detail: result.ExpectedName + " exists but does not point to " + expectedDNSContentLabel(result.ExpectedContent)})
		}
	}
	return Phase{Title: "DNS", Summary: "Wildcard DNS should point at the selected tunnel.", Checks: checks}
}

func configPhase(s snapshot) Phase {
	checks := []Check{}
	if s.diagErr != nil {
		checks = append(checks, Check{Label: "cloudflared config", Status: StatusAction, Detail: errDetail(s.diagErr)})
		return Phase{Title: "Local cloudflared config", Summary: "Preserve existing ingress and add vtunnel wildcard rules.", Checks: checks}
	}
	if !s.diag.Exists {
		checks = append(checks, Check{Label: "cloudflared config", Status: StatusAction, Detail: "missing at " + config.ExpandPath(s.cfg.Cloudflared.ConfigPath)})
	} else {
		checks = append(checks, Check{Label: "cloudflared config", Status: StatusOK, Detail: config.ExpandPath(s.cfg.Cloudflared.ConfigPath)})
	}
	if s.diag.Config.Tunnel == "" {
		checks = append(checks, Check{Label: "tunnel id/name", Status: StatusAction, Detail: "missing"})
	} else {
		checks = append(checks, Check{Label: "tunnel id/name", Status: StatusOK, Detail: s.diag.Config.Tunnel})
	}
	if s.diag.Config.CredentialsFile == "" {
		checks = append(checks, Check{Label: "credentials-file", Status: StatusAction, Detail: "missing"})
	} else {
		checks = append(checks, Check{Label: "credentials-file", Status: StatusOK, Detail: s.diag.Config.CredentialsFile})
	}
	if s.planErr != nil {
		checks = append(checks, Check{Label: "write plan", Status: StatusAction, Detail: errDetail(s.planErr)})
	} else if len(s.plan.Changes) == 0 {
		checks = append(checks, Check{Label: "ingress plan", Status: StatusOK, Detail: "no changes needed"})
	} else {
		checks = append(checks, Check{Label: "ingress plan", Status: StatusAction, Detail: fmt.Sprintf("%d change(s) pending", len(s.plan.Changes))})
	}
	for _, change := range s.plan.Changes {
		checks = append(checks, Check{Label: "cloudflared change", Status: StatusAction, Detail: formatCloudflaredChange(change)})
	}
	for _, result := range s.diag.DomainResults {
		if result.Found && result.ServiceOK {
			checks = append(checks, Check{Label: "wildcard ingress", Status: StatusOK, Detail: result.ExpectedHostname + " -> " + result.ActualService})
		} else if result.Found {
			checks = append(checks, Check{Label: "wildcard ingress", Status: StatusAction, Detail: result.ExpectedHostname + " points to " + result.ActualService + ", expected " + result.ExpectedService})
		} else {
			checks = append(checks, Check{Label: "wildcard ingress", Status: StatusAction, Detail: "missing " + result.ExpectedHostname + " -> " + result.ExpectedService})
		}
	}
	return Phase{Title: "Local cloudflared config", Summary: "Preserve existing ingress and add vtunnel wildcard rules.", Checks: checks}
}

func runtimePhase(s snapshot) Phase {
	checks := []Check{}
	if s.process.Err != nil {
		checks = append(checks, Check{Label: "cloudflared process", Status: StatusWarn, Detail: errDetail(s.process.Err)})
	} else if s.process.RunningForConfig() {
		detail := "running with this config"
		if len(s.process.Matches) > 0 && s.process.Matches[0].PID > 0 {
			detail += fmt.Sprintf(" (pid %d)", s.process.Matches[0].PID)
		}
		checks = append(checks, Check{Label: "cloudflared process", Status: StatusOK, Detail: detail})
	} else if len(s.process.Processes) > 0 {
		checks = append(checks, Check{Label: "cloudflared process", Status: StatusAction, Detail: "running, but not with vtunnel config"})
	} else {
		checks = append(checks, Check{Label: "cloudflared process", Status: StatusAction, Detail: "not running"})
	}
	if s.daemonRunning {
		checks = append(checks, Check{Label: "vtunnel daemon", Status: StatusOK, Detail: "running"})
	} else {
		checks = append(checks, Check{Label: "vtunnel daemon", Status: StatusAction, Detail: "not running"})
	}
	return Phase{Title: "Runtime", Summary: "Start the local processes needed for daily tunneling.", Checks: checks}
}

func servicesPhase(s snapshot) Phase {
	checks := []Check{
		{Label: "macOS services", Status: StatusWarn, Detail: "run vtunnel service install after setup to start at login"},
		{Label: "vtunnel daemon service", Status: StatusWarn, Detail: "managed by LaunchAgent sh.vltn.vtunnel.daemon"},
		{Label: "cloudflared service", Status: StatusWarn, Detail: "managed by LaunchAgent sh.vltn.vtunnel.cloudflared"},
	}
	if runtime.GOOS != "darwin" {
		checks = []Check{{Label: "services", Status: StatusWarn, Detail: "automatic services are currently macOS-first"}}
	}
	if reportReady(s) {
		checks[0] = Check{Label: "macOS services", Status: StatusAction, Detail: "optional but recommended: vtunnel service install"}
	}
	return Phase{Title: "Services", Summary: "Install vtunnel and cloudflared as user services so they start at login.", Checks: checks}
}

func completionPhase(s snapshot) Phase {
	if reportReady(s) {
		return Phase{
			Title:   "Completion",
			Summary: "vtunnel is ready.",
		}
	}
	return Phase{
		Title:   "Completion",
		Summary: "A few checks still need attention before vtunnel is ready.",
		Checks:  []Check{{Label: "Next step", Status: StatusAction, Detail: "run the first available action, then recheck"}},
	}
}

// Canonical command definitions: the single source of truth for the wording of
// every command surfaced to users. The CLI welcome banner (CommandReference) and
// the onboarding completion step (CompletionCommands) both compose their lists
// from these, so a description can never drift between the two surfaces.
var (
	cmdOnboarding     = Command{Invocation: "vtunnel onboarding", Description: "Guided first-run setup"}
	cmdDashboard      = Command{Invocation: "vtunnel http", Description: "Open the request dashboard"}
	cmdTCP            = Command{Invocation: "vtunnel tcp", Description: "Expose a TCP service (Postgres, Redis, …)"}
	cmdSSH            = Command{Invocation: "vtunnel ssh", Description: "Open SSH in the browser (zero-install)"}
	cmdOrbstack       = Command{Invocation: "vtunnel orbstack", Description: "Expose OrbStack containers"}
	cmdAccess         = Command{Invocation: "vtunnel access", Description: "Protect routes with a login (Zero Trust)"}
	cmdMCP            = Command{Invocation: "vtunnel mcp", Description: "Run as an MCP server for AI agents"}
	cmdList           = Command{Invocation: "vtunnel list", Description: "List active routes"}
	cmdLogs           = Command{Invocation: "vtunnel logs dev", Description: "Show request logs"}
	cmdServiceInstall = Command{Invocation: "vtunnel service install", Description: "Start vtunnel automatically at login"}
	cmdStatus         = Command{Invocation: "vtunnel status", Description: "Show local status"}
	cmdHelp           = Command{Invocation: "vtunnel --help", Description: "Show every command and flag"}
)

// exposeCommand is the only command whose description varies: defaultDomain, when
// set, makes the example concrete (dev.example.com instead of dev.<domain>).
func exposeCommand(defaultDomain string) Command {
	host := "dev.<domain>"
	if defaultDomain != "" {
		host = "dev." + defaultDomain
	}
	return Command{Invocation: "vtunnel http 3000 dev", Description: "Expose localhost:3000 as " + host}
}

// CommandReference is the canonical "useful commands" list for the welcome banner
// shown by bare `vtunnel`. --help is omitted on purpose: the banner surfaces it as
// a closing call to action instead.
func CommandReference(defaultDomain string) []Command {
	return []Command{
		cmdOnboarding,
		cmdDashboard,
		exposeCommand(defaultDomain),
		cmdTCP,
		cmdSSH,
		cmdOrbstack,
		cmdAccess,
		cmdMCP,
		cmdList,
		cmdLogs,
		cmdServiceInstall,
		cmdStatus,
	}
}

// CompletionCommands is the curated subset shown at the end of onboarding, where
// `vtunnel onboarding` is redundant and --help doubles as the documentation entry.
func CompletionCommands(defaultDomain string) []Command {
	return []Command{
		exposeCommand(defaultDomain),
		cmdDashboard,
		cmdTCP,
		cmdSSH,
		cmdAccess,
		cmdMCP,
		cmdList,
		cmdLogs,
		cmdHelp,
	}
}

func completionCommands(s snapshot) []Command {
	if !reportReady(s) {
		return nil
	}
	domain := s.cfg.DefaultDomain
	if domain == "" && len(s.cfg.Domains) > 0 {
		domain = s.cfg.Domains[0]
	}
	return CompletionCommands(domain)
}

func completionNotes(s snapshot) []string {
	if !reportReady(s) {
		return []string{"Run the highlighted action, then press r to recheck."}
	}
	return []string{
		"Subdomains are created on demand — pick any name per project.",
		"Routes are local; your Cloudflare wildcard setup stays untouched.",
	}
}

func availableActions(s snapshot) []Action {
	var actions []Action
	if !s.configExists {
		actions = append(actions, Action{ID: "save-config", Label: "Create vtunnel config", Description: s.configPath, Mutates: true})
	}
	actions = append(actions, domainActions(s)...)
	actions = append(actions, localActions(s)...)
	if tunnelNeedsFix(s) {
		actions = append(actions, Action{ID: "fix-tunnel", Label: "Create/fix tunnel", Description: "Create a replacement tunnel and update local config.", Mutates: true})
	}
	if s.planErr == nil && len(s.plan.Changes) > 0 && len(s.cfg.Domains) > 0 {
		actions = append(actions, Action{ID: "write-cloudflared", Label: "Write cloudflared config", Description: "Backup then apply local ingress changes.", Mutates: true})
	}
	if dnsNeedsFix(s) {
		actions = append(actions, Action{ID: "fix-dns", Label: "Fix wildcard DNS", Description: "Create or update wildcard DNS records.", Mutates: true})
	}
	actions = append(actions, runtimeActions(s)...)
	actions = append(actions, authActions(s)...)
	return dedupeActions(actions)
}

func localActions(s snapshot) []Action {
	var actions []Action
	if s.cloudflared.Err != nil {
		actions = append(actions, Action{ID: "install-cloudflared", Label: "Install cloudflared manually", Description: "Run this outside vtunnel.", Command: "brew install cloudflared"})
	}
	if s.cloudflared.UpdateAvailable() {
		actions = append(actions, Action{ID: "update-cloudflared", Label: "Update cloudflared manually", Description: "Run this outside vtunnel.", Command: "brew upgrade cloudflared"})
	}
	if !s.cloudflared.CertExists && s.cloudflared.Err == nil {
		actions = append(actions, Action{ID: "login-cloudflared", Label: "Login to cloudflared manually", Description: "Run this outside vtunnel.", Command: "vtunnel cloudflared login"})
	}
	return actions
}

func authActions(s snapshot) []Action {
	if !s.tokenMissing && s.tokenErr == nil {
		return nil
	}
	return []Action{
		{ID: "open-token-url", Label: "Open token template", Description: "Open Cloudflare with vtunnel permissions pre-filled.", Command: TokenTemplateURL()},
		{ID: "store-token", Label: "Store API token", Description: "Paste and verify a Cloudflare API token.", Mutates: true, InputPrompt: "Cloudflare API token"},
	}
}

func domainActions(s snapshot) []Action {
	actions := []Action{
		{ID: "manage-domains", Label: "Manage domains", Description: "Open the domain manager.", Command: "manage domains"},
		{ID: "add-domain", Label: "Add a domain", Description: "Add another domain to vtunnel.", Mutates: true, InputPrompt: "Domain"},
	}
	if len(s.cfg.Domains) > 1 || (len(s.cfg.Domains) == 1 && s.cfg.DefaultDomain != s.cfg.Domains[0]) {
		actions = append(actions, Action{ID: "set-default-domain", Label: "Set default domain", Description: "Choose which configured domain is used by default.", Mutates: true, InputPrompt: "Default domain"})
	}
	return actions
}

func runtimeActions(s snapshot) []Action {
	var actions []Action
	if cloudflaredCanStart(s) {
		actions = append(actions, Action{ID: "start-cloudflared", Label: "Start cloudflared", Description: "Run cloudflared tunnel in the background.", Mutates: true})
	}
	if !s.daemonRunning {
		actions = append(actions, Action{ID: "start-daemon", Label: "Start vtunnel daemon", Description: "Start the local proxy/API daemon.", Mutates: true})
	}
	return actions
}

func serviceActions(s snapshot) []Action {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return []Action{{ID: "service-install-manual", Label: "Install login services", Description: "Run this after setup to start vtunnel automatically at login.", Command: "vtunnel service install"}}
}

func dedupeActions(actions []Action) []Action {
	seen := map[string]bool{}
	next := make([]Action, 0, len(actions))
	for _, action := range actions {
		if action.ID == "" || seen[action.ID] {
			continue
		}
		seen[action.ID] = true
		next = append(next, action)
	}
	return next
}

func wizardSteps(s snapshot) []Step {
	steps := []Step{
		stepFromPhase(StepWelcome, welcomePhase(), []Action{}, []string{
			"Cloudflare is configured once with wildcard DNS.",
			"Daily tunnels are local route changes, not Cloudflare changes.",
		}),
		stepFromPhase(StepLocal, localPhase(s), append(localActions(s), configActions(s)...), []string{
			"vtunnel keeps its API and proxy bound to 127.0.0.1.",
			"Go is not required after installing the binary.",
		}),
		stepFromPhase(StepAuth, cloudflarePhase(s), authActions(s), []string{
			"The API token is optional, but enables DNS verification and repair.",
			"vtunnel stores the token in macOS Keychain.",
		}),
		stepFromPhase(StepDiscovery, discoveryPhase(s), discoveryActions(s), []string{
			"Discovery is read-only.",
			"Existing Cloudflare resources are preferred when they are safe to reuse.",
		}),
		stepFromPhase(StepDomains, domainPhase(s), domainActions(s), []string{
			"You can return here later to add more domains.",
			"The default domain is used when --domain is omitted.",
		}),
		stepFromPhase(StepTunnel, tunnelPhase(s), tunnelActions(s), []string{
			"vtunnel reuses an existing tunnel when possible.",
			"Creating a tunnel updates local cloudflared config but does not touch daily routes.",
		}),
		stepFromPhase(StepDNS, dnsPhase(s), dnsActions(s), []string{
			"Wildcard DNS should point at the selected Cloudflare Tunnel.",
			"DNS is configured once per domain, not on every tunnel start.",
		}),
		stepFromPhase(StepConfig, configPhase(s), cloudflaredConfigActions(s), []string{
			"vtunnel preserves unrelated ingress rules.",
			"Config writes create a timestamped backup first.",
		}),
		stepFromPhase(StepServices, servicesPhase(s), serviceActions(s), []string{
			"Services are optional but recommended after setup is ready.",
			"They start vtunnel and cloudflared automatically when you log in.",
		}),
		stepFromPhase(StepHealth, runtimePhase(s), runtimeActions(s), []string{
			"Health checks verify the local daemon, proxy and cloudflared process.",
			"You can start processes manually here before installing services.",
		}),
		stepFromPhase(StepCompletion, completionPhase(s), []Action{}, completionNotes(s)),
	}
	steps[len(steps)-1].Commands = completionCommands(s)
	return steps
}

func stepFromPhase(id StepID, phase Phase, actions []Action, manual []string) Step {
	return Step{
		ID:          id,
		Title:       phase.Title,
		Summary:     phase.Summary,
		Description: stepDescription(id),
		Checks:      phase.Checks,
		Actions:     dedupeActions(actions),
		Manual:      manual,
	}
}

func stepDescription(id StepID) string {
	switch id {
	case StepWelcome:
		return "This guided setup walks through the full vtunnel configuration, even when parts are already ready."
	case StepLocal:
		return "Check local dependencies, paths and private loopback bindings."
	case StepAuth:
		return "Connect optional Cloudflare API access for safer DNS discovery and repair."
	case StepDiscovery:
		return "Read the current Cloudflare and cloudflared state before selecting anything."
	case StepDomains:
		return "Manage the domains vtunnel can use for wildcard local development URLs."
	case StepTunnel:
		return "Choose or create the Cloudflare Tunnel that receives wildcard traffic."
	case StepDNS:
		return "Verify each wildcard DNS record points at the selected tunnel."
	case StepConfig:
		return "Review and write the local cloudflared ingress config."
	case StepServices:
		return "Install session services so vtunnel is ready after login."
	case StepHealth:
		return "Start and verify the local runtime processes."
	case StepCompletion:
		return ""
	default:
		return ""
	}
}

func configActions(s snapshot) []Action {
	if !s.configExists {
		return []Action{{ID: "save-config", Label: "Create vtunnel config", Description: s.configPath, Mutates: true}}
	}
	return nil
}

func discoveryActions(s snapshot) []Action {
	if s.tokenMissing || s.tokenErr != nil {
		return authActions(s)
	}
	return nil
}

func tunnelActions(s snapshot) []Action {
	actions := []Action{
		{ID: "manage-tunnels", Label: "Manage tunnels", Description: "Open the tunnel manager.", Command: "manage tunnels"},
	}
	if tunnelNeedsFix(s) {
		actions = append(actions, Action{ID: "fix-tunnel", Label: "Create/fix tunnel", Description: "Create a replacement tunnel and update local config.", Mutates: true})
	}
	return actions
}

func dnsActions(s snapshot) []Action {
	if dnsNeedsFix(s) {
		return []Action{{ID: "fix-dns", Label: "Fix wildcard DNS", Description: "Create or update wildcard DNS records.", Mutates: true}}
	}
	return nil
}

func cloudflaredConfigActions(s snapshot) []Action {
	if s.planErr == nil && len(s.plan.Changes) > 0 && len(s.cfg.Domains) > 0 {
		return []Action{{ID: "write-cloudflared", Label: "Write cloudflared config", Description: "Backup then apply local ingress changes.", Mutates: true}}
	}
	return nil
}

func reportReady(s snapshot) bool {
	if len(s.cfg.Domains) == 0 || s.cloudflared.Err != nil || !s.cloudflared.CertExists || s.cloudflared.TunnelListErr != nil || s.cloudflared.UpdateAvailable() || s.diagErr != nil || s.planErr != nil {
		return false
	}
	if !s.configExists || !apiListenIsLoopback(s.cfg) || !s.daemonRunning || !s.proxyReachable || !s.process.RunningForConfig() {
		return false
	}
	if !s.diag.Exists || s.diag.Config.Tunnel == "" || s.diag.Config.CredentialsFile == "" {
		return false
	}
	if _, found := cf.FindTunnel(s.cloudflared.Tunnels, s.diag.Config.Tunnel); !found {
		return false
	}
	if len(s.plan.Changes) > 0 {
		return false
	}
	if !s.dns.TokenMissing && s.dns.Err == nil && len(s.dns.Results) > 0 && !s.dns.Ready() {
		return false
	}
	for _, result := range s.diag.DomainResults {
		if !result.Found || !result.ServiceOK {
			return false
		}
	}
	return true
}

func check(ok bool, failure Status, label string, okDetail string, failDetail string) Check {
	if ok {
		return Check{Label: label, Status: StatusOK, Detail: okDetail}
	}
	return Check{Label: label, Status: failure, Detail: failDetail}
}

func checkStatus(ok bool, failure Status) Status {
	if ok {
		return StatusOK
	}
	return failure
}

func addDomain(cfg config.Config, domain string) config.Config {
	domain = routes.NormalizeHostname(domain)
	if domain == "" {
		return cfg
	}
	for _, existing := range cfg.Domains {
		if routes.NormalizeHostname(existing) == domain {
			if cfg.DefaultDomain == "" {
				cfg.DefaultDomain = domain
			}
			return cfg
		}
	}
	cfg.Domains = append(cfg.Domains, domain)
	if cfg.DefaultDomain == "" {
		cfg.DefaultDomain = domain
	}
	return cfg
}

func setDefaultDomain(cfg config.Config, domain string) (config.Config, bool) {
	domain = routes.NormalizeHostname(domain)
	for _, existing := range cfg.Domains {
		if routes.NormalizeHostname(existing) == domain {
			cfg.DefaultDomain = domain
			return cfg, true
		}
	}
	return cfg, false
}

func removeDomain(cfg config.Config, domain string) (config.Config, bool) {
	domain = routes.NormalizeHostname(domain)
	if domain == "" {
		return cfg, false
	}
	next := make([]string, 0, len(cfg.Domains))
	removed := false
	for _, existing := range cfg.Domains {
		normalized := routes.NormalizeHostname(existing)
		if normalized == domain {
			removed = true
			continue
		}
		next = append(next, normalized)
	}
	if !removed {
		return cfg, false
	}
	cfg.Domains = next
	if routes.NormalizeHostname(cfg.DefaultDomain) == domain {
		cfg.DefaultDomain = ""
		if len(cfg.Domains) > 0 {
			cfg.DefaultDomain = cfg.Domains[0]
		}
	}
	return cfg, true
}

func renameDomain(cfg config.Config, from string, to string) (config.Config, bool) {
	from = routes.NormalizeHostname(from)
	to = routes.NormalizeHostname(to)
	if from == "" || to == "" {
		return cfg, false
	}
	found := false
	next := make([]string, 0, len(cfg.Domains))
	seen := map[string]struct{}{}
	for _, existing := range cfg.Domains {
		normalized := routes.NormalizeHostname(existing)
		if normalized == from {
			normalized = to
			found = true
		}
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		next = append(next, normalized)
	}
	if !found {
		return cfg, false
	}
	cfg.Domains = next
	if routes.NormalizeHostname(cfg.DefaultDomain) == from || cfg.DefaultDomain == "" {
		cfg.DefaultDomain = to
	}
	return cfg, true
}

func parseDomainRename(input string) (string, string, bool) {
	parts := strings.SplitN(input, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	from := routes.NormalizeHostname(parts[0])
	to := routes.NormalizeHostname(parts[1])
	return from, to, from != "" && to != ""
}

func apiListenIsLoopback(cfg config.Config) bool {
	host, _, err := net.SplitHostPort(cfg.API.Listen)
	return err == nil && host == "127.0.0.1"
}

func checkProxy(ctx context.Context, cfg config.Config) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cfg.Proxy.Listen, nil)
	if err != nil {
		return false, err
	}
	req.Host = "vtunnel-health.local"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	return true, nil
}

func errDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func formatCloudflaredChange(change cf.Change) string {
	switch change.Kind {
	case "create-config":
		return "create config: " + change.To
	case "add-ingress":
		return "add ingress: " + change.Hostname + " -> " + change.To
	case "update-ingress":
		return "update ingress: " + change.Hostname + " from " + change.From + " to " + change.To
	case "add-fallback":
		return "add fallback: " + change.To
	default:
		return change.Kind + ": " + change.From + " -> " + change.To
	}
}

func tunnelNeedsFix(s snapshot) bool {
	if s.cloudflared.Err != nil || !s.cloudflared.CertExists || s.cloudflared.TunnelListErr != nil {
		return false
	}
	if s.diag.Config.Tunnel == "" || s.diag.Config.CredentialsFile == "" {
		return true
	}
	_, found := cf.FindTunnel(s.cloudflared.Tunnels, s.diag.Config.Tunnel)
	return !found
}

func dnsNeedsFix(s snapshot) bool {
	if len(s.cfg.Domains) == 0 || strings.TrimSpace(s.diag.Config.Tunnel) == "" {
		return false
	}
	if s.dns.TokenMissing || s.dns.Err != nil || len(s.dns.Results) == 0 {
		return false
	}
	return !s.dns.Ready()
}

func cloudflaredCanStart(s snapshot) bool {
	if s.process.RunningForConfig() || s.process.Err != nil || len(s.process.Processes) > 0 {
		return false
	}
	if s.cloudflared.Err != nil || s.diagErr != nil || !s.diag.Exists || s.diag.Config.Tunnel == "" || s.diag.Config.CredentialsFile == "" {
		return false
	}
	if s.planErr != nil || len(s.plan.Changes) > 0 {
		return false
	}
	_, found := cf.FindTunnel(s.cloudflared.Tunnels, s.diag.Config.Tunnel)
	return found
}

func inspectDNSWithClient(ctx context.Context, client *cfapi.Client, cfg config.Config, diag cf.Diagnostic, zones []cfapi.Zone, zonesErr error) DNSInspection {
	if len(cfg.Domains) == 0 {
		return DNSInspection{}
	}
	if zonesErr != nil {
		return DNSInspection{Err: zonesErr}
	}
	zonesByName := map[string]cfapi.Zone{}
	for _, zone := range zones {
		zonesByName[routes.NormalizeHostname(zone.Name)] = zone
	}
	inspection := DNSInspection{}
	expectedContent := expectedTunnelDNSContent(diag)
	for _, domain := range cfg.Domains {
		domain = routes.NormalizeHostname(domain)
		result := WildcardDNSResult{
			Domain:          domain,
			ExpectedName:    "*." + domain,
			ExpectedContent: expectedContent,
		}
		zone, ok := zonesByName[domain]
		if !ok {
			inspection.Results = append(inspection.Results, result)
			continue
		}
		result.Zone = zone
		result.ZoneFound = true
		result.Records, result.Err = client.ListDNSRecords(ctx, zone.ID, cfapi.DNSRecordFilter{Name: result.ExpectedName})
		inspection.Results = append(inspection.Results, result)
	}
	return inspection
}

func newCloudflareClient(token string) (*cfapi.Client, error) {
	options := []cfapi.Option{}
	if baseURL := strings.TrimSpace(os.Getenv(cfapi.BaseURLEnv)); baseURL != "" {
		options = append(options, cfapi.WithBaseURL(baseURL))
	}
	return cfapi.New(token, options...)
}

func TokenTemplateURL() string {
	permissions := []map[string]string{
		{"key": "account_settings", "type": "read"},
		{"key": "zone", "type": "read"},
		{"key": "dns", "type": "edit"},
		{"key": "access", "type": "read"},
		{"key": "access_acct", "type": "read"},
		{"key": "cloudflare_one_connectors", "type": "read"},
	}
	encodedPermissions, _ := json.Marshal(permissions)
	values := url.Values{}
	values.Set("permissionGroupKeys", string(encodedPermissions))
	values.Set("accountId", "*")
	values.Set("zoneId", "all")
	values.Set("name", "vtunnel Cloudflare API Token")
	return "https://dash.cloudflare.com/profile/api-tokens?" + values.Encode()
}

func InspectCloudflared(ctx context.Context) Cloudflared {
	path, err := exec.LookPath("cloudflared")
	if err != nil {
		return Cloudflared{CertPath: cloudflaredOriginCertPath(), Err: err}
	}
	inspection := Cloudflared{Path: path, CertPath: cloudflaredOriginCertPath()}
	if _, err := os.Stat(inspection.CertPath); err == nil {
		inspection.CertExists = true
	}
	versionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, path, "--version").CombinedOutput()
	if err != nil {
		inspection.VersionOutput = fmt.Sprintf("version unavailable: %v", err)
	} else {
		inspection.VersionOutput = strings.TrimSpace(string(output))
		inspection.LocalVersion = extractCloudflaredVersion(inspection.VersionOutput)
	}
	inspection.LatestVersion, inspection.LatestErr = inspectHomebrewCloudflaredLatest(ctx)
	if inspection.CertExists {
		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		inspection.Tunnels, inspection.TunnelListErr = cf.NewTunnelRunner(path).List(listCtx)
	}
	return inspection
}

func cloudflaredOriginCertPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloudflared", "cert.pem")
}

func inspectHomebrewCloudflaredLatest(ctx context.Context) (string, error) {
	brewPath, err := exec.LookPath("brew")
	if err != nil {
		return "", errors.New("brew not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, brewPath, "info", "--json=v2", "cloudflared").CombinedOutput()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("brew info cloudflared: %s", strings.TrimSpace(string(output)))
	}
	var info struct {
		Formulae []struct {
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		} `json:"formulae"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return "", fmt.Errorf("parse brew info cloudflared: %w", err)
	}
	if len(info.Formulae) == 0 || info.Formulae[0].Versions.Stable == "" {
		return "", errors.New("brew did not return a stable version")
	}
	return info.Formulae[0].Versions.Stable, nil
}

func extractCloudflaredVersion(output string) string {
	for _, field := range strings.Fields(output) {
		version := strings.Trim(field, "(),")
		if version == "" {
			continue
		}
		first := version[0]
		if first >= '0' && first <= '9' {
			return version
		}
	}
	return ""
}

func InspectProcess(ctx context.Context, cloudflaredPath string, cloudflaredConfigPath string, tunnelRef string) ProcessInspection {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return ProcessInspection{Err: err}
	}
	inspection := ProcessInspection{}
	for _, line := range strings.Split(string(output), "\n") {
		process, ok := parseProcessLine(line)
		if !ok {
			continue
		}
		inspection.Processes = append(inspection.Processes, process)
		if processMatches(process.Command, cloudflaredPath, cloudflaredConfigPath, tunnelRef) {
			inspection.Matches = append(inspection.Matches, process)
		}
	}
	return inspection
}

func parseProcessLine(line string) (Process, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Process{}, false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Process{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return Process{}, false
	}
	executable := filepath.Base(fields[1])
	if executable != "cloudflared" && !strings.HasSuffix(fields[1], "/cloudflared") {
		return Process{}, false
	}
	command := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	return Process{PID: pid, Command: command}, true
}

func processMatches(command string, cloudflaredPath string, cloudflaredConfigPath string, tunnelRef string) bool {
	args := strings.Fields(command)
	if len(args) == 0 {
		return false
	}
	executable := filepath.Base(args[0])
	if executable != "cloudflared" && !strings.HasSuffix(args[0], "/cloudflared") && !strings.EqualFold(args[0], cloudflaredPath) {
		return false
	}
	if !containsArg(args, "tunnel") || !containsArg(args, "run") {
		return false
	}
	configPath := strings.TrimSpace(config.ExpandPath(cloudflaredConfigPath))
	if configPath != "" && usesConfig(args, configPath) {
		return true
	}
	tunnelRef = strings.TrimSpace(tunnelRef)
	return tunnelRef != "" && containsArg(args, tunnelRef)
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func usesConfig(args []string, configPath string) bool {
	configPath = filepath.Clean(configPath)
	for index, arg := range args {
		switch {
		case arg == "--config" && index+1 < len(args):
			if samePath(args[index+1], configPath) {
				return true
			}
		case strings.HasPrefix(arg, "--config="):
			if samePath(strings.TrimPrefix(arg, "--config="), configPath) {
				return true
			}
		}
	}
	return false
}

func samePath(left string, right string) bool {
	left = strings.TrimSpace(config.ExpandPath(left))
	right = strings.TrimSpace(config.ExpandPath(right))
	if left == "" || right == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func StartCloudflared(ctx context.Context, cloudflaredPath string, cloudflaredConfigPath string) (StartResult, error) {
	_ = ctx
	logPath, err := cloudflaredLogPath()
	if err != nil {
		return StartResult{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return StartResult{}, fmt.Errorf("open cloudflared log: %w", err)
	}
	command := exec.CommandContext(context.Background(), cloudflaredPath, "--config", cloudflaredConfigPath, "tunnel", "run")
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return StartResult{}, fmt.Errorf("start cloudflared: %w", err)
	}
	_ = logFile.Close()
	return StartResult{PID: command.Process.Pid, LogPath: logPath}, nil
}

func cloudflaredLogPath() (string, error) {
	logs, err := config.LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(logs, "cloudflared.log"), nil
}

func StartDaemon(ctx context.Context, cfg config.Config, configPath string) error {
	client := api.New(cfg)
	if _, err := client.Health(ctx); err == nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find current executable: %w", err)
	}
	logPath, err := daemonLogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	args := []string{"daemon"}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	command := exec.CommandContext(context.Background(), exe, args...)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	_ = logFile.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Health(ctx); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready; see %s", logPath)
}

func daemonLogPath() (string, error) {
	logs, err := config.LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(logs, "daemon.log"), nil
}

type TunnelFixResult struct {
	TunnelName      string
	TunnelID        string
	CredentialsFile string
	BackupPath      string
	Skipped         bool
}

func (engine *Engine) fixTunnel(ctx context.Context, s snapshot) (TunnelFixResult, error) {
	if s.cloudflared.Err != nil {
		return TunnelFixResult{}, errors.New("cannot fix tunnel without cloudflared")
	}
	if !s.cloudflared.CertExists {
		return TunnelFixResult{}, errors.New("cannot fix tunnel without cloudflared login")
	}
	if s.cloudflared.TunnelListErr != nil {
		return TunnelFixResult{}, fmt.Errorf("cannot fix tunnel because tunnels cannot be listed: %w", s.cloudflared.TunnelListErr)
	}
	if configuredTunnelExists(s.diag, s.cloudflared) {
		tunnel, _ := cf.FindTunnel(s.cloudflared.Tunnels, s.diag.Config.Tunnel)
		return TunnelFixResult{TunnelName: tunnel.Name, TunnelID: tunnel.ID, Skipped: true}, nil
	}
	name := tunnelNameForFix(s.cfg, s.diag)
	created, err := engine.deps.createCloudflaredTunnel(ctx, s.cloudflared.Path, name)
	if err != nil {
		return TunnelFixResult{}, err
	}
	writeResult, err := engine.deps.writeTunnelConfig(cf.TunnelConfigUpdate{
		Path:            config.ExpandPath(s.cfg.Cloudflared.ConfigPath),
		Tunnel:          created.ID,
		CredentialsFile: created.CredentialsFile,
	})
	if err != nil {
		return TunnelFixResult{}, err
	}
	return TunnelFixResult{
		TunnelName:      created.Name,
		TunnelID:        created.ID,
		CredentialsFile: created.CredentialsFile,
		BackupPath:      writeResult.BackupPath,
	}, nil
}

type DNSFixResult struct {
	Method  string
	Changes []string
	Skipped []string
}

func (engine *Engine) fixDNS(ctx context.Context, s snapshot) (DNSFixResult, error) {
	if len(s.cfg.Domains) == 0 {
		return DNSFixResult{}, nil
	}
	if strings.TrimSpace(s.diag.Config.Tunnel) == "" {
		return DNSFixResult{}, errors.New("cannot fix DNS without a configured cloudflared tunnel")
	}
	expectedContent := expectedTunnelDNSContent(s.diag)
	if expectedContent == "" {
		return DNSFixResult{}, errors.New("cannot infer Cloudflare tunnel DNS target from configured tunnel")
	}
	token, err := engine.deps.readToken()
	if err == nil {
		client, err := engine.deps.cloudflareClient(token)
		if err == nil && !s.dns.TokenMissing && s.dns.Err == nil {
			return fixDNSWithAPI(ctx, client, s.dns, expectedContent)
		}
	}
	return fixDNSWithCloudflared(ctx, s.cfg, s.diag, s.cloudflared)
}

func fixDNSWithAPI(ctx context.Context, client *cfapi.Client, dns DNSInspection, expectedContent string) (DNSFixResult, error) {
	result := DNSFixResult{Method: "Cloudflare API"}
	for _, dnsResult := range dns.Results {
		if dnsResult.Ready() {
			result.Skipped = append(result.Skipped, dnsResult.ExpectedName+" already points to "+expectedContent)
			continue
		}
		if !dnsResult.ZoneFound {
			return result, fmt.Errorf("cannot fix DNS for %s: Cloudflare zone not found", dnsResult.Domain)
		}
		if dnsResult.Err != nil {
			return result, fmt.Errorf("cannot fix DNS for %s: %w", dnsResult.Domain, dnsResult.Err)
		}
		input := cfapi.DNSRecordInput{Type: "CNAME", Name: dnsResult.ExpectedName, Content: expectedContent, Proxied: true, TTL: 1}
		switch len(dnsResult.Records) {
		case 0:
			record, err := client.CreateDNSRecord(ctx, dnsResult.Zone.ID, input)
			if err != nil {
				return result, err
			}
			result.Changes = append(result.Changes, "created "+record.Name+" CNAME -> "+record.Content)
		case 1:
			record, err := client.UpdateDNSRecord(ctx, dnsResult.Zone.ID, dnsResult.Records[0].ID, input)
			if err != nil {
				return result, err
			}
			result.Changes = append(result.Changes, "updated "+record.Name+" CNAME -> "+record.Content)
		default:
			return result, fmt.Errorf("cannot safely fix %s via API: multiple DNS records found", dnsResult.ExpectedName)
		}
	}
	return result, nil
}

func fixDNSWithCloudflared(ctx context.Context, cfg config.Config, diag cf.Diagnostic, cloudflared Cloudflared) (DNSFixResult, error) {
	if cloudflared.Err != nil {
		return DNSFixResult{}, errors.New("cannot fix DNS without cloudflared")
	}
	if !cloudflared.CertExists {
		return DNSFixResult{}, errors.New("cannot fix DNS without cloudflared login")
	}
	if cloudflared.TunnelListErr != nil {
		return DNSFixResult{}, fmt.Errorf("cannot fix DNS because tunnels cannot be listed: %w", cloudflared.TunnelListErr)
	}
	if !configuredTunnelExists(diag, cloudflared) {
		return DNSFixResult{}, errors.New("cannot fix DNS because configured tunnel does not exist")
	}
	result := DNSFixResult{Method: "cloudflared"}
	runner := cf.NewTunnelRunner(cloudflared.Path)
	for _, domain := range cfg.Domains {
		hostname := "*." + routes.NormalizeHostname(domain)
		if err := runner.RouteDNS(ctx, diag.Config.Tunnel, hostname, true); err != nil {
			return result, err
		}
		result.Changes = append(result.Changes, "routed "+hostname+" to "+diag.Config.Tunnel)
	}
	return result, nil
}

func startConfiguredCloudflared(ctx context.Context, cfg config.Config, cloudflared Cloudflared, process ProcessInspection, diag cf.Diagnostic, diagErr error, plan cf.Plan, planErr error, start func(context.Context, string, string) (StartResult, error)) (StartResult, error) {
	if process.RunningForConfig() {
		return StartResult{PID: process.Matches[0].PID, Skipped: true}, nil
	}
	if len(process.Processes) > 0 {
		return StartResult{}, errors.New("cloudflared is already running, but vtunnel cannot confirm it uses this config")
	}
	if cloudflared.Err != nil {
		return StartResult{}, errors.New("cannot start cloudflared because it is not installed")
	}
	if diagErr != nil || !diag.Exists {
		return StartResult{}, errors.New("cannot start cloudflared without a readable config")
	}
	if diag.Config.Tunnel == "" || diag.Config.CredentialsFile == "" {
		return StartResult{}, errors.New("cannot start cloudflared without tunnel and credentials-file in config")
	}
	if !configuredTunnelExists(diag, cloudflared) {
		return StartResult{}, errors.New("cannot start cloudflared because the configured tunnel does not exist")
	}
	if planErr != nil {
		return StartResult{}, fmt.Errorf("cannot start cloudflared because the config plan is invalid: %w", planErr)
	}
	if len(plan.Changes) > 0 {
		return StartResult{}, errors.New("cannot start cloudflared before applying local config changes")
	}
	return start(ctx, cloudflared.Path, config.ExpandPath(cfg.Cloudflared.ConfigPath))
}

func configuredTunnelExists(diag cf.Diagnostic, cloudflared Cloudflared) bool {
	if strings.TrimSpace(diag.Config.Tunnel) == "" {
		return false
	}
	_, found := cf.FindTunnel(cloudflared.Tunnels, diag.Config.Tunnel)
	return found
}

func tunnelNameForFix(cfg config.Config, diag cf.Diagnostic) string {
	if strings.TrimSpace(cfg.Cloudflared.TunnelName) != "" {
		return strings.TrimSpace(cfg.Cloudflared.TunnelName)
	}
	if tunnel := strings.TrimSpace(diag.Config.Tunnel); tunnel != "" && !looksLikeUUID(tunnel) {
		return tunnel
	}
	return "vtunnel"
}

func defaultTunnelCredentialsFile(tunnelID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloudflared", strings.TrimSpace(tunnelID)+".json")
}

func expectedTunnelDNSContent(diag cf.Diagnostic) string {
	tunnel := strings.TrimSpace(diag.Config.Tunnel)
	if tunnel == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(tunnel), ".cfargotunnel.com") {
		return tunnel
	}
	if looksLikeUUID(tunnel) {
		return tunnel + ".cfargotunnel.com"
	}
	return ""
}

func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			isDigit := char >= '0' && char <= '9'
			isLowerHex := char >= 'a' && char <= 'f'
			isUpperHex := char >= 'A' && char <= 'F'
			if !isDigit && !isLowerHex && !isUpperHex {
				return false
			}
		}
	}
	return true
}

func dnsRecordMatchesTunnel(record cfapi.DNSRecord, expectedContent string) bool {
	if !strings.EqualFold(record.Type, "CNAME") {
		return false
	}
	if expectedContent == "" {
		return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(record.Content, ".")), ".cfargotunnel.com")
	}
	return strings.EqualFold(strings.TrimSuffix(record.Content, "."), strings.TrimSuffix(expectedContent, "."))
}

func expectedDNSContentLabel(content string) string {
	if content == "" {
		return "<tunnel>.cfargotunnel.com"
	}
	return content
}
