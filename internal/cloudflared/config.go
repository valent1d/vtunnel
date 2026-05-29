package cloudflared

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"vtunnel/internal/routes"
)

type Config struct {
	Tunnel          string        `yaml:"tunnel"`
	CredentialsFile string        `yaml:"credentials-file"`
	Ingress         []IngressRule `yaml:"ingress"`
}

type IngressRule struct {
	Hostname string `yaml:"hostname"`
	Service  string `yaml:"service"`
}

type Diagnostic struct {
	Path            string
	Exists          bool
	Config          Config
	DomainResults   []DomainResult
	ExpectedService string
}

type DomainResult struct {
	Domain           string
	ExpectedHostname string
	ExpectedService  string
	Found            bool
	ServiceOK        bool
	ActualService    string
}

type Plan struct {
	Path            string
	Exists          bool
	ExpectedService string
	Config          Config
	Changes         []Change
}

type Change struct {
	Kind     string
	Hostname string
	From     string
	To       string
}

type WriteResult struct {
	Plan       Plan
	BackupPath string
}

type TunnelConfigUpdate struct {
	Path            string
	Tunnel          string
	CredentialsFile string
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse cloudflared config %s: %w", path, err)
	}
	return cfg, nil
}

func Diagnose(path string, domains []string, proxyListen string) (Diagnostic, error) {
	diag := Diagnostic{
		Path:            path,
		ExpectedService: "http://" + proxyListen,
	}

	cfg, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return diag, nil
	}
	if err != nil {
		return diag, err
	}

	diag.Exists = true
	diag.Config = cfg
	for _, domain := range normalizeDomains(domains) {
		diag.DomainResults = append(diag.DomainResults, checkDomain(cfg, domain, diag.ExpectedService))
	}
	return diag, nil
}

func MissingIngressRules(diag Diagnostic) []DomainResult {
	var missing []DomainResult
	for _, result := range diag.DomainResults {
		if !result.Found || !result.ServiceOK {
			missing = append(missing, result)
		}
	}
	return missing
}

func PlanConfig(path string, domains []string, proxyListen string) (Plan, error) {
	expectedService := "http://" + proxyListen
	plan := Plan{
		Path:            path,
		ExpectedService: expectedService,
	}

	cfg, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg = Config{}
		plan.Changes = append(plan.Changes, Change{Kind: "create-config", To: path})
	} else if err != nil {
		return plan, err
	} else {
		plan.Exists = true
	}

	for _, domain := range normalizeDomains(domains) {
		hostname := "*." + domain
		index := findIngressIndex(cfg, hostname)
		if index < 0 {
			cfg.Ingress = insertBeforeFallback(cfg.Ingress, IngressRule{
				Hostname: hostname,
				Service:  expectedService,
			})
			plan.Changes = append(plan.Changes, Change{Kind: "add-ingress", Hostname: hostname, To: expectedService})
			continue
		}
		if !servicesEquivalent(cfg.Ingress[index].Service, expectedService) {
			from := cfg.Ingress[index].Service
			cfg.Ingress[index].Service = expectedService
			plan.Changes = append(plan.Changes, Change{Kind: "update-ingress", Hostname: hostname, From: from, To: expectedService})
		}
	}

	if len(cfg.Ingress) > 0 && !hasFallbackRule(cfg.Ingress) {
		cfg.Ingress = append(cfg.Ingress, IngressRule{Service: "http_status:404"})
		plan.Changes = append(plan.Changes, Change{Kind: "add-fallback", To: "http_status:404"})
	}
	if len(cfg.Ingress) == 0 && len(normalizeDomains(domains)) > 0 {
		cfg.Ingress = append(cfg.Ingress, IngressRule{Service: "http_status:404"})
		plan.Changes = append(plan.Changes, Change{Kind: "add-fallback", To: "http_status:404"})
	}

	plan.Config = cfg
	return plan, nil
}

func WritePlan(plan Plan, now time.Time) (WriteResult, error) {
	result := WriteResult{Plan: plan}
	if len(plan.Changes) == 0 {
		return result, nil
	}

	if err := os.MkdirAll(filepath.Dir(plan.Path), 0o755); err != nil {
		return result, fmt.Errorf("create cloudflared config directory: %w", err)
	}
	if plan.Exists {
		backupPath := fmt.Sprintf("%s.bak-%s", plan.Path, now.Format("20060102-150405"))
		data, err := os.ReadFile(plan.Path)
		if err != nil {
			return result, fmt.Errorf("read existing cloudflared config: %w", err)
		}
		if err := os.WriteFile(backupPath, data, 0o644); err != nil {
			return result, fmt.Errorf("write cloudflared config backup: %w", err)
		}
		result.BackupPath = backupPath
	}

	data, err := yaml.Marshal(plan.Config)
	if err != nil {
		return result, fmt.Errorf("encode cloudflared config: %w", err)
	}
	if err := os.WriteFile(plan.Path, data, 0o644); err != nil {
		return result, fmt.Errorf("write cloudflared config: %w", err)
	}
	return result, nil
}

func WriteTunnelConfigUpdate(update TunnelConfigUpdate, now time.Time) (WriteResult, error) {
	path := strings.TrimSpace(update.Path)
	if path == "" {
		return WriteResult{}, errors.New("cloudflared config path is required")
	}

	plan := Plan{Path: path}
	cfg, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg = Config{}
		plan.Changes = append(plan.Changes, Change{Kind: "create-config", To: path})
	} else if err != nil {
		return WriteResult{Plan: plan}, err
	} else {
		plan.Exists = true
	}

	if strings.TrimSpace(update.Tunnel) != "" && cfg.Tunnel != update.Tunnel {
		plan.Changes = append(plan.Changes, Change{Kind: "update-tunnel", From: cfg.Tunnel, To: update.Tunnel})
		cfg.Tunnel = update.Tunnel
	}
	if strings.TrimSpace(update.CredentialsFile) != "" && cfg.CredentialsFile != update.CredentialsFile {
		plan.Changes = append(plan.Changes, Change{Kind: "update-credentials-file", From: cfg.CredentialsFile, To: update.CredentialsFile})
		cfg.CredentialsFile = update.CredentialsFile
	}

	plan.Config = cfg
	return WritePlan(plan, now)
}

func checkDomain(cfg Config, domain string, expectedService string) DomainResult {
	expectedHostname := "*." + domain
	result := DomainResult{
		Domain:           domain,
		ExpectedHostname: expectedHostname,
		ExpectedService:  expectedService,
	}
	for _, rule := range cfg.Ingress {
		if routes.NormalizeHostname(rule.Hostname) != expectedHostname {
			continue
		}
		result.Found = true
		result.ActualService = strings.TrimSpace(rule.Service)
		result.ServiceOK = servicesEquivalent(result.ActualService, expectedService)
		return result
	}
	return result
}

func findIngressIndex(cfg Config, hostname string) int {
	hostname = routes.NormalizeHostname(hostname)
	for index, rule := range cfg.Ingress {
		if routes.NormalizeHostname(rule.Hostname) == hostname {
			return index
		}
	}
	return -1
}

func insertBeforeFallback(rules []IngressRule, rule IngressRule) []IngressRule {
	out := make([]IngressRule, 0, len(rules)+1)
	inserted := false
	for _, existing := range rules {
		if !inserted && isFallbackRule(existing) {
			out = append(out, rule)
			inserted = true
		}
		out = append(out, existing)
	}
	if !inserted {
		out = append(out, rule)
	}
	return out
}

func hasFallbackRule(rules []IngressRule) bool {
	for _, rule := range rules {
		if isFallbackRule(rule) {
			return true
		}
	}
	return false
}

func isFallbackRule(rule IngressRule) bool {
	return strings.TrimSpace(rule.Hostname) == "" && strings.HasPrefix(normalizeService(rule.Service), "http_status:")
}

func servicesEquivalent(actual string, expected string) bool {
	actual = normalizeService(actual)
	expected = normalizeService(expected)
	if actual == expected {
		return true
	}
	return strings.ReplaceAll(actual, "localhost", "127.0.0.1") == strings.ReplaceAll(expected, "localhost", "127.0.0.1")
}

func normalizeService(service string) string {
	service = strings.TrimSpace(service)
	service = strings.TrimRight(service, "/")
	return strings.ToLower(service)
}

func normalizeDomains(domains []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = routes.NormalizeHostname(domain)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, domain)
	}
	return out
}
