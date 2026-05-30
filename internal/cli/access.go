package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	cfapi "vtunnel/internal/cloudflare"
)

func newAccessCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Protect routes with Cloudflare Access (Zero Trust login)",
		Long: "Put a Cloudflare Access login page in front of exposed routes. Access is\n" +
			"free for up to 50 users (counted across your whole Cloudflare account).",
	}
	cmd.AddCommand(newAccessStatusCommand(configPath))
	return cmd
}

func newAccessStatusCommand(_ *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Cloudflare Access / Zero Trust readiness",
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
				fmt.Fprintln(out, "Token: Access read OK")
				fmt.Fprintln(out, "To let vtunnel create protections, run: vtunnel cloudflare auth --access")
			case cfapi.AccessNotSetUp:
				fmt.Fprintln(out, "Zero Trust: NOT ENABLED")
				fmt.Fprintln(out, "Enable it once in the dashboard (free up to 50 users):")
				fmt.Fprintln(out, "  https://one.dash.cloudflare.com  → pick a team name → Free plan")
				fmt.Fprintln(out, "  (Cloudflare asks for a card even on Free; you are not charged.)")
				if status.Detail != "" {
					fmt.Fprintf(out, "  detail: %s\n", status.Detail)
				}
			case cfapi.AccessTokenUnscoped:
				fmt.Fprintln(out, "Zero Trust: your API token cannot read Access.")
				fmt.Fprintln(out, "Re-create the token with Access scope: vtunnel cloudflare auth --access")
			default:
				fmt.Fprintf(out, "Access state unknown: %s\n", status.Detail)
			}
			return nil
		},
	}
}

// resolveAccountID returns the Cloudflare account to operate on. With a single
// account it is unambiguous; multiple accounts are noted (confirm-with-default
// is a later refinement).
func resolveAccountID(ctx context.Context, client *cfapi.Client) (string, error) {
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "", errors.New("no Cloudflare account is accessible with this token")
	}
	return accounts[0].ID, nil
}
