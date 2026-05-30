package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"vtunnel/internal/api"
	cfapi "vtunnel/internal/cloudflare"
	cf "vtunnel/internal/cloudflared"
	"vtunnel/internal/config"
	"vtunnel/internal/daemon"
	"vtunnel/internal/httpui"
	"vtunnel/internal/launchd"
	"vtunnel/internal/onboarding"
	onboardingtui "vtunnel/internal/onboarding/tui"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
	"vtunnel/internal/secrets"
	"vtunnel/internal/uninstall"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

var runHTTPUI = httpui.Run

var launchdRunner = launchd.ExecRunner

func Execute() error {
	return NewRootCommand().Execute()
}

func NewRootCommand() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:          "vtunnel",
		Short:        "Pleasant local tunnels powered by Cloudflare Tunnel",
		Version:      versionInfo(),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			printWelcome(cmd.OutOrStdout())
			if !onboardingComplete(configPath) {
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), onboardingBanner())
			}
			return nil
		},
	}
	root.SetVersionTemplate("vtunnel {{.Version}}\n")
	root.PersistentFlags().StringVar(&configPath, "config", "", "config file path")

	root.AddCommand(
		newHTTPCommand(&configPath),
		newListCommand(&configPath),
		newLogsCommand(&configPath),
		newStopCommand(&configPath),
		newStatusCommand(&configPath),
		newSetupCommand(&configPath),
		newOnboardingCommand(&configPath),
		newCloudflareCommand(),
		newCloudflaredCommand(),
		newServiceCommand(&configPath),
		newDaemonCommand(&configPath),
		newOrbstackCommand(&configPath),
		newAccessCommand(&configPath),
		newUninstallCommand(&configPath),
	)

	applyBrandedHelpTemplate(root)

	return root
}

func applyBrandedHelpTemplate(cmd *cobra.Command) {
	cmd.SetHelpTemplate(brandedHelpTemplate())
	for _, child := range cmd.Commands() {
		applyBrandedHelpTemplate(child)
	}
}

func brandedHelpTemplate() string {
	return brandWelcome() + `

{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}`
}

func versionInfo() string {
	parts := []string{version}
	if commit != "" {
		parts = append(parts, "commit "+commit)
	}
	if date != "" {
		parts = append(parts, "built "+date)
	}
	return strings.Join(parts, " ")
}

func printWelcome(out io.Writer) {
	fmt.Fprintln(out, brandWelcome())
	fmt.Fprintf(out, "Version: %s\n", versionInfo())
	fmt.Fprintln(out, "Copyright (c) 2026 vtunnel by vltn.sh")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Pleasant local tunnels powered by Cloudflare Tunnel.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Useful commands:")
	commands := onboarding.CommandReference("")
	width := 0
	for _, command := range commands {
		if len(command.Invocation) > width {
			width = len(command.Invocation)
		}
	}
	for _, command := range commands {
		fmt.Fprintf(out, "  %-*s  %s\n", width, command.Invocation, command.Description)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run `vtunnel --help` for all commands.")
}

func brandWelcome() string {
	logo := strings.TrimRight(`
  _   __________  ___  ___  ________
 | | / /_  __/ / / / |/ / |/ / __/ /
 | |/ / / / / /_/ /    /    / _// /__
 |___/ /_/  \____/_/|_/_/|_/___/____/
                                      `, "\n")
	// lipgloss v2 always emits color codes; gate on the detected profile so the
	// banner stays plain when stdout is not a color terminal (pipes, NO_COLOR).
	if cliColorProfile < colorprofile.ANSI {
		return logo
	}
	return brandStyleCLI.Render(logo)
}

var (
	brandStyleCLI   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48"))
	cliColorProfile = colorprofile.Detect(os.Stdout, os.Environ())
)

// onboardingComplete reports whether the user has already run the setup wizard,
// detected by the presence of the vtunnel config file. It mirrors the path
// resolution in internal/config so the first-run banner disappears for good
// once onboarding has written a config.
func onboardingComplete(configPath string) bool {
	path := strings.TrimSpace(configPath)
	if path == "" {
		resolved, err := config.ConfigPath()
		if err != nil {
			return false
		}
		path = resolved
	}
	_, err := os.Stat(path)
	return err == nil
}

// onboardingBanner renders a prominent, boxed call to action pointing first-run
// users at the setup wizard. Color is gated on the detected profile, matching
// brandWelcome, so piped/NO_COLOR output stays plain.
func onboardingBanner() string {
	box := renderBox([]string{
		"Welcome to vtunnel — one step left!",
		"",
		"Run the guided setup:",
		"    vtunnel onboarding",
	})
	if cliColorProfile < colorprofile.ANSI {
		return box
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("48")).Render(box)
}

// renderBox draws a rounded Unicode box around the given lines, padded to the
// widest line. lipgloss.Width is used for measurement so wide runes align.
func renderBox(lines []string) string {
	width := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > width {
			width = w
		}
	}
	rule := strings.Repeat("─", width+2)
	var b strings.Builder
	b.WriteString("╭" + rule + "╮\n")
	for _, line := range lines {
		pad := strings.Repeat(" ", width-lipgloss.Width(line))
		b.WriteString("│ " + line + pad + " │\n")
	}
	b.WriteString("╰" + rule + "╯")
	return b.String()
}

func newCloudflareCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloudflare",
		Short: "Inspect Cloudflare API access",
	}
	cmd.AddCommand(
		newCloudflareStatusCommand(),
		newCloudflareAuthCommand(),
	)
	return cmd
}

func newCloudflareStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show read-only Cloudflare API discovery",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, source, err := newCloudflareClientFromKeychain()
			if errors.Is(err, cfapi.ErrMissingToken) {
				fmt.Fprintln(cmd.OutOrStdout(), "Cloudflare API token: missing")
				fmt.Fprintf(cmd.OutOrStdout(), "Run: vtunnel cloudflare auth\n")
				return nil
			}
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Cloudflare API token source: %s\n", source)
			discovery := cfapi.Discover(cmd.Context(), client)
			cloudflared := inspectCloudflaredForCLI()
			printCloudflareDiscovery(cmd.OutOrStdout(), discovery, cloudflared)
			return nil
		},
	}
}

func newCloudflareAuthCommand() *cobra.Command {
	var token string
	var printURL bool
	var noOpen bool
	var accessWrite bool

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Create and store an optional Cloudflare API token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			templateURL := cloudflareTokenTemplateURL(accessWrite)
			fmt.Fprintln(cmd.OutOrStdout(), "Cloudflare API token helper")
			fmt.Fprintln(cmd.OutOrStdout(), "This token is optional; vtunnel prefers cloudflared login for tunnel setup.")
			if accessWrite {
				fmt.Fprintln(cmd.OutOrStdout(), "Includes Access write (edit) so vtunnel can create Access apps and policies:")
				fmt.Fprintln(cmd.OutOrStdout(), "  • Access: Apps and Policies — Edit")
				fmt.Fprintln(cmd.OutOrStdout(), "  • Access: Organizations, Identity Providers, and Groups — Edit")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Template URL:")
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", templateURL)

			hasTokenFlag := strings.TrimSpace(token) != ""
			if printURL {
				return nil
			}
			if !noOpen && !hasTokenFlag {
				if err := openURL(templateURL); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Could not open browser automatically: %v\n", err)
				}
			}

			token = strings.TrimSpace(token)
			if token == "" {
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprint(cmd.OutOrStdout(), "Paste the generated Cloudflare API token: ")
				scanner := bufio.NewScanner(cmd.InOrStdin())
				if scanner.Scan() {
					token = strings.TrimSpace(scanner.Text())
				}
				if err := scanner.Err(); err != nil {
					return err
				}
			}
			if token == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "No token stored.")
				return nil
			}

			client, err := newCloudflareClientForAuth(token)
			if err != nil {
				return err
			}
			status, err := client.VerifyToken(cmd.Context())
			if err != nil {
				return fmt.Errorf("verify user API token before storing: %w", err)
			}
			if err := writeCloudflareTokenToKeychain(token); err != nil {
				return err
			}
			storedToken, err := readCloudflareTokenFromKeychain()
			if err != nil {
				return fmt.Errorf("read stored token from macOS Keychain: %w", err)
			}
			if storedToken != token {
				return errors.New("stored token readback mismatch")
			}
			storedClient, err := newCloudflareClientForAuth(storedToken)
			if err != nil {
				return err
			}
			if _, err := storedClient.VerifyToken(cmd.Context()); err != nil {
				return fmt.Errorf("verify stored token from macOS Keychain: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Stored Cloudflare API token in macOS Keychain.")
			if status.Status != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Token status: %s\n", status.Status)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next: go run ./cmd/vtunnel cloudflare status")
			fmt.Fprintln(cmd.OutOrStdout(), "Once installed: vtunnel cloudflare status")
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Cloudflare API token to store without prompting")
	cmd.Flags().BoolVar(&printURL, "print-url", false, "print the token template URL and exit")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open the browser")
	cmd.Flags().BoolVar(&accessWrite, "access", false, "also request Access edit, so vtunnel can protect routes with Cloudflare Access")
	return cmd
}

func newCloudflaredCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloudflared",
		Short: "Manage native cloudflared authentication",
	}
	cmd.AddCommand(
		newCloudflaredStatusCommand(),
		newCloudflaredLoginCommand(),
	)
	return cmd
}

func newCloudflaredStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show native cloudflared login status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			inspection := inspectCloudflared()
			if inspection.Err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "cloudflared: not found")
				fmt.Fprintln(cmd.OutOrStdout(), "Install with: brew install cloudflared")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cloudflared: %s\n", inspection.Path)
			if inspection.LocalVersion != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "version: %s\n", inspection.LocalVersion)
			}
			if inspection.CertExists {
				fmt.Fprintf(cmd.OutOrStdout(), "login: connected (%s)\n", inspection.CertPath)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "login: missing (%s)\n", inspection.CertPath)
				fmt.Fprintln(cmd.OutOrStdout(), "Run: vtunnel cloudflared login")
			}
			return nil
		},
	}
}

func newCloudflaredLoginCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Run cloudflared tunnel login and create cert.pem",
		RunE: func(cmd *cobra.Command, _ []string) error {
			inspection := inspectCloudflared()
			if inspection.Err != nil {
				return fmt.Errorf("cloudflared not found; install with: brew install cloudflared")
			}
			if inspection.CertExists && !force {
				fmt.Fprintf(cmd.OutOrStdout(), "cloudflared login: already connected (%s)\n", inspection.CertPath)
				fmt.Fprintln(cmd.OutOrStdout(), "Use --force to run cloudflared tunnel login again.")
				return nil
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Opening Cloudflare login through cloudflared...")
			fmt.Fprintln(cmd.OutOrStdout(), "After login, Cloudflare will write cert.pem locally.")
			login := exec.CommandContext(cmd.Context(), inspection.Path, "tunnel", "login")
			login.Stdin = cmd.InOrStdin()
			login.Stdout = cmd.OutOrStdout()
			login.Stderr = cmd.ErrOrStderr()
			if err := login.Run(); err != nil {
				return err
			}

			certPath := cloudflaredOriginCertPath()
			if _, err := os.Stat(certPath); err != nil {
				return fmt.Errorf("cloudflared login finished but cert.pem was not found at %s", certPath)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cloudflared login: connected (%s)\n", certPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "run cloudflared tunnel login even if cert.pem already exists")
	return cmd
}

func newCloudflareClientFromKeychain() (*cfapi.Client, string, error) {
	token, err := readCloudflareTokenFromKeychain()
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, "", cfapi.ErrMissingToken
	}
	if err != nil {
		return nil, "", err
	}
	client, err := newCloudflareClientWithEnvOptions(token)
	return client, "macOS Keychain", err
}

var readCloudflareTokenFromKeychain = func() (string, error) {
	return secrets.DefaultStore().Get(secrets.CloudflareToken)
}

var writeCloudflareTokenToKeychain = func(token string) error {
	return secrets.DefaultStore().Set(secrets.CloudflareToken, token)
}

var deleteCloudflareTokenFromKeychain = func() error {
	return secrets.DefaultStore().Delete(secrets.CloudflareToken)
}

var inspectCloudflaredForCLI = inspectCloudflared

var inspectCloudflaredProcessForCLI = inspectCloudflaredProcess

var startCloudflaredForCLI = startCloudflared

func newCloudflareClientForAuth(token string) (*cfapi.Client, error) {
	options := []cfapi.Option{}
	if baseURL := strings.TrimSpace(os.Getenv(cfapi.BaseURLEnv)); baseURL != "" {
		options = append(options, cfapi.WithBaseURL(baseURL))
	}
	return cfapi.New(token, options...)
}

func newCloudflareClientWithEnvOptions(token string) (*cfapi.Client, error) {
	options := []cfapi.Option{}
	if baseURL := strings.TrimSpace(os.Getenv(cfapi.BaseURLEnv)); baseURL != "" {
		options = append(options, cfapi.WithBaseURL(baseURL))
	}
	return cfapi.New(token, options...)
}

func cloudflareTokenTemplateURL(includeAccessWrite bool) string {
	// Access protection (creating apps/policies/IdPs) needs edit on the two
	// Access groups. We request edit (which implies read) only when asked, to
	// keep the default token least-privilege.
	accessType := "read"
	if includeAccessWrite {
		accessType = "edit"
	}
	permissions := []map[string]string{
		{"key": "account_settings", "type": "read"},
		{"key": "zone", "type": "read"},
		{"key": "dns", "type": "edit"},
		{"key": "access", "type": accessType},
		{"key": "access_acct", "type": accessType},
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

func openURL(rawURL string) error {
	return exec.Command("open", rawURL).Start()
}

func newHTTPCommand(configPath *string) *cobra.Command {
	var domain string
	var detach bool
	var target string

	cmd := &cobra.Command{
		Use:   "http [port|target] [subdomain]",
		Short: "Open the HTTP tunnel dashboard or expose a local or remote HTTP service",
		Long: "Open the HTTP tunnel dashboard, or expose an HTTP service.\n\n" +
			"With no arguments, opens the dashboard. Given a port, forwards a local\n" +
			"service (http://127.0.0.1:<port>). The first argument may also be a full\n" +
			"URL or host:port (e.g. http://web.orb.local) to forward to any HTTP\n" +
			"upstream — see also `vtunnel orbstack` for OrbStack containers.",
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if err := config.EnsureDirs(); err != nil {
				return err
			}

			useTarget := strings.TrimSpace(target) != ""
			if len(args) == 0 && !useTarget {
				if err := ensureDaemon(cmd.Context(), cfg, *configPath); err != nil {
					return err
				}
				return runHTTPUI(cmd.Context(), cfg, "")
			}

			var routeTarget, subdomain string
			if useTarget {
				routeTarget, err = normalizeTargetURL(target)
				if len(args) >= 1 {
					subdomain = args[0]
				}
			} else {
				routeTarget, err = resolveHTTPTarget(args[0])
				if len(args) == 2 {
					subdomain = args[1]
				}
			}
			if err != nil {
				return err
			}

			hostname, err := hostnameForRoute(subdomain, domain, cfg)
			if err != nil {
				return err
			}

			runtimeCfg := configForRouteDomain(cfg, domain)
			engine := newCLIOnboardingEngine(*configPath, runtimeCfg)
			if result, err := engine.EnsureHTTPRuntime(cmd.Context()); err != nil {
				return err
			} else if result.Cloudflared != nil && !result.Cloudflared.Skipped {
				fmt.Fprintf(cmd.OutOrStdout(), "cloudflared: started (pid %d)\n", result.Cloudflared.PID)
			}

			client := api.New(cfg)

			route := routes.Route{
				Hostname: hostname,
				Target:   routeTarget,
			}
			if err := client.AddRoute(cmd.Context(), route); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Forwarding https://%s -> %s\n", hostname, route.Target)
			if detach {
				return nil
			}
			return runHTTPUI(cmd.Context(), cfg, hostname)
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain to use for this route")
	cmd.Flags().BoolVar(&detach, "detach", false, "create the route and return instead of following request logs")
	cmd.Flags().StringVar(&target, "target", "", "upstream URL or host:port to forward to (instead of a local port)")
	return cmd
}

// resolveHTTPTarget turns the first positional argument of `vtunnel http` into a
// proxy target: a bare port becomes http://127.0.0.1:<port>, anything else is
// treated as a URL or host:port.
func resolveHTTPTarget(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if _, err := strconv.Atoi(arg); err == nil {
		port, perr := normalizePort(arg)
		if perr != nil {
			return "", perr
		}
		return localHTTPPortTarget(port), nil
	}
	return normalizeTargetURL(arg)
}

// normalizeTargetURL validates an upstream target and defaults the scheme to
// http:// when omitted (so "web.orb.local" and "web.orb.local:8080" both work).
func normalizeTargetURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("target is required")
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid target %q: %w", value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("target must use http or https, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid target %q: missing host", value)
	}
	return parsed.String(), nil
}

func newListCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List routes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			client := api.New(cfg)

			list, err := client.ListRoutes(cmd.Context())
			if err != nil {
				list, err = readSavedRoutes()
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon is not running; showing saved routes.")
			}
			printRoutes(cmd.OutOrStdout(), list)
			return nil
		},
	}
}

func newLogsCommand(configPath *string) *cobra.Command {
	var follow bool
	var limit int

	cmd := &cobra.Command{
		Use:   "logs [hostname-or-subdomain]",
		Short: "Show request logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}

			hostname := ""
			if len(args) == 1 {
				hostname, err = hostnameForStop(args[0], cfg)
				if err != nil {
					return err
				}
			}

			client := api.New(cfg)
			if follow {
				return followLogs(cmd.Context(), cmd.OutOrStdout(), client, hostname)
			}

			entries, err := client.ListLogs(cmd.Context(), requestlog.Filter{
				Hostname: hostname,
				Limit:    limit,
			})
			if err != nil {
				logStore, storeErr := savedRequestLogStore()
				if storeErr != nil {
					return err
				}
				entries = logStore.List(requestlog.Filter{
					Hostname: hostname,
					Limit:    limit,
				})
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon is not running; showing saved request logs.")
			}
			printLogEntries(cmd.OutOrStdout(), entries)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow request logs")
	cmd.Flags().IntVar(&limit, "limit", 50, "number of log entries to show")
	return cmd
}

func newStopCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <hostname-or-subdomain>",
		Short: "Remove a route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			hostname, err := hostnameForStop(args[0], cfg)
			if err != nil {
				return err
			}

			client := api.New(cfg)
			if err := client.DeleteRoute(cmd.Context(), hostname); err != nil {
				store, storeErr := savedRouteStore()
				if storeErr != nil {
					return err
				}
				deleted, deleteErr := store.Delete(hostname)
				if deleteErr != nil {
					return deleteErr
				}
				if !deleted {
					return err
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", hostname)
			return nil
		},
	}
}

func newStatusCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon and config status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			path := *configPath
			if path == "" {
				path, _ = config.ConfigPath()
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Config: %s\n", path)
			fmt.Fprintf(cmd.OutOrStdout(), "Proxy:  %s\n", cfg.Proxy.Listen)
			fmt.Fprintf(cmd.OutOrStdout(), "API:    %s\n", cfg.API.Listen)
			if cfg.DefaultDomain == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Domain: not configured")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Domain: %s\n", cfg.DefaultDomain)
			}
			if len(cfg.Domains) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Domains: %s\n", strings.Join(cfg.Domains, ", "))
			}

			routesPath, _ := config.RoutesPath()
			requestLogsPath, _ := config.RequestLogsPath()
			daemonLogPath, _ := daemonLogPath()
			fmt.Fprintf(cmd.OutOrStdout(), "Routes: %s\n", routesPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Requests: %s\n", requestLogsPath)
			fmt.Fprintf(cmd.OutOrStdout(), "Daemon log: %s\n", daemonLogPath)

			health, err := api.New(cfg).Health(cmd.Context())
			if err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon: stopped")
				printSavedCounts(cmd.OutOrStdout())
				printCloudflaredSummary(cmd.OutOrStdout(), cfg)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Daemon: running")
			fmt.Fprintf(cmd.OutOrStdout(), "Active routes: %d\n", health.Routes)
			fmt.Fprintf(cmd.OutOrStdout(), "Logged requests: %d\n", health.Logs)
			printCloudflaredSummary(cmd.OutOrStdout(), cfg)
			return nil
		},
	}
}

func newSetupCommand(configPath *string) *cobra.Command {
	var setupDomains []string
	var writeCloudflared bool
	var fixTunnel bool
	var fixDNS bool
	var startCloudflared bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Run local setup diagnostics",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := config.EnsureDirs(); err != nil {
				return err
			}
			path := *configPath
			if path == "" {
				var err error
				path, err = config.ConfigPath()
				if err != nil {
					return err
				}
			}

			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			if len(setupDomains) > 0 {
				cfg = applySetupDomains(cfg, setupDomains)
			}

			_, statErr := os.Stat(path)
			configCreated := errors.Is(statErr, os.ErrNotExist)
			if configCreated {
				if err := config.Save(path, cfg); err != nil {
					return err
				}
			} else if statErr != nil {
				return fmt.Errorf("inspect config %s: %w", path, statErr)
			} else {
				if len(setupDomains) > 0 {
					if err := config.Save(path, cfg); err != nil {
						return err
					}
				}
			}

			engine := newCLIOnboardingEngine(path, cfg)
			var notices []string
			for _, actionID := range setupActionIDs(writeCloudflared, fixTunnel, fixDNS, startCloudflared) {
				notice, err := engine.Execute(cmd.Context(), actionID, "")
				if err != nil {
					return err
				}
				notices = append(notices, notice)
			}
			onboarding.PrintSetupText(cmd.OutOrStdout(), engine.Report(cmd.Context()), onboarding.SetupTextOptions{
				ConfigPath:    path,
				ConfigCreated: configCreated,
				Notices:       notices,
			})
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&setupDomains, "domain", nil, "domain to add to vtunnel config")
	cmd.Flags().BoolVar(&writeCloudflared, "write-cloudflared", false, "write missing or incorrect local cloudflared ingress rules")
	cmd.Flags().BoolVar(&fixTunnel, "fix-tunnel", false, "create a cloudflared tunnel when the configured tunnel is missing")
	cmd.Flags().BoolVar(&fixDNS, "fix-dns", false, "create or update Cloudflare wildcard DNS records")
	cmd.Flags().BoolVar(&startCloudflared, "start-cloudflared", false, "start cloudflared with the configured tunnel config")
	return cmd
}

func setupActionIDs(writeCloudflared bool, fixTunnel bool, fixDNS bool, startCloudflared bool) []string {
	var actions []string
	if writeCloudflared {
		actions = append(actions, "write-cloudflared")
	}
	if fixTunnel {
		actions = append(actions, "fix-tunnel")
	}
	if fixDNS {
		actions = append(actions, "fix-dns")
	}
	if startCloudflared {
		actions = append(actions, "start-cloudflared")
	}
	return actions
}

func newOnboardingCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "onboarding",
		Aliases: []string{"onboard"},
		Short:   "Open the guided first-run setup wizard",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := config.EnsureDirs(); err != nil {
				return err
			}
			path := *configPath
			if path == "" {
				var err error
				path, err = config.ConfigPath()
				if err != nil {
					return err
				}
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			engine := newCLIOnboardingEngine(path, cfg)
			return onboardingtui.Run(cmd.Context(), engine)
		},
	}
}

func newServiceCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install and manage macOS user services",
	}
	cmd.AddCommand(
		newServiceInstallCommand(configPath),
		newServiceStatusCommand(configPath),
		newServiceStartCommand(configPath),
		newServiceStopCommand(configPath),
		newServiceUninstallCommand(configPath),
	)
	return cmd
}

func newServiceInstallCommand(configPath *string) *cobra.Command {
	var vtunnelBin string
	var cloudflaredBin string
	var noStart bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install vtunnel and cloudflared as macOS LaunchAgents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, specs, err := serviceInstallContext(*configPath, vtunnelBin, cloudflaredBin)
			if err != nil {
				return err
			}
			if err := manager.Install(cmd.Context(), specs, !noStart); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Installed vtunnel services:")
			printServicePaths(cmd.OutOrStdout(), manager, specs)
			if noStart {
				fmt.Fprintln(cmd.OutOrStdout(), "Services installed but not started.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Services started.")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Run: vtunnel service status")
			return nil
		},
	}
	cmd.Flags().StringVar(&vtunnelBin, "vtunnel-bin", "", "vtunnel binary path to use in the LaunchAgent")
	cmd.Flags().StringVar(&cloudflaredBin, "cloudflared-bin", "", "cloudflared binary path to use in the LaunchAgent")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "write LaunchAgents without starting them")
	return cmd
}

func newServiceStatusCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show macOS LaunchAgent status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, specs, err := serviceRuntimeContext(*configPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "vtunnel services:")
			for _, spec := range specs {
				status := manager.Status(cmd.Context(), spec)
				state := "not installed"
				switch {
				case status.Loaded:
					state = "loaded"
				case status.Exists:
					state = "installed, stopped"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", spec.Name, state)
				fmt.Fprintf(cmd.OutOrStdout(), "    label: %s\n", spec.Label)
				fmt.Fprintf(cmd.OutOrStdout(), "    plist: %s\n", status.Path)
			}
			return nil
		},
	}
}

func newServiceStartCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start installed macOS LaunchAgents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, specs, err := serviceRuntimeContext(*configPath)
			if err != nil {
				return err
			}
			if err := manager.Start(cmd.Context(), specs); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Services started.")
			return nil
		},
	}
}

func newServiceStopCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop installed macOS LaunchAgents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, specs, err := serviceRuntimeContext(*configPath)
			if err != nil {
				return err
			}
			for _, spec := range specs {
				if err := manager.Stop(cmd.Context(), []launchd.Spec{spec}); err != nil {
					status := manager.Status(cmd.Context(), spec)
					if status.Exists && !status.Loaded {
						continue
					}
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Services stopped.")
			return nil
		},
	}
}

func newServiceUninstallCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove macOS LaunchAgents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			manager, specs, err := serviceRuntimeContext(*configPath)
			if err != nil {
				return err
			}
			if err := manager.Uninstall(cmd.Context(), specs); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Services uninstalled.")
			return nil
		},
	}
}

// uninstallRuntime holds the live handles BuildPlan resolved so the action
// hooks reuse them instead of re-inspecting.
type uninstallRuntime struct {
	cfg      config.Config
	manager  launchd.Manager
	specs    []launchd.Spec
	cfClient *cfapi.Client
}

func newUninstallCommand(configPath *string) *cobra.Command {
	var includeCloudflare bool
	var assumeYes bool
	var dryRun bool
	var keepConfig bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove vtunnel services, local config, and optionally Cloudflare resources",
		Long: "Tear down what vtunnel installed: macOS LaunchAgents, the local config\n" +
			"directory and Keychain token, and — with --cloudflare — the Cloudflare\n" +
			"tunnel and wildcard DNS records it created. The vtunnel binary itself is\n" +
			"removed separately with `brew uninstall vtunnel`.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			opts := uninstall.Options{IncludeCloudflare: includeCloudflare, KeepConfig: keepConfig}

			interactive := !assumeYes
			// Inspect Cloudflare when it might be removed: requested via flag, or
			// interactive so we can offer it and show what would go.
			inspectCF := includeCloudflare || interactive

			plan, rt, err := buildUninstallPlan(cmd.Context(), *configPath, opts, inspectCF)
			if err != nil {
				return err
			}

			printUninstallPlan(out, plan)

			if dryRun {
				fmt.Fprintln(out, "\nDry run — nothing was removed.")
				return nil
			}

			reader := bufio.NewReader(cmd.InOrStdin())

			if interactive && !opts.IncludeCloudflare && plan.Cloudflare.Resolved {
				if confirmPrompt(reader, out, "Also delete the Cloudflare tunnel and DNS records?", false) {
					opts.IncludeCloudflare = true
					plan.Options.IncludeCloudflare = true
				}
			}

			if interactive && !confirmPrompt(reader, out, "Proceed with uninstall?", false) {
				fmt.Fprintln(out, "Aborted.")
				return nil
			}

			report := uninstall.Execute(cmd.Context(), plan, uninstallActions(rt))
			printUninstallReport(out, report)

			fmt.Fprintln(out, "\nThe vtunnel binary is still installed. Remove it with:")
			fmt.Fprintln(out, "  brew uninstall vtunnel")

			if report.Failed() {
				return errors.New("uninstall completed with errors (see above)")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&includeCloudflare, "cloudflare", false, "also delete the Cloudflare tunnel and wildcard DNS records")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip confirmation prompts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without changing anything")
	cmd.Flags().BoolVar(&keepConfig, "keep-config", false, "keep ~/.config/vtunnel and the Keychain token")
	return cmd
}

func buildUninstallPlan(ctx context.Context, configPath string, opts uninstall.Options, inspectCF bool) (uninstall.Plan, *uninstallRuntime, error) {
	path := strings.TrimSpace(config.ExpandPath(configPath))
	if path == "" {
		var err error
		path, err = config.ConfigPath()
		if err != nil {
			return uninstall.Plan{}, nil, err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return uninstall.Plan{}, nil, err
	}

	rt := &uninstallRuntime{cfg: cfg}
	plan := uninstall.Plan{Options: opts}

	if _, err := api.New(cfg).Health(ctx); err == nil {
		plan.DaemonRunning = true
	}

	if runtime.GOOS == "darwin" {
		if manager, specs, serr := serviceRuntimeContext(path); serr == nil {
			rt.manager = manager
			rt.specs = specs
			for _, spec := range specs {
				status := manager.Status(ctx, spec)
				plan.Services = append(plan.Services, uninstall.Service{
					Name:      spec.Name,
					Label:     spec.Label,
					PlistPath: status.Path,
					Installed: status.Exists,
					Loaded:    status.Loaded,
				})
			}
		}
	}

	if dir, derr := config.ConfigDir(); derr == nil {
		plan.Local = append(plan.Local, uninstall.LocalPath{
			Label:   "Config directory " + dir,
			Path:    dir,
			Present: pathExists(dir),
		})
	}

	if _, terr := readCloudflareTokenFromKeychain(); terr == nil {
		plan.Token = true
	}

	if inspectCF {
		plan.Cloudflare = buildCloudflareUninstallPlan(ctx, cfg, rt)
	}

	return plan, rt, nil
}

func buildCloudflareUninstallPlan(ctx context.Context, cfg config.Config, rt *uninstallRuntime) uninstall.Cloudflare {
	cfPath := strings.TrimSpace(config.ExpandPath(cfg.Cloudflared.ConfigPath))
	if cfPath == "" {
		return uninstall.Cloudflare{Reason: "no cloudflared config path configured"}
	}
	cfCfg, err := cf.Load(cfPath)
	if errors.Is(err, os.ErrNotExist) {
		return uninstall.Cloudflare{Reason: "cloudflared config not found"}
	}
	if err != nil {
		return uninstall.Cloudflare{Reason: err.Error()}
	}
	tunnelRef := strings.TrimSpace(cfCfg.Tunnel)
	if tunnelRef == "" {
		return uninstall.Cloudflare{Reason: "no tunnel configured"}
	}

	result := uninstall.Cloudflare{Resolved: true, TunnelRef: tunnelRef}
	tunnelID := tunnelRef
	if tunnels, terr := cf.NewTunnelRunner("").List(ctx); terr == nil {
		if tunnel, found := cf.FindTunnel(tunnels, tunnelRef); found {
			result.TunnelName = tunnel.Name
			result.TunnelRef = tunnel.ID
			tunnelID = tunnel.ID
		}
	}

	creds := strings.TrimSpace(cfCfg.CredentialsFile)
	if creds == "" {
		home, _ := os.UserHomeDir()
		creds = filepath.Join(home, ".cloudflared", tunnelID+".json")
	}
	result.Creds = uninstall.LocalPath{
		Label:   "Tunnel credentials " + creds,
		Path:    creds,
		Present: pathExists(creds),
	}

	client, _, cerr := newCloudflareClientFromKeychain()
	if cerr != nil {
		result.Reason = "DNS not inspected (" + cerr.Error() + ")"
		return result
	}
	rt.cfClient = client

	zones, zerr := client.ListZones(ctx)
	if zerr != nil {
		result.Reason = "DNS not inspected (" + zerr.Error() + ")"
		return result
	}
	zonesByName := map[string]cfapi.Zone{}
	for _, zone := range zones {
		zonesByName[routes.NormalizeHostname(zone.Name)] = zone
	}

	expected := tunnelID + ".cfargotunnel.com"
	for _, domain := range uninstallDomains(cfg) {
		zone, ok := zonesByName[domain]
		if !ok {
			continue
		}
		records, rerr := client.ListDNSRecords(ctx, zone.ID, cfapi.DNSRecordFilter{Name: "*." + domain, Type: "CNAME"})
		if rerr != nil {
			continue
		}
		for _, record := range records {
			if strings.EqualFold(strings.TrimSuffix(record.Content, "."), expected) {
				result.Records = append(result.Records, uninstall.DNSRecord{
					ZoneID:   zone.ID,
					RecordID: record.ID,
					Hostname: record.Name,
				})
			}
		}
	}
	return result
}

func uninstallActions(rt *uninstallRuntime) uninstall.Actions {
	cfg := rt.cfg
	actions := uninstall.Actions{
		StopDaemon:   func(ctx context.Context) error { return api.New(cfg).Shutdown(ctx) },
		RemovePath:   func(path string) error { return os.RemoveAll(path) },
		DeleteToken:  deleteCloudflareTokenFromKeychain,
		DeleteTunnel: func(ctx context.Context, ref string) error { return cf.NewTunnelRunner("").Delete(ctx, ref, true) },
	}
	if len(rt.specs) > 0 {
		manager := rt.manager
		specs := rt.specs
		actions.RemoveServices = func(ctx context.Context) error { return manager.Uninstall(ctx, specs) }
	}
	if rt.cfClient != nil {
		client := rt.cfClient
		actions.DeleteDNS = func(ctx context.Context, record uninstall.DNSRecord) error {
			return client.DeleteDNSRecord(ctx, record.ZoneID, record.RecordID)
		}
	}
	return actions
}

func uninstallDomains(cfg config.Config) []string {
	seen := map[string]bool{}
	var domains []string
	add := func(domain string) {
		domain = routes.NormalizeHostname(strings.TrimSpace(domain))
		if domain == "" || seen[domain] {
			return
		}
		seen[domain] = true
		domains = append(domains, domain)
	}
	add(cfg.DefaultDomain)
	for _, domain := range cfg.Domains {
		add(domain)
	}
	return domains
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func printUninstallPlan(out io.Writer, plan uninstall.Plan) {
	fmt.Fprintln(out, "vtunnel uninstall will remove:")
	fmt.Fprintln(out)

	fmt.Fprintln(out, "  macOS LaunchAgents")
	installed := plan.InstalledServices()
	if len(installed) == 0 {
		fmt.Fprintln(out, "    (none installed)")
	} else {
		for _, svc := range installed {
			state := "stopped"
			if svc.Loaded {
				state = "running"
			}
			fmt.Fprintf(out, "    • %s (%s)\n      %s\n", svc.Name, state, svc.PlistPath)
		}
	}

	if plan.Options.KeepConfig {
		fmt.Fprintln(out, "  Local data: kept (--keep-config)")
	} else {
		fmt.Fprintln(out, "  Local data")
		found := false
		for _, item := range plan.Local {
			if item.Present {
				fmt.Fprintf(out, "    • %s\n", item.Label)
				found = true
			}
		}
		if plan.Token {
			fmt.Fprintln(out, "    • Cloudflare API token (macOS Keychain)")
			found = true
		}
		if !found {
			fmt.Fprintln(out, "    (nothing found)")
		}
	}

	if plan.Options.IncludeCloudflare {
		fmt.Fprintln(out, "  Cloudflare account resources")
		if !plan.Cloudflare.Resolved {
			reason := plan.Cloudflare.Reason
			if reason == "" {
				reason = "nothing to remove"
			}
			fmt.Fprintf(out, "    (%s)\n", reason)
		} else {
			if plan.Cloudflare.TunnelRef != "" {
				fmt.Fprintf(out, "    • Tunnel %s\n", plan.Cloudflare.Label())
			}
			for _, record := range plan.Cloudflare.Records {
				fmt.Fprintf(out, "    • DNS %s\n", record.Hostname)
			}
			if plan.Cloudflare.Creds.Present {
				fmt.Fprintf(out, "    • %s\n", plan.Cloudflare.Creds.Label)
			}
			if plan.Cloudflare.Reason != "" {
				fmt.Fprintf(out, "    note: %s\n", plan.Cloudflare.Reason)
			}
		}
	} else {
		fmt.Fprintln(out, "  Cloudflare account resources: kept (pass --cloudflare to also delete the tunnel and DNS)")
	}
}

func printUninstallReport(out io.Writer, report uninstall.Report) {
	fmt.Fprintln(out)
	if len(report.Steps) == 0 {
		fmt.Fprintln(out, "Nothing to remove.")
		return
	}
	for _, step := range report.Steps {
		switch step.Outcome {
		case uninstall.Removed:
			fmt.Fprintf(out, "  ✓ %s\n", step.Label)
		case uninstall.Failed:
			fmt.Fprintf(out, "  ✗ %s: %v\n", step.Label, step.Err)
		}
	}
}

func confirmPrompt(reader *bufio.Reader, out io.Writer, question string, defaultYes bool) bool {
	suffix := " [y/N] "
	if defaultYes {
		suffix = " [Y/n] "
	}
	fmt.Fprint(out, question+suffix)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return defaultYes
	}
	return answer == "y" || answer == "yes"
}

func serviceInstallContext(configPath string, vtunnelBin string, cloudflaredBin string) (launchd.Manager, []launchd.Spec, error) {
	if runtime.GOOS != "darwin" {
		return launchd.Manager{}, nil, errors.New("vtunnel service install is currently supported on macOS only")
	}
	if err := config.EnsureDirs(); err != nil {
		return launchd.Manager{}, nil, err
	}
	path, cfg, err := loadServiceConfig(configPath)
	if err != nil {
		return launchd.Manager{}, nil, err
	}
	if vtunnelBin == "" {
		vtunnelBin, err = resolveServiceBinary("vtunnel")
		if err != nil {
			return launchd.Manager{}, nil, err
		}
	}
	if cloudflaredBin == "" {
		cloudflaredBin, err = resolveServiceBinary("cloudflared")
		if err != nil {
			return launchd.Manager{}, nil, err
		}
	}
	manager, err := launchd.New(launchdRunner)
	if err != nil {
		return launchd.Manager{}, nil, err
	}
	specs, err := serviceSpecs(manager.Home, cfg, path, vtunnelBin, cloudflaredBin)
	if err != nil {
		return launchd.Manager{}, nil, err
	}
	return manager, specs, nil
}

func serviceRuntimeContext(configPath string) (launchd.Manager, []launchd.Spec, error) {
	if runtime.GOOS != "darwin" {
		return launchd.Manager{}, nil, errors.New("vtunnel services are currently supported on macOS only")
	}
	path, cfg, err := loadServiceConfig(configPath)
	if err != nil {
		return launchd.Manager{}, nil, err
	}
	manager, err := launchd.New(launchdRunner)
	if err != nil {
		return launchd.Manager{}, nil, err
	}
	specs, err := serviceSpecs(manager.Home, cfg, path, "vtunnel", "cloudflared")
	if err != nil {
		return launchd.Manager{}, nil, err
	}
	return manager, specs, nil
}

func loadServiceConfig(configPath string) (string, config.Config, error) {
	path := strings.TrimSpace(config.ExpandPath(configPath))
	if path == "" {
		var err error
		path, err = config.ConfigPath()
		if err != nil {
			return "", config.Config{}, err
		}
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", config.Config{}, err
		}
		path = abs
	}
	cfg, err := config.Load(path)
	if err != nil {
		return "", config.Config{}, err
	}
	return path, cfg, nil
}

func serviceSpecs(home string, cfg config.Config, configPath string, vtunnelBin string, cloudflaredBin string) ([]launchd.Spec, error) {
	logsDir, err := config.LogsDir()
	if err != nil {
		return nil, err
	}
	cloudflaredConfigPath := config.ExpandPath(cfg.Cloudflared.ConfigPath)
	if !filepath.IsAbs(cloudflaredConfigPath) {
		abs, err := filepath.Abs(cloudflaredConfigPath)
		if err != nil {
			return nil, err
		}
		cloudflaredConfigPath = abs
	}
	return []launchd.Spec{
		launchd.VtunnelDaemonSpec(vtunnelBin, configPath, logsDir, home),
		launchd.CloudflaredSpec(cloudflaredBin, cloudflaredConfigPath, logsDir, home),
	}, nil
}

func resolveServiceBinary(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err == nil && !strings.Contains(path, "/go-build") {
		return path, nil
	}
	if name != "vtunnel" {
		return "", fmt.Errorf("%s not found; install it first", name)
	}
	exe, exeErr := os.Executable()
	if exeErr != nil {
		return "", fmt.Errorf("find current vtunnel executable: %w", exeErr)
	}
	if strings.Contains(exe, "/go-build") {
		return "", errors.New("vtunnel service install needs an installed vtunnel binary; run it from Homebrew or pass --vtunnel-bin")
	}
	return exe, nil
}

func printServicePaths(out io.Writer, manager launchd.Manager, specs []launchd.Spec) {
	for _, spec := range specs {
		fmt.Fprintf(out, "  - %s: %s\n", spec.Name, manager.PlistPath(spec))
	}
}

func newCLIOnboardingEngine(path string, cfg config.Config) *onboarding.Engine {
	return onboarding.New(onboarding.Options{
		ConfigPath: path,
		Config:     cfg,
		Hooks: onboarding.Hooks{
			ReadToken:        readCloudflareTokenFromKeychain,
			WriteToken:       writeCloudflareTokenToKeychain,
			OpenURL:          openURL,
			CloudflareClient: newCloudflareClientWithEnvOptions,
			InspectCloudflared: func(ctx context.Context) onboarding.Cloudflared {
				_ = ctx
				return onboardingCloudflared(inspectCloudflaredForCLI())
			},
			InspectProcess: func(ctx context.Context, path string, configPath string, tunnelRef string) onboarding.ProcessInspection {
				return onboardingProcessInspection(inspectCloudflaredProcessForCLI(ctx, path, configPath, tunnelRef))
			},
			StartCloudflared: func(ctx context.Context, path string, configPath string) (onboarding.StartResult, error) {
				result, err := startCloudflaredForCLI(ctx, path, configPath)
				return onboarding.StartResult{PID: result.PID, LogPath: result.LogPath, Skipped: result.Skipped}, err
			},
			StartDaemon: func(ctx context.Context, cfg config.Config, configPath string) error {
				return ensureDaemon(ctx, cfg, configPath)
			},
			CreateCloudflaredTunnel: createCloudflaredTunnel,
			WriteTunnelConfig:       writeCloudflaredTunnelConfig,
			WriteCloudflaredPlan: func(plan cf.Plan) (cf.WriteResult, error) {
				return cf.WritePlan(plan, time.Now())
			},
		},
	})
}

func onboardingCloudflared(inspection cloudflaredInspection) onboarding.Cloudflared {
	return onboarding.Cloudflared{
		Path:          inspection.Path,
		VersionOutput: inspection.VersionOutput,
		LocalVersion:  inspection.LocalVersion,
		LatestVersion: inspection.LatestVersion,
		CertPath:      inspection.CertPath,
		CertExists:    inspection.CertExists,
		Tunnels:       inspection.Tunnels,
		TunnelListErr: inspection.TunnelListErr,
		Err:           inspection.Err,
		LatestErr:     inspection.LatestErr,
	}
}

func onboardingProcessInspection(inspection cloudflaredProcessInspection) onboarding.ProcessInspection {
	processes := make([]onboarding.Process, 0, len(inspection.Processes))
	for _, process := range inspection.Processes {
		processes = append(processes, onboarding.Process{PID: process.PID, Command: process.Command})
	}
	matches := make([]onboarding.Process, 0, len(inspection.Matches))
	for _, process := range inspection.Matches {
		matches = append(matches, onboarding.Process{PID: process.PID, Command: process.Command})
	}
	return onboarding.ProcessInspection{
		Processes: processes,
		Matches:   matches,
		Err:       inspection.Err,
	}
}

func newDaemonCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the local vtunnel daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if err := config.EnsureDirs(); err != nil {
				return err
			}
			store, err := savedRouteStore()
			if err != nil {
				return err
			}
			logStore, err := savedRequestLogStore()
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			return daemon.New(cfg, store, logStore, logger).Run(cmd.Context())
		},
	}
	cmd.AddCommand(
		newDaemonStopCommand(configPath),
		newDaemonRestartCommand(configPath),
	)
	return cmd
}

func newDaemonStopCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the local vtunnel daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			client := api.New(cfg)
			if err := client.Shutdown(cmd.Context()); err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Daemon: already stopped")
				return nil
			}
			if err := waitDaemonStopped(cmd.Context(), client); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Daemon: stopped")
			return nil
		},
	}
}

func newDaemonRestartCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the local vtunnel daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			client := api.New(cfg)
			if err := client.Shutdown(cmd.Context()); err == nil {
				if err := waitDaemonStopped(cmd.Context(), client); err != nil {
					return err
				}
			}
			if err := ensureDaemon(cmd.Context(), cfg, *configPath); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Daemon: restarted")
			return nil
		},
	}
}

func ensureDaemon(ctx context.Context, cfg config.Config, configPath string) error {
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

func waitDaemonStopped(ctx context.Context, client *api.Client) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Health(ctx); err != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("daemon did not stop within 3 seconds")
}

func configForRouteDomain(cfg config.Config, domainFlag string) config.Config {
	domain := routes.NormalizeHostname(domainFlag)
	if domain == "" {
		return cfg
	}
	for _, existing := range cfg.Domains {
		if routes.NormalizeHostname(existing) == domain {
			return cfg
		}
	}
	cfg.Domains = append(append([]string{}, cfg.Domains...), domain)
	return cfg
}

func normalizePort(value string) (string, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port %q", value)
	}
	return strconv.Itoa(port), nil
}

func localHTTPPortTarget(port string) string {
	return "http://127.0.0.1:" + port
}

func hostnameForRoute(subdomain string, domainFlag string, cfg config.Config) (string, error) {
	subdomain = routes.NormalizeHostname(subdomain)
	domain := routes.NormalizeHostname(domainFlag)
	if domain == "" {
		domain = routes.NormalizeHostname(cfg.DefaultDomain)
	}

	if subdomain == "" {
		var err error
		subdomain, err = randomSubdomain(6)
		if err != nil {
			return "", err
		}
	}
	if strings.Contains(subdomain, ".") && domainFlag == "" {
		return subdomain, nil
	}
	if domain == "" {
		return "", errors.New("no domain configured; pass --domain or set default_domain in ~/.config/vtunnel/config.yml")
	}
	return subdomain + "." + domain, nil
}

func hostnameForStop(value string, cfg config.Config) (string, error) {
	hostname := routes.NormalizeHostname(value)
	if hostname == "" {
		return "", errors.New("hostname is required")
	}
	if strings.Contains(hostname, ".") {
		return hostname, nil
	}
	domain := routes.NormalizeHostname(cfg.DefaultDomain)
	if domain == "" {
		return "", errors.New("subdomain used but no default_domain is configured")
	}
	return hostname + "." + domain, nil
}

func randomSubdomain(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b strings.Builder
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generate subdomain: %w", err)
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}

func readSavedRoutes() ([]routes.Route, error) {
	store, err := savedRouteStore()
	if err != nil {
		return nil, err
	}
	return store.List(), nil
}

func savedRouteStore() (*routes.Store, error) {
	path, err := config.RoutesPath()
	if err != nil {
		return nil, err
	}
	return routes.NewStore(path)
}

func savedRequestLogStore() (*requestlog.Store, error) {
	path, err := config.RequestLogsPath()
	if err != nil {
		return nil, err
	}
	return requestlog.NewStore(path, requestlog.DefaultMaxEntries)
}

func printSavedCounts(out io.Writer) {
	routeCount := 0
	if store, err := savedRouteStore(); err == nil {
		routeCount = len(store.List())
	}
	logCount := 0
	if store, err := savedRequestLogStore(); err == nil {
		logCount = store.Count()
	}
	fmt.Fprintf(out, "Saved routes: %d\n", routeCount)
	fmt.Fprintf(out, "Saved requests: %d\n", logCount)
}

func printRoutes(out io.Writer, list []routes.Route) {
	if len(list) == 0 {
		fmt.Fprintln(out, "No routes.")
		return
	}
	for _, route := range list {
		fmt.Fprintf(out, "%-32s -> %s\n", route.Hostname, route.Target)
	}
}

func printCloudflaredSummary(out io.Writer, cfg config.Config) {
	if path, err := exec.LookPath("cloudflared"); err == nil {
		fmt.Fprintf(out, "cloudflared: installed at %s\n", path)
	} else {
		fmt.Fprintln(out, "cloudflared: not found")
	}

	cloudflaredConfig := config.ExpandPath(cfg.Cloudflared.ConfigPath)
	if _, err := os.Stat(cloudflaredConfig); err == nil {
		fmt.Fprintf(out, "cloudflared config: %s\n", cloudflaredConfig)
	} else {
		fmt.Fprintf(out, "cloudflared config: not found at %s\n", cloudflaredConfig)
	}
}

func printCloudflareDiscovery(out io.Writer, discovery cfapi.Discovery, cloudflared cloudflaredInspection) {
	if discovery.Token.Status == "" {
		fmt.Fprintln(out, "Cloudflare API token: not verified")
	} else {
		fmt.Fprintf(out, "Cloudflare API token: %s\n", discovery.Token.Status)
		if discovery.Token.ID != "" {
			fmt.Fprintf(out, "Token ID: %s\n", discovery.Token.ID)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Accounts: %d\n", len(discovery.Accounts))
	for _, account := range discovery.Accounts {
		label := account.Name
		if label == "" {
			label = account.ID
		}
		fmt.Fprintf(out, "  - %s (%s)\n", label, account.ID)
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "Zones: %d\n", len(discovery.Zones))
	for _, zone := range discovery.Zones {
		accountName := zone.Account.Name
		if accountName == "" {
			accountName = zone.Account.ID
		}
		fmt.Fprintf(out, "  - %s [%s] account=%s\n", zone.Name, zone.Status, accountName)
	}

	printCloudflaredTunnels(out, cloudflared)

	fmt.Fprintln(out)
	totalWildcardRecords := 0
	for _, records := range discovery.WildcardDNS {
		totalWildcardRecords += len(records)
	}
	fmt.Fprintf(out, "Wildcard DNS records: %d\n", totalWildcardRecords)
	for _, zone := range discovery.Zones {
		records := discovery.WildcardDNS[zone.ID]
		if len(records) == 0 {
			continue
		}
		fmt.Fprintf(out, "  %s:\n", zone.Name)
		for _, record := range records {
			proxied := "dns-only"
			if record.Proxied {
				proxied = "proxied"
			}
			fmt.Fprintf(out, "    - %s %s -> %s [%s]\n", record.Type, record.Name, record.Content, proxied)
		}
	}

	if len(discovery.Errors) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Warnings:")
		for _, warning := range discovery.Errors {
			fmt.Fprintf(out, "  - %s\n", warning)
		}
	}
}

func printCloudflaredTunnels(out io.Writer, cloudflared cloudflaredInspection) {
	fmt.Fprintln(out)
	if cloudflared.Err != nil {
		fmt.Fprintln(out, "Local cloudflared tunnels: unavailable (cloudflared not found)")
		return
	}
	if cloudflared.TunnelListErr != nil {
		fmt.Fprintf(out, "Local cloudflared tunnels: unavailable (%v)\n", cloudflared.TunnelListErr)
		return
	}
	fmt.Fprintf(out, "Local cloudflared tunnels: %d\n", len(cloudflared.Tunnels))
	for _, tunnel := range cloudflared.Tunnels {
		connectionCount := len(tunnel.Connections)
		status := "stopped"
		if connectionCount > 0 {
			status = fmt.Sprintf("%d connections", connectionCount)
		}
		fmt.Fprintf(out, "  - %s [%s] %s\n", tunnel.Name, status, tunnel.ID)
	}
}

func followLogs(ctx context.Context, out io.Writer, client *api.Client, hostname string) error {
	lastID, err := latestLogID(ctx, client, hostname)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			entries, err := client.ListLogs(ctx, requestlog.Filter{
				Hostname: hostname,
				AfterID:  lastID,
				Limit:    100,
			})
			if err != nil {
				return err
			}
			printLogEntries(out, entries)
			for _, entry := range entries {
				if entry.ID > lastID {
					lastID = entry.ID
				}
			}
		}
	}
}

func latestLogID(ctx context.Context, client *api.Client, hostname string) (uint64, error) {
	entries, err := client.ListLogs(ctx, requestlog.Filter{
		Hostname: hostname,
		Limit:    1,
	})
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	return entries[len(entries)-1].ID, nil
}

func printLogEntries(out io.Writer, entries []requestlog.Entry) {
	for _, entry := range entries {
		fmt.Fprintf(
			out,
			"%s %-6s %-4d %-28s %s %s\n",
			entry.Time.Format("15:04:05"),
			entry.Method,
			entry.Status,
			entry.Hostname,
			formatDuration(entry.Duration),
			entry.Path,
		)
	}
}

func formatDuration(duration time.Duration) string {
	if duration < time.Millisecond {
		return fmt.Sprintf("%dus", duration.Microseconds())
	}
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	return duration.Truncate(100 * time.Millisecond).String()
}

func applySetupDomains(cfg config.Config, domains []string) config.Config {
	seen := map[string]bool{}
	next := make([]string, 0, len(cfg.Domains)+len(domains))

	for _, domain := range cfg.Domains {
		domain = routes.NormalizeHostname(domain)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		next = append(next, domain)
	}
	for _, domain := range domains {
		domain = routes.NormalizeHostname(domain)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		next = append(next, domain)
	}
	cfg.Domains = next
	if cfg.DefaultDomain == "" && len(next) > 0 {
		cfg.DefaultDomain = next[0]
	}
	return cfg
}

var createCloudflaredTunnel = func(ctx context.Context, path string, name string) (cf.CreateTunnelResult, error) {
	return cf.NewTunnelRunner(path).Create(ctx, name, "")
}

var writeCloudflaredTunnelConfig = func(update cf.TunnelConfigUpdate) (cf.WriteResult, error) {
	return cf.WriteTunnelConfigUpdate(update, time.Now())
}

type cloudflaredInspection struct {
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

type cloudflaredProcess struct {
	PID     int
	Command string
}

type cloudflaredProcessInspection struct {
	Processes []cloudflaredProcess
	Matches   []cloudflaredProcess
	Err       error
}

func inspectCloudflared() cloudflaredInspection {
	path, err := exec.LookPath("cloudflared")
	if err != nil {
		return cloudflaredInspection{CertPath: cloudflaredOriginCertPath(), Err: err}
	}

	inspection := cloudflaredInspection{Path: path, CertPath: cloudflaredOriginCertPath()}
	if _, err := os.Stat(inspection.CertPath); err == nil {
		inspection.CertExists = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		inspection.VersionOutput = fmt.Sprintf("version unavailable: %v", err)
	} else {
		inspection.VersionOutput = strings.TrimSpace(string(output))
		inspection.LocalVersion = extractCloudflaredVersion(inspection.VersionOutput)
	}

	inspection.LatestVersion, inspection.LatestErr = inspectHomebrewCloudflaredLatest()
	if inspection.CertExists {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		inspection.Tunnels, inspection.TunnelListErr = cf.NewTunnelRunner(path).List(ctx)
	}
	return inspection
}

func cloudflaredOriginCertPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloudflared", "cert.pem")
}

func inspectHomebrewCloudflaredLatest() (string, error) {
	brewPath, err := exec.LookPath("brew")
	if err != nil {
		return "", errors.New("brew not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

func inspectCloudflaredProcess(ctx context.Context, cloudflaredPath string, cloudflaredConfigPath string, tunnelRef string) cloudflaredProcessInspection {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return cloudflaredProcessInspection{Err: err}
	}

	inspection := cloudflaredProcessInspection{}
	for _, line := range strings.Split(string(output), "\n") {
		process, ok := parseCloudflaredProcessLine(line)
		if !ok {
			continue
		}
		inspection.Processes = append(inspection.Processes, process)
		if cloudflaredProcessMatches(process.Command, cloudflaredPath, cloudflaredConfigPath, tunnelRef) {
			inspection.Matches = append(inspection.Matches, process)
		}
	}
	return inspection
}

func parseCloudflaredProcessLine(line string) (cloudflaredProcess, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return cloudflaredProcess{}, false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return cloudflaredProcess{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return cloudflaredProcess{}, false
	}
	command := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	executable := filepath.Base(fields[1])
	if executable != "cloudflared" && !strings.HasSuffix(fields[1], "/cloudflared") {
		return cloudflaredProcess{}, false
	}
	return cloudflaredProcess{PID: pid, Command: command}, true
}

func cloudflaredProcessMatches(command string, cloudflaredPath string, cloudflaredConfigPath string, tunnelRef string) bool {
	args := strings.Fields(command)
	if len(args) == 0 {
		return false
	}
	executable := filepath.Base(args[0])
	if executable != "cloudflared" && !strings.HasSuffix(args[0], "/cloudflared") && !strings.EqualFold(args[0], cloudflaredPath) {
		return false
	}
	if !commandContainsArg(args, "tunnel") || !commandContainsArg(args, "run") {
		return false
	}
	configPath := strings.TrimSpace(config.ExpandPath(cloudflaredConfigPath))
	if configPath != "" && commandUsesConfig(args, configPath) {
		return true
	}
	tunnelRef = strings.TrimSpace(tunnelRef)
	return tunnelRef != "" && commandContainsArg(args, tunnelRef)
}

func commandContainsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func commandUsesConfig(args []string, configPath string) bool {
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

type cloudflaredStartResult struct {
	PID     int
	LogPath string
	Skipped bool
}

func startCloudflared(ctx context.Context, cloudflaredPath string, cloudflaredConfigPath string) (cloudflaredStartResult, error) {
	_ = ctx
	logPath, err := cloudflaredLogPath()
	if err != nil {
		return cloudflaredStartResult{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return cloudflaredStartResult{}, fmt.Errorf("open cloudflared log: %w", err)
	}

	command := exec.CommandContext(context.Background(), cloudflaredPath, "--config", cloudflaredConfigPath, "tunnel", "run")
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return cloudflaredStartResult{}, fmt.Errorf("start cloudflared: %w", err)
	}
	_ = logFile.Close()

	return cloudflaredStartResult{
		PID:     command.Process.Pid,
		LogPath: logPath,
	}, nil
}

func cloudflaredLogPath() (string, error) {
	return config.CloudflaredLogPath()
}
