package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	cfapi "vtunnel/internal/cloudflare"
)

func newAccessIdpCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "idp",
		Short: "Manage Cloudflare Access identity providers",
	}
	cmd.AddCommand(newAccessIdpListCommand(), newAccessIdpAddCommand(), newAccessIdpRemoveCommand())
	return cmd
}

func newAccessIdpListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List identity providers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, accountID, err := accessClient(cmd.Context())
			if err != nil {
				return err
			}
			idps, err := client.ListIdentityProviders(cmd.Context(), accountID)
			if err != nil {
				return err
			}
			if len(idps) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No identity providers. Add one with: vtunnel access idp add <otp|authentik|google>")
				return nil
			}
			for _, idp := range idps {
				name := idp.Name
				if name == "" && idp.Type == "onetimepin" {
					name = "One-time PIN (built-in)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s\n", idp.Type, name)
			}
			return nil
		},
	}
}

func newAccessIdpAddCommand() *cobra.Command {
	var issuer, clientID, clientSecret, appsDomain, name string

	cmd := &cobra.Command{
		Use:   "add <otp|authentik|google>",
		Short: "Add an identity provider",
		Long: "Add a login method to Cloudflare Access.\n\n" +
			"  otp        built-in email one-time PIN (no setup)\n" +
			"  authentik  OIDC — pass --issuer; endpoints are auto-discovered\n" +
			"  google     Google Workspace — pass --apps-domain\n\n" +
			"For authentik/google, first create an OAuth client on the provider side\n" +
			"with the redirect URI vtunnel prints, then pass --client-id/--client-secret.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			client, accountID, err := accessClient(cmd.Context())
			if err != nil {
				return err
			}

			provider := strings.ToLower(strings.TrimSpace(args[0]))
			switch provider {
			case "otp", "onetimepin":
				for _, idp := range mustListIdPs(cmd.Context(), client, accountID) {
					if idp.Type == "onetimepin" {
						fmt.Fprintln(out, "One-time PIN is already enabled.")
						return nil
					}
				}
				if _, err := client.CreateIdentityProvider(cmd.Context(), accountID, cfapi.IdentityProvider{Name: "One-time PIN", Type: "onetimepin"}); err != nil {
					return err
				}
				fmt.Fprintln(out, "Enabled One-time PIN (email).")
				return nil

			case "authentik", "oidc":
				if issuer == "" || clientID == "" || clientSecret == "" {
					printRedirectURI(cmd.Context(), out, client, accountID)
					return fmt.Errorf("authentik needs --issuer, --client-id and --client-secret")
				}
				auth, token, certs, err := fetchOIDCEndpoints(cmd.Context(), issuer)
				if err != nil {
					return err
				}
				if name == "" {
					name = "Authentik"
				}
				if _, err := client.CreateIdentityProvider(cmd.Context(), accountID, cfapi.IdentityProvider{
					Name: name,
					Type: "oidc",
					Config: cfapi.IDPConfig{
						ClientID:     clientID,
						ClientSecret: clientSecret,
						AuthURL:      auth,
						TokenURL:     token,
						CertsURL:     certs,
						Scopes:       []string{"openid", "email", "profile"},
					},
				}); err != nil {
					return err
				}
				fmt.Fprintf(out, "Added identity provider %q (oidc).\n", name)
				return nil

			case "google", "google-apps":
				if appsDomain == "" || clientID == "" || clientSecret == "" {
					printRedirectURI(cmd.Context(), out, client, accountID)
					return fmt.Errorf("google needs --apps-domain, --client-id and --client-secret")
				}
				if name == "" {
					name = "Google Workspace"
				}
				if _, err := client.CreateIdentityProvider(cmd.Context(), accountID, cfapi.IdentityProvider{
					Name: name,
					Type: "google-apps",
					Config: cfapi.IDPConfig{
						ClientID:     clientID,
						ClientSecret: clientSecret,
						AppsDomain:   appsDomain,
					},
				}); err != nil {
					return err
				}
				fmt.Fprintf(out, "Added identity provider %q (google-apps).\n", name)
				return nil

			default:
				return fmt.Errorf("unknown provider %q: use otp, authentik or google", provider)
			}
		},
	}
	cmd.Flags().StringVar(&issuer, "issuer", "", "OIDC issuer URL (authentik); endpoints are read from its .well-known")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth client id")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth client secret")
	cmd.Flags().StringVar(&appsDomain, "apps-domain", "", "Google Workspace domain (google)")
	cmd.Flags().StringVar(&name, "name", "", "display name for the identity provider")
	return cmd
}

func newAccessIdpRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an identity provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := accessClient(cmd.Context())
			if err != nil {
				return err
			}
			idps, err := client.ListIdentityProviders(cmd.Context(), accountID)
			if err != nil {
				return err
			}
			ref := strings.TrimSpace(args[0])
			for _, idp := range idps {
				if strings.EqualFold(idp.Name, ref) || strings.EqualFold(idp.Type, ref) {
					if err := client.DeleteIdentityProvider(cmd.Context(), accountID, idp.ID); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Removed identity provider %q.\n", ref)
					return nil
				}
			}
			return fmt.Errorf("identity provider %q not found", ref)
		},
	}
}

// accessClient builds a Cloudflare client from the stored token and resolves the
// account, with friendly errors when the token is missing or under-scoped.
func accessClient(ctx context.Context) (*cfapi.Client, string, error) {
	client, _, err := newCloudflareClientFromKeychain()
	if err != nil {
		return nil, "", errAccessWriteScope
	}
	accountID, err := resolveAccountID(ctx, client)
	if err != nil {
		return nil, "", err
	}
	return client, accountID, nil
}

func mustListIdPs(ctx context.Context, client *cfapi.Client, accountID string) []cfapi.IdentityProvider {
	idps, _ := client.ListIdentityProviders(ctx, accountID)
	return idps
}

// printRedirectURI shows the OAuth redirect URI the user must configure on the
// identity-provider side, derived from the Zero Trust team domain.
func printRedirectURI(ctx context.Context, out io.Writer, client *cfapi.Client, accountID string) {
	org, err := client.ZeroTrustOrg(ctx, accountID)
	if err != nil || org.AuthDomain == "" {
		return
	}
	fmt.Fprintf(out, "On the identity provider, set the redirect URI to:\n  https://%s/cdn-cgi/access/callback\n", org.AuthDomain)
}

// fetchOIDCEndpoints reads authorization/token/jwks endpoints from an OIDC
// issuer's discovery document, so the user only needs to paste the issuer URL.
func fetchOIDCEndpoints(ctx context.Context, issuer string) (authURL, tokenURL, certsURL string, err error) {
	wellKnown := strings.TrimRight(strings.TrimSpace(issuer), "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return "", "", "", err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("fetch OIDC discovery from %s: %w", wellKnown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("OIDC discovery %s returned HTTP %d", wellKnown, resp.StatusCode)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", "", "", fmt.Errorf("parse OIDC discovery: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		return "", "", "", fmt.Errorf("OIDC discovery %s is missing endpoints", wellKnown)
	}
	return doc.AuthorizationEndpoint, doc.TokenEndpoint, doc.JWKSURI, nil
}
