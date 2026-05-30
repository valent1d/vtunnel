package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	cfapi "vtunnel/internal/cloudflare"
	cf "vtunnel/internal/cloudflared"
	"vtunnel/internal/config"
	"vtunnel/internal/sshui"
)

func newSSHCommand(configPath *string) *cobra.Command {
	var domain, target, idp string
	var allow []string
	var force bool

	cmd := &cobra.Command{
		Use:   "ssh [subdomain]",
		Short: "Expose SSH in the browser (zero-install) via Cloudflare Access",
		Long: "Create a browser-rendered SSH terminal at <subdomain>.<domain>. Visitors\n" +
			"open the URL and get an SSH terminal in their browser — no client and no\n" +
			"cloudflared needed (Cloudflare renders it at the edge after Access login).\n" +
			"Requires Zero Trust. Each user's email prefix must match their SSH username.\n\n" +
			"Run with no arguments to open the browser-SSH dashboard.",
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return sshui.Run(cmd.Context(), cliSSHManager{cfg: cfg, configPath: *configPath, domain: domain})
			}
			hostname, err := hostnameForRoute(args[0], domain, cfg)
			if err != nil {
				return err
			}
			if len(allow) == 0 {
				return errors.New("--allow is required for ssh (the email prefix must match the SSH username on the server)")
			}
			return createBrowserSSH(cmd, cfg, *configPath, hostname, sshTarget(target), protectOptions{Mode: sshMode(idp), Allow: allow, IdP: idp, Force: force})
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain to use for this SSH endpoint")
	cmd.Flags().StringVar(&target, "target", "localhost:22", "SSH server to expose (host:port)")
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "who may sign in: an email or @domain (repeatable, required)")
	cmd.Flags().StringVar(&idp, "idp", "", "identity provider for login (defaults to email one-time PIN)")
	cmd.Flags().BoolVar(&force, "force", false, "allow risky choices such as --allow everyone")
	cmd.AddCommand(newSSHListCommand(configPath), newSSHRemoveCommand(configPath))
	return cmd
}

func sshTarget(target string) string {
	target = strings.TrimSpace(strings.TrimPrefix(target, "ssh://"))
	if target == "" {
		target = "localhost:22"
	}
	return target
}

func sshMode(idp string) string {
	if strings.TrimSpace(idp) != "" {
		return "sso"
	}
	return "otp"
}

// provisionBrowserSSH creates a browser-rendered SSH endpoint: an Access app of
// type "ssh" + an Allow policy (gating the login), then an ssh:// ingress rule.
// The Access app is rolled back if the policy fails. It does NOT restart
// cloudflared — callers reload so each can choose how to report a reload error.
func provisionBrowserSSH(ctx context.Context, cfg config.Config, configPath, hostname, target string, opts protectOptions) error {
	client, accountID, err := accessClient(ctx)
	if err != nil {
		return err
	}
	if status := cfapi.DetectAccess(ctx, client, accountID); status.State != cfapi.AccessReady {
		if status.State == cfapi.AccessTokenUnscoped {
			return errAccessWriteScope
		}
		return errors.New("Zero Trust is not enabled — run `vtunnel access setup` first")
	}

	rules, err := buildAllowRules("otp", opts.Allow, opts.Force)
	if err != nil {
		return err
	}
	idpID, err := resolveProtectIdP(ctx, client, accountID, opts.Mode, opts.IdP)
	if err != nil {
		if cfapi.IsAuthorizationError(err) {
			return errAccessWriteScope
		}
		return err
	}

	name := accessAppName(hostname)
	if apps, listErr := client.ListAccessApps(ctx, accountID); listErr == nil {
		for _, app := range apps {
			if app.Name == name {
				_ = client.DeleteAccessApp(ctx, accountID, app.ID)
			}
		}
	}
	app, err := client.CreateAccessApp(ctx, accountID, cfapi.AccessApp{
		Name:                   name,
		Type:                   "ssh",
		Destinations:           []cfapi.AccessDestination{{Type: "public", URI: hostname}},
		AllowedIDPs:            []string{idpID},
		AutoRedirectToIdentity: true,
		SessionDuration:        "24h",
		SkipInterstitial:       true,
	})
	if err != nil {
		if cfapi.IsAuthorizationError(err) {
			return errAccessWriteScope
		}
		return err
	}
	if _, err := client.CreateAccessPolicy(ctx, accountID, app.ID, cfapi.AccessPolicy{Name: name, Decision: "allow", Include: rules}); err != nil {
		_ = client.DeleteAccessApp(ctx, accountID, app.ID)
		return err
	}

	plan, err := cf.PlanAddIngress(cloudflaredConfigPathFor(cfg), hostname, "ssh://"+target)
	if err != nil {
		return err
	}
	_, err = cf.WritePlan(plan, time.Now())
	return err
}

// createBrowserSSH is the CLI front-end: provision, then restart cloudflared
// (warning rather than failing on a reload error), then print the URL.
func createBrowserSSH(cmd *cobra.Command, cfg config.Config, configPath, hostname, target string, opts protectOptions) error {
	out := cmd.OutOrStdout()
	if err := provisionBrowserSSH(cmd.Context(), cfg, configPath, hostname, target, opts); err != nil {
		return err
	}
	fmt.Fprintf(out, "+ ingress %s → ssh://%s\n", hostname, target)
	fmt.Fprintln(out, "↻ restarting cloudflared (all tunnels reconnect briefly)…")
	if err := reloadCloudflared(cmd.Context(), cfg, configPath); err != nil {
		fmt.Fprintf(out, "⚠ could not restart cloudflared automatically (%v); restart it to apply.\n", err)
	}
	fmt.Fprintf(out, "🔒 Browser SSH ready: https://%s\n", hostname)
	fmt.Fprintln(out, "  Open it in any browser — no client needed. The email prefix must match the SSH username.")
	return nil
}

// removeBrowserSSH deletes the Access app (best effort) and the ssh:// ingress
// for hostname. It does NOT reload cloudflared. Returns false when there was no
// SSH ingress to remove.
func removeBrowserSSH(ctx context.Context, cfg config.Config, hostname string) (bool, error) {
	if client, accountID, accErr := accessClient(ctx); accErr == nil {
		name := accessAppName(hostname)
		if apps, listErr := client.ListAccessApps(ctx, accountID); listErr == nil {
			for _, app := range apps {
				if app.Name == name {
					_ = client.DeleteAccessApp(ctx, accountID, app.ID)
				}
			}
		}
	}
	plan, err := cf.PlanRemoveIngress(cloudflaredConfigPathFor(cfg), hostname)
	if err != nil {
		return false, err
	}
	if len(plan.Changes) == 0 {
		return false, nil
	}
	if _, err := cf.WritePlan(plan, time.Now()); err != nil {
		return false, err
	}
	return true, nil
}

// maybeSuggestSSH offers browser SSH when the user exposes port 22 over plain
// TCP. It returns handled=true when it has dealt with the request (created the
// browser-SSH endpoint, or the user cancelled), so the caller skips the TCP
// path. A non-interactive shell, a suppressed preference, or a non-SSH port all
// fall through silently. The remember choice is persisted either way.
func maybeSuggestSSH(cmd *cobra.Command, cfg *config.Config, configPath, hostname, target, subdomain string) (bool, error) {
	if cfg.Prefs.SuppressSSHSuggestion || portFromService(target) != 22 || !interactiveTerminal() {
		return false, nil
	}
	res, err := runSSHSuggestion(cmd.Context(), subdomain)
	if err != nil {
		// Could not run the prompt — fall back to the plain TCP path.
		return false, nil
	}
	if res.remember {
		cfg.Prefs.SuppressSSHSuggestion = true
		if saveErr := config.Save(configPath, *cfg); saveErr != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "⚠ could not save preference (%v)\n", saveErr)
		}
	}
	if res.cancelled {
		return true, nil
	}
	if res.useSSH {
		return true, createBrowserSSH(cmd, *cfg, configPath, hostname, sshTarget(target), protectOptions{Mode: "otp", Allow: []string{res.allow}})
	}
	return false, nil
}

func newSSHListCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List browser SSH endpoints",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			loaded, err := cf.Load(cloudflaredConfigPathFor(cfg))
			if err != nil {
				return err
			}
			found := false
			for _, rule := range loaded.Ingress {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rule.Service)), "ssh://") {
					fmt.Fprintf(cmd.OutOrStdout(), "  https://%-30s %s\n", rule.Hostname, rule.Service)
					found = true
				}
			}
			if !found {
				fmt.Fprintln(cmd.OutOrStdout(), "No browser SSH endpoints.")
			}
			return nil
		},
	}
}

func newSSHRemoveCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <subdomain>",
		Aliases: []string{"remove"},
		Short:   "Remove a browser SSH endpoint",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			hostname, err := hostnameForStop(args[0], cfg)
			if err != nil {
				return err
			}
			removed, err := removeBrowserSSH(cmd.Context(), cfg, hostname)
			if err != nil {
				return err
			}
			if !removed {
				fmt.Fprintf(cmd.OutOrStdout(), "No SSH endpoint for %s.\n", hostname)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "↻ restarting cloudflared…")
			if err := reloadCloudflared(cmd.Context(), cfg, *configPath); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "⚠ could not restart cloudflared automatically (%v).\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ removed browser SSH %s\n", hostname)
			return nil
		},
	}
}

// cliSSHManager implements sshui.Manager so the browser-SSH dashboard can
// list/add/remove endpoints without importing the Cloudflare client plumbing.
type cliSSHManager struct {
	cfg        config.Config
	configPath string
	domain     string
}

func (m cliSSHManager) List(ctx context.Context) ([]sshui.Endpoint, error) {
	loaded, err := cf.Load(cloudflaredConfigPathFor(m.cfg))
	if err != nil {
		return nil, err
	}
	var out []sshui.Endpoint
	for _, rule := range loaded.Ingress {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(rule.Service)), "ssh://") {
			out = append(out, sshui.Endpoint{
				Hostname: rule.Hostname,
				Target:   strings.TrimPrefix(strings.TrimSpace(rule.Service), "ssh://"),
				URL:      "https://" + rule.Hostname,
			})
		}
	}
	return out, nil
}

func (m cliSSHManager) Add(ctx context.Context, subdomain, target, allow, idp string) error {
	hostname, err := hostnameForRoute(subdomain, m.domain, m.cfg)
	if err != nil {
		return err
	}
	var allowList []string
	if a := strings.TrimSpace(allow); a != "" {
		allowList = []string{a}
	}
	if err := provisionBrowserSSH(ctx, m.cfg, m.configPath, hostname, sshTarget(target), protectOptions{Mode: sshMode(idp), Allow: allowList, IdP: strings.TrimSpace(idp)}); err != nil {
		return err
	}
	return reloadCloudflared(ctx, m.cfg, m.configPath)
}

func (m cliSSHManager) Remove(ctx context.Context, hostname string) error {
	removed, err := removeBrowserSSH(ctx, m.cfg, hostname)
	if err != nil || !removed {
		return err
	}
	return reloadCloudflared(ctx, m.cfg, m.configPath)
}
