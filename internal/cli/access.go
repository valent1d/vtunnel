package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"vtunnel/internal/api"
	cfapi "vtunnel/internal/cloudflare"
	"vtunnel/internal/config"
	"vtunnel/internal/routes"
)

// accessSetupPoll is how often `access setup` re-checks Zero Trust readiness
// while the user completes the dashboard step. Overridable in tests.
var accessSetupPoll = 3 * time.Second

// errAccessWriteScope is returned when the token can read but not write Access.
var errAccessWriteScope = errors.New("your Cloudflare token cannot create Access apps — run: vtunnel cloudflare auth --access")

func newAccessCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Protect routes with Cloudflare Access (Zero Trust login)",
		Long: "Put a Cloudflare Access login page in front of exposed routes. Access is\n" +
			"free for up to 50 users (counted across your whole Cloudflare account).",
	}
	cmd.AddCommand(
		newAccessStatusCommand(configPath),
		newAccessSetupCommand(),
		newAccessProtectCommand(configPath),
		newAccessUnprotectCommand(configPath),
		newAccessPauseCommand(configPath),
		newAccessResumeCommand(configPath),
		newAccessIdpCommand(),
	)
	return cmd
}

func newAccessSetupCommand() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Guided Cloudflare Access (Zero Trust) setup",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			client, _, err := newCloudflareClientFromKeychain()
			if errors.Is(err, cfapi.ErrMissingToken) {
				fmt.Fprintln(out, "First, create a Cloudflare API token with Access scope:")
				fmt.Fprintln(out, "  vtunnel cloudflare auth --access")
				return nil
			}
			if err != nil {
				return err
			}
			accountID, err := resolveAccountID(cmd.Context(), client)
			if err != nil {
				return err
			}

			switch status := cfapi.DetectAccess(cmd.Context(), client, accountID); status.State {
			case cfapi.AccessReady:
				fmt.Fprintf(out, "✓ Zero Trust is enabled (team: %s).\n", status.AuthDomain)
				fmt.Fprintln(out, "Protect a route: vtunnel http <port> <sub> --protect --allow you@example.com")
				return nil
			case cfapi.AccessTokenUnscoped:
				fmt.Fprintln(out, "Your token can't read Access. Re-create it with: vtunnel cloudflare auth --access")
				return nil
			}

			fmt.Fprintln(out, "Zero Trust is not enabled yet. One-time setup in the dashboard:")
			fmt.Fprintln(out, "  1. Open https://one.dash.cloudflare.com")
			fmt.Fprintln(out, "  2. Pick a team name → your login domain becomes <team>.cloudflareaccess.com")
			fmt.Fprintln(out, "  3. Choose the Free plan (≤50 users; a card is required even on Free — you are not charged)")
			if !noOpen {
				_ = openURL("https://one.dash.cloudflare.com")
			}
			fmt.Fprintln(out, "\nWaiting for Zero Trust to become active… (Ctrl+C to stop)")

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ticker := time.NewTicker(accessSetupPoll)
			defer ticker.Stop()
			waited := 0
			for {
				select {
				case <-ctx.Done():
					fmt.Fprintln(out, "Stopped. Re-run `vtunnel access setup` when ready.")
					return nil
				case <-ticker.C:
					if status := cfapi.DetectAccess(ctx, client, accountID); status.State == cfapi.AccessReady {
						fmt.Fprintf(out, "✓ Zero Trust is now enabled (team: %s).\n", status.AuthDomain)
						fmt.Fprintln(out, "Next: run `vtunnel cloudflare auth --access` (if not done), then protect a route.")
						return nil
					}
					if waited++; waited%10 == 0 {
						fmt.Fprintln(out, "  still waiting…")
					}
				}
			}
		},
	}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open the dashboard in a browser")
	return cmd
}

func newAccessStatusCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Cloudflare Access / Zero Trust readiness and protected routes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			client, _, err := newCloudflareClientFromKeychain()
			if errors.Is(err, cfapi.ErrMissingToken) {
				fmt.Fprintln(out, "No Cloudflare API token stored.")
				fmt.Fprintln(out, "Run: vtunnel cloudflare auth --access")
				return nil
			}
			if err != nil {
				return err
			}
			accountID, err := resolveAccountID(cmd.Context(), client)
			if err != nil {
				return err
			}

			status := cfapi.DetectAccess(cmd.Context(), client, accountID)
			switch status.State {
			case cfapi.AccessReady:
				fmt.Fprintf(out, "Zero Trust: ENABLED   team: %s\n", status.AuthDomain)
				if idps, idpErr := client.ListIdentityProviders(cmd.Context(), accountID); idpErr == nil && len(idps) > 0 {
					fmt.Fprintln(out, "Identity providers:")
					for _, idp := range idps {
						name := idp.Name
						if name == "" && idp.Type == "onetimepin" {
							name = "One-time PIN (built-in)"
						}
						fmt.Fprintf(out, "  - %-12s %s\n", idp.Type, name)
					}
				}
			case cfapi.AccessNotSetUp:
				fmt.Fprintln(out, "Zero Trust: NOT ENABLED")
				fmt.Fprintln(out, "Enable it once in the dashboard (free up to 50 users):")
				fmt.Fprintln(out, "  https://one.dash.cloudflare.com  → pick a team name → Free plan")
				fmt.Fprintln(out, "  (Cloudflare asks for a card even on Free; you are not charged.)")
				return nil
			case cfapi.AccessTokenUnscoped:
				fmt.Fprintln(out, "Zero Trust: your API token cannot read Access.")
				fmt.Fprintln(out, "Re-create the token with Access scope: vtunnel cloudflare auth --access")
				return nil
			default:
				fmt.Fprintf(out, "Access state unknown: %s\n", status.Detail)
				return nil
			}

			printProtectedRoutes(cmd.Context(), out, *configPath)
			return nil
		},
	}
}

func newAccessProtectCommand(configPath *string) *cobra.Command {
	var mode, idp, session string
	var allow []string
	var force bool

	cmd := &cobra.Command{
		Use:   "protect <subdomain>",
		Short: "Protect an existing route with Cloudflare Access",
		Long: "Create an Access app + policy for an already-published route. The route\n" +
			"stays reachable until the policy propagates to the edge (a few seconds).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			hostname, err := hostnameForStop(args[0], cfg)
			if err != nil {
				return err
			}

			route, ok := findRoute(cmd.Context(), cfg, hostname)
			if !ok {
				return fmt.Errorf("no route %s; create it protected with: vtunnel http <port> %s --protect", hostname, args[0])
			}

			fmt.Fprintf(out, "⚠ %s is currently PUBLIC and stays reachable for a few seconds while the policy propagates.\n", hostname)

			info, err := runProtection(cmd, hostname, protectOptions{Mode: mode, Allow: allow, IdP: idp, Session: session, Force: force})
			if err != nil {
				return err
			}
			route.Access = info
			if err := api.New(cfg).AddRoute(cmd.Context(), route); err != nil {
				return err
			}
			fmt.Fprintf(out, "🔒 %s protected (%s)\n", hostname, info.Mode)
			return nil
		},
	}
	addProtectFlags(cmd, &mode, &allow, &idp, &session, &force)
	return cmd
}

func newAccessUnprotectCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "unprotect <subdomain>",
		Short: "Remove Cloudflare Access protection from a route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			hostname, err := hostnameForStop(args[0], cfg)
			if err != nil {
				return err
			}
			route, ok := findRoute(cmd.Context(), cfg, hostname)
			if !ok || route.Access == nil {
				fmt.Fprintf(out, "%s is not protected.\n", hostname)
				return nil
			}

			client, _, err := newCloudflareClientFromKeychain()
			if err != nil {
				return err
			}
			accountID, err := resolveAccountID(cmd.Context(), client)
			if err != nil {
				return err
			}
			if err := unprotectHostname(cmd.Context(), client, accountID, route.Access); err != nil {
				return err
			}
			route.Access = nil
			if err := api.New(cfg).AddRoute(cmd.Context(), route); err != nil {
				return err
			}
			fmt.Fprintf(out, "🔓 %s is now PUBLIC — anyone with the link can reach it.\n", hostname)
			return nil
		},
	}
}

func addProtectFlags(cmd *cobra.Command, mode *string, allow *[]string, idp, session *string, force *bool) {
	cmd.Flags().StringVar(mode, "mode", "otp", "auth method: otp | email | sso")
	cmd.Flags().StringArrayVar(allow, "allow", nil, "who may sign in: an email, @domain, or everyone (repeatable)")
	cmd.Flags().StringVar(idp, "idp", "", "identity provider name for --mode=sso")
	cmd.Flags().StringVar(session, "session", "24h", "Access session duration")
	cmd.Flags().BoolVar(force, "force", false, "allow risky choices such as --allow everyone")
}

// runProtection wires the shared protect flow: resolve+confirm account, ensure
// Access is ready, then create the app + policy.
func runProtection(cmd *cobra.Command, hostname string, opts protectOptions) (*routes.AccessInfo, error) {
	out := cmd.OutOrStdout()
	client, _, err := newCloudflareClientFromKeychain()
	if errors.Is(err, cfapi.ErrMissingToken) {
		return nil, errAccessWriteScope
	}
	if err != nil {
		return nil, err
	}
	accountID, accountName, count, err := resolveAccount(cmd.Context(), client)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "Using Cloudflare account: %s\n", accountName)
	if count > 1 {
		fmt.Fprintln(out, "  (multiple accounts visible; using the first — multi-account selection is not implemented yet)")
	}

	if status := cfapi.DetectAccess(cmd.Context(), client, accountID); status.State != cfapi.AccessReady {
		if status.State == cfapi.AccessTokenUnscoped {
			return nil, errAccessWriteScope
		}
		return nil, errors.New("Zero Trust is not enabled — run `vtunnel access status` for setup steps")
	}

	return protectHostname(cmd.Context(), client, accountID, hostname, opts)
}

type protectOptions struct {
	Mode    string
	Allow   []string
	IdP     string
	Session string
	Force   bool
}

func accessAppName(hostname string) string { return "vtunnel-" + hostname }

// protectHostname creates (idempotently replacing any prior vtunnel app) the
// Access app + allow policy for a hostname and returns the route metadata. On
// any failure after the app is created it rolls the app back, so a half-built
// protection never lingers.
func protectHostname(ctx context.Context, client *cfapi.Client, accountID, hostname string, opts protectOptions) (*routes.AccessInfo, error) {
	mode, err := normalizeProtectMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	rules, err := buildAllowRules(mode, opts.Allow, opts.Force)
	if err != nil {
		return nil, err
	}
	idpID, err := resolveProtectIdP(ctx, client, accountID, mode, opts.IdP)
	if err != nil {
		if cfapi.IsAuthorizationError(err) {
			return nil, errAccessWriteScope
		}
		return nil, err
	}

	name := accessAppName(hostname)
	// Idempotent replace: drop any prior app vtunnel created for this hostname.
	if apps, listErr := client.ListAccessApps(ctx, accountID); listErr == nil {
		for _, app := range apps {
			if app.Name == name {
				_ = client.DeleteAccessApp(ctx, accountID, app.ID)
			}
		}
	}

	session := strings.TrimSpace(opts.Session)
	if session == "" {
		session = "24h"
	}
	app, err := client.CreateAccessApp(ctx, accountID, cfapi.AccessApp{
		Name:                   name,
		Type:                   "self_hosted",
		Destinations:           []cfapi.AccessDestination{{Type: "public", URI: hostname}},
		AllowedIDPs:            []string{idpID},
		AutoRedirectToIdentity: true,
		SessionDuration:        session,
		AppLauncherVisible:     false,
	})
	if err != nil {
		if cfapi.IsAuthorizationError(err) {
			return nil, errAccessWriteScope
		}
		return nil, err
	}

	policy, err := client.CreateAccessPolicy(ctx, accountID, app.ID, cfapi.AccessPolicy{
		Name:     name,
		Decision: "allow",
		Include:  rules,
	})
	if err != nil {
		_ = client.DeleteAccessApp(ctx, accountID, app.ID) // rollback
		return nil, err
	}

	info := &routes.AccessInfo{
		AppID:     app.ID,
		PolicyIDs: []string{policy.ID},
		Mode:      mode,
		Allow:     opts.Allow,
	}
	if mode == "sso" {
		info.IdP = opts.IdP
	}
	return info, nil
}

// teardownAccessForStop removes the Access app behind a route being stopped,
// best-effort: if the token can't write Access, it warns instead of failing the
// stop, so the route still goes away and the user knows the app remains.
func teardownAccessForStop(cmd *cobra.Command, hostname, appID string) {
	out := cmd.OutOrStdout()
	client, _, err := newCloudflareClientFromKeychain()
	if err != nil {
		fmt.Fprintf(out, "⚠ %s was Access-protected; its Access app remains. Remove it with `vtunnel access unprotect` (after `vtunnel cloudflare auth --access`) or in the dashboard.\n", hostname)
		return
	}
	accountID, err := resolveAccountID(cmd.Context(), client)
	if err != nil {
		fmt.Fprintf(out, "⚠ %s Access app could not be removed (%v); remove it in the dashboard.\n", hostname, err)
		return
	}
	if err := client.DeleteAccessApp(cmd.Context(), accountID, appID); err != nil {
		fmt.Fprintf(out, "⚠ %s Access app could not be removed (%v); remove it in the dashboard.\n", hostname, err)
		return
	}
	fmt.Fprintf(out, "Removed Cloudflare Access protection for %s\n", hostname)
}

func unprotectHostname(ctx context.Context, client *cfapi.Client, accountID string, info *routes.AccessInfo) error {
	if info == nil || strings.TrimSpace(info.AppID) == "" {
		return nil
	}
	return client.DeleteAccessApp(ctx, accountID, info.AppID)
}

// pauseProtection swaps the app's policy for a bypass-everyone policy, so the
// route is public again while the app and allow-list are preserved. It mutates
// info (PolicyIDs + Paused). Fail-closed: old policies are dropped first.
func pauseProtection(ctx context.Context, client *cfapi.Client, accountID string, info *routes.AccessInfo) error {
	for _, id := range info.PolicyIDs {
		_ = client.DeleteAccessPolicy(ctx, accountID, info.AppID, id)
	}
	policy, err := client.CreateAccessPolicy(ctx, accountID, info.AppID, cfapi.AccessPolicy{
		Name:     "vtunnel-paused",
		Decision: "bypass",
		Include:  []map[string]any{cfapi.EveryoneRule()},
	})
	if err != nil {
		return err
	}
	info.PolicyIDs = []string{policy.ID}
	info.Paused = true
	return nil
}

// resumeProtection restores the allow policy from the stored allow-list.
func resumeProtection(ctx context.Context, client *cfapi.Client, accountID string, info *routes.AccessInfo) error {
	rules, err := buildAllowRules(info.Mode, info.Allow, true)
	if err != nil {
		return err
	}
	for _, id := range info.PolicyIDs {
		_ = client.DeleteAccessPolicy(ctx, accountID, info.AppID, id)
	}
	policy, err := client.CreateAccessPolicy(ctx, accountID, info.AppID, cfapi.AccessPolicy{
		Name:     "vtunnel-access",
		Decision: "allow",
		Include:  rules,
	})
	if err != nil {
		return err
	}
	info.PolicyIDs = []string{policy.ID}
	info.Paused = false
	return nil
}

func newAccessPauseCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "pause <subdomain>",
		Short: "Temporarily make a protected route public (keeps the config)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runPauseResume(cmd, configPath, args[0], true) },
	}
}

func newAccessResumeCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <subdomain>",
		Short: "Re-enable protection on a paused route",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return runPauseResume(cmd, configPath, args[0], false) },
	}
}

func runPauseResume(cmd *cobra.Command, configPath *string, arg string, pause bool) error {
	out := cmd.OutOrStdout()
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	hostname, err := hostnameForStop(arg, cfg)
	if err != nil {
		return err
	}
	route, ok := findRoute(cmd.Context(), cfg, hostname)
	if !ok || route.Access == nil {
		fmt.Fprintf(out, "%s is not protected.\n", hostname)
		return nil
	}
	client, _, err := newCloudflareClientFromKeychain()
	if err != nil {
		return errAccessWriteScope
	}
	accountID, err := resolveAccountID(cmd.Context(), client)
	if err != nil {
		return err
	}
	if pause {
		if err := pauseProtection(cmd.Context(), client, accountID, route.Access); err != nil {
			return err
		}
	} else {
		if err := resumeProtection(cmd.Context(), client, accountID, route.Access); err != nil {
			return err
		}
	}
	if err := api.New(cfg).AddRoute(cmd.Context(), route); err != nil {
		return err
	}
	if pause {
		fmt.Fprintf(out, "⏸ %s protection paused — PUBLIC until resumed.\n", hostname)
	} else {
		fmt.Fprintf(out, "🔒 %s protection resumed.\n", hostname)
	}
	return nil
}

func normalizeProtectMode(mode string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "", "otp":
		return "otp", nil
	case "email":
		return "email", nil
	case "sso":
		return "sso", nil
	default:
		return "", fmt.Errorf("invalid --mode %q: use otp, email or sso", mode)
	}
}

// buildAllowRules turns --allow values into Access policy include rules.
func buildAllowRules(mode string, allow []string, force bool) ([]map[string]any, error) {
	if mode == "email" {
		if len(allow) != 1 || strings.HasPrefix(allow[0], "@") || !strings.Contains(allow[0], "@") {
			return nil, errors.New("--mode=email needs exactly one --allow <email>")
		}
		return []map[string]any{cfapi.EmailRule(strings.TrimSpace(allow[0]))}, nil
	}
	if len(allow) == 0 {
		return nil, fmt.Errorf("--mode=%s needs at least one --allow <email|@domain>", mode)
	}
	rules := make([]map[string]any, 0, len(allow))
	for _, value := range allow {
		value = strings.TrimSpace(value)
		switch {
		case value == "everyone":
			if !force {
				return nil, errors.New("--allow everyone protects nobody meaningfully; pass --force to confirm")
			}
			rules = append(rules, cfapi.EveryoneRule())
		case strings.HasPrefix(value, "@"):
			rules = append(rules, cfapi.EmailDomainRule(strings.TrimPrefix(value, "@")))
		case strings.Contains(value, "@"):
			rules = append(rules, cfapi.EmailRule(value))
		default:
			return nil, fmt.Errorf("invalid --allow %q: use an email, @domain, or everyone", value)
		}
	}
	return rules, nil
}

// resolveProtectIdP returns the identity-provider ID for the mode. otp/email
// reuse (or create) the built-in One-time PIN; sso resolves a named provider.
func resolveProtectIdP(ctx context.Context, client *cfapi.Client, accountID, mode, idpName string) (string, error) {
	idps, err := client.ListIdentityProviders(ctx, accountID)
	if err != nil {
		return "", err
	}
	if mode == "sso" {
		if strings.TrimSpace(idpName) == "" {
			return "", errors.New("--mode=sso needs --idp <name> (see: vtunnel access status)")
		}
		for _, idp := range idps {
			if strings.EqualFold(idp.Name, idpName) || strings.EqualFold(idp.Type, idpName) {
				return idp.ID, nil
			}
		}
		return "", fmt.Errorf("identity provider %q not found (see: vtunnel access status)", idpName)
	}
	for _, idp := range idps {
		if idp.Type == "onetimepin" {
			return idp.ID, nil
		}
	}
	created, err := client.CreateIdentityProvider(ctx, accountID, cfapi.IdentityProvider{Name: "One-time PIN", Type: "onetimepin"})
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// resolveAccount returns the account to operate on plus how many were visible.
func resolveAccount(ctx context.Context, client *cfapi.Client) (id, name string, count int, err error) {
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		return "", "", 0, err
	}
	if len(accounts) == 0 {
		return "", "", 0, errors.New("no Cloudflare account is accessible with this token")
	}
	return accounts[0].ID, accounts[0].Name, len(accounts), nil
}

// resolveAccountID is the simple form used by read-only commands.
func resolveAccountID(ctx context.Context, client *cfapi.Client) (string, error) {
	id, _, _, err := resolveAccount(ctx, client)
	return id, err
}

// findRoute looks up a route by hostname via the daemon, falling back to the
// saved routes file when the daemon is not running.
func findRoute(ctx context.Context, cfg config.Config, hostname string) (routes.Route, bool) {
	list, err := api.New(cfg).ListRoutes(ctx)
	if err != nil {
		list, _ = readSavedRoutes()
	}
	for _, route := range list {
		if route.Hostname == hostname {
			return route, true
		}
	}
	return routes.Route{}, false
}

func printProtectedRoutes(ctx context.Context, out io.Writer, configPath string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}
	list, err := api.New(cfg).ListRoutes(ctx)
	if err != nil {
		list, _ = readSavedRoutes()
	}
	if len(list) == 0 {
		return
	}
	fmt.Fprintln(out, "Routes:")
	for _, route := range list {
		if route.Access == nil {
			fmt.Fprintf(out, "  🔓 %-28s (public)\n", route.Hostname)
			continue
		}
		detail := route.Access.Mode
		if len(route.Access.Allow) > 0 {
			detail += "  " + strings.Join(route.Access.Allow, ",")
		}
		if route.Access.IdP != "" {
			detail += "  via " + route.Access.IdP
		}
		badge := "🔒"
		if route.Access.Paused {
			badge = "⏸"
			detail += "  (paused — public)"
		}
		fmt.Fprintf(out, "  %s %-28s %s\n", badge, route.Hostname, detail)
	}
}
