package cli

import (
	"context"
	"fmt"
	"strings"

	"vtunnel/internal/api"
	cfapi "vtunnel/internal/cloudflare"
	"vtunnel/internal/config"
	"vtunnel/internal/httpui"
)

// cliAccessController implements httpui.AccessController so the HTTP dashboard
// can manage Cloudflare Access without importing the Cloudflare client. Each
// call resolves the client/account fresh (cheap, and keeps tokens current).
type cliAccessController struct {
	cfg config.Config
}

func accessControllerFor(cfg config.Config) httpui.AccessController {
	return cliAccessController{cfg: cfg}
}

func (c cliAccessController) resolve(ctx context.Context) (*cloudflareWriteClient, error) {
	client, _, err := newCloudflareClientFromKeychain()
	if err != nil {
		return nil, errAccessWriteScope
	}
	accountID, err := resolveAccountID(ctx, client)
	if err != nil {
		return nil, err
	}
	return &cloudflareWriteClient{client: client, accountID: accountID}, nil
}

func (c cliAccessController) IdentityProviders(ctx context.Context) []string {
	w, err := c.resolve(ctx)
	if err != nil {
		return nil
	}
	idps, err := w.client.ListIdentityProviders(ctx, w.accountID)
	if err != nil {
		return nil
	}
	var names []string
	for _, idp := range idps {
		if idp.Type == "onetimepin" || idp.Name == "" {
			continue
		}
		names = append(names, idp.Name)
	}
	return names
}

func (c cliAccessController) Protect(ctx context.Context, hostname, mode, allow, idp string) error {
	w, err := c.resolve(ctx)
	if err != nil {
		return err
	}
	route, ok := findRoute(ctx, c.cfg, hostname)
	if !ok {
		return fmt.Errorf("no route %s", hostname)
	}
	info, err := protectHostname(ctx, w.client, w.accountID, hostname, protectOptions{
		Mode:  mode,
		Allow: splitAllow(allow),
		IdP:   idp,
	})
	if err != nil {
		return err
	}
	route.Access = info
	return api.New(c.cfg).AddRoute(ctx, route)
}

func (c cliAccessController) Unprotect(ctx context.Context, hostname string) error {
	w, err := c.resolve(ctx)
	if err != nil {
		return err
	}
	route, ok := findRoute(ctx, c.cfg, hostname)
	if !ok || route.Access == nil {
		return nil
	}
	if err := unprotectHostname(ctx, w.client, w.accountID, route.Access); err != nil {
		return err
	}
	route.Access = nil
	return api.New(c.cfg).AddRoute(ctx, route)
}

func (c cliAccessController) Pause(ctx context.Context, hostname string) error {
	return c.pauseResume(ctx, hostname, true)
}

func (c cliAccessController) Resume(ctx context.Context, hostname string) error {
	return c.pauseResume(ctx, hostname, false)
}

func (c cliAccessController) pauseResume(ctx context.Context, hostname string, pause bool) error {
	w, err := c.resolve(ctx)
	if err != nil {
		return err
	}
	route, ok := findRoute(ctx, c.cfg, hostname)
	if !ok || route.Access == nil {
		return fmt.Errorf("%s is not protected", hostname)
	}
	if pause {
		err = pauseProtection(ctx, w.client, w.accountID, route.Access)
	} else {
		err = resumeProtection(ctx, w.client, w.accountID, route.Access)
	}
	if err != nil {
		return err
	}
	return api.New(c.cfg).AddRoute(ctx, route)
}

type cloudflareWriteClient struct {
	client    *cfapi.Client
	accountID string
}

// splitAllow turns a free-form allow string ("a@b.com @c.com") into a list.
func splitAllow(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
