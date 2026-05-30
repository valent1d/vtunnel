package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// This file covers the Cloudflare Access (Zero Trust) surface vtunnel needs:
// reading the Zero Trust organization, managing identity providers, and
// managing self-hosted Access applications + their policies. Shapes are modeled
// on a live account (see the GET responses verified during design).

// AccessOrg is the account's Zero Trust organization. A non-empty AuthDomain is
// vtunnel's readiness signal: Access is usable only once an org exists.
type AccessOrg struct {
	Name         string `json:"name"`
	AuthDomain   string `json:"auth_domain"`
	IsUIReadOnly bool   `json:"is_ui_read_only"`
}

// IdentityProvider is a Cloudflare Access login method (onetimepin, oidc,
// google-apps, …).
type IdentityProvider struct {
	ID     string    `json:"id,omitempty"`
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Config IDPConfig `json:"config,omitempty"`
}

// IDPConfig is the union of identity-provider config fields vtunnel sets or
// reads. Cloudflare redacts client_secret on read.
type IDPConfig struct {
	ClientID       string   `json:"client_id,omitempty"`
	ClientSecret   string   `json:"client_secret,omitempty"`
	AuthURL        string   `json:"auth_url,omitempty"`
	TokenURL       string   `json:"token_url,omitempty"`
	CertsURL       string   `json:"certs_url,omitempty"`
	Scopes         []string `json:"scopes,omitempty"`
	AppsDomain     string   `json:"apps_domain,omitempty"` // google-apps
	EmailClaimName string   `json:"email_claim_name,omitempty"`
}

// AccessDestination binds an app to a public hostname. It replaces the
// deprecated self_hosted_domains field (sunset 2025-11-21).
type AccessDestination struct {
	Type string `json:"type"` // "public"
	URI  string `json:"uri"`  // e.g. "doli23.example.com"
}

// AccessApp is a self-hosted Access application protecting one hostname.
type AccessApp struct {
	ID                     string              `json:"id,omitempty"`
	Name                   string              `json:"name"`
	Type                   string              `json:"type"` // "self_hosted"
	Domain                 string              `json:"domain,omitempty"`
	Destinations           []AccessDestination `json:"destinations,omitempty"`
	AllowedIDPs            []string            `json:"allowed_idps,omitempty"`
	AutoRedirectToIdentity bool                `json:"auto_redirect_to_identity"`
	SessionDuration        string              `json:"session_duration,omitempty"`
	AppLauncherVisible     bool                `json:"app_launcher_visible"`
	// SkipInterstitial is set for browser-rendered SSH/VNC apps (type "ssh").
	SkipInterstitial bool `json:"skip_interstitial,omitempty"`
}

// AccessPolicy is an allow/deny rule attached to an app. Include rules are
// modeled as raw objects (one rule per entry) built via the *Rule helpers.
type AccessPolicy struct {
	ID         string           `json:"id,omitempty"`
	Name       string           `json:"name"`
	Decision   string           `json:"decision"` // "allow", "deny", "bypass"
	Include    []map[string]any `json:"include,omitempty"`
	Precedence int              `json:"precedence,omitempty"`
}

// EmailRule allows a single email address.
func EmailRule(email string) map[string]any {
	return map[string]any{"email": map[string]string{"email": email}}
}

// EmailDomainRule allows any address at a domain (e.g. "progiseize.com").
func EmailDomainRule(domain string) map[string]any {
	return map[string]any{"email_domain": map[string]string{"domain": domain}}
}

// EveryoneRule allows anyone who authenticates. Combined with OTP this gates
// nobody meaningfully — callers must guard against it.
func EveryoneRule() map[string]any {
	return map[string]any{"everyone": map[string]any{}}
}

// AccessState is the readiness of Cloudflare Access for an account.
type AccessState int

const (
	// AccessUnknown means detection could not run (e.g. no account id).
	AccessUnknown AccessState = iota
	// AccessNotSetUp means no Zero Trust organization exists yet — the user
	// must bootstrap it in the dashboard (cannot be done over the API).
	AccessNotSetUp
	// AccessTokenUnscoped means the token cannot even read Access (401/403).
	AccessTokenUnscoped
	// AccessReady means a Zero Trust org exists and is readable.
	AccessReady
)

// AccessStatus is the detected Access readiness plus context for the UX.
type AccessStatus struct {
	State      AccessState
	AuthDomain string
	Detail     string
}

// DetectAccess maps an account to one of the readiness states. It fails open:
// any non-authorization error (a never-enabled account returns billing/other
// errors) is treated as "not set up" rather than a hard failure, so the CLI can
// guide the user to bootstrap Zero Trust.
func DetectAccess(ctx context.Context, client *Client, accountID string) AccessStatus {
	if strings.TrimSpace(accountID) == "" {
		return AccessStatus{State: AccessUnknown, Detail: "no account id"}
	}
	org, err := client.ZeroTrustOrg(ctx, accountID)
	switch {
	case err == nil && strings.TrimSpace(org.AuthDomain) != "":
		return AccessStatus{State: AccessReady, AuthDomain: org.AuthDomain}
	case err == nil:
		return AccessStatus{State: AccessNotSetUp, Detail: "Zero Trust organization is not configured"}
	case IsAuthorizationError(err):
		return AccessStatus{State: AccessTokenUnscoped, Detail: err.Error()}
	default:
		return AccessStatus{State: AccessNotSetUp, Detail: err.Error()}
	}
}

func accessPath(accountID string, parts ...string) string {
	path := "/accounts/" + url.PathEscape(strings.TrimSpace(accountID)) + "/access"
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

// ZeroTrustOrg reads the account's Zero Trust organization. The result is a
// single object (not a list). A nil error with empty AuthDomain means the org
// is not set up; an authorization error means the token lacks Access scope.
func (c *Client) ZeroTrustOrg(ctx context.Context, accountID string) (AccessOrg, error) {
	if strings.TrimSpace(accountID) == "" {
		return AccessOrg{}, errors.New("account id is required")
	}
	var org AccessOrg
	err := c.do(ctx, http.MethodGet, accessPath(accountID, "organizations"), nil, &org)
	return org, err
}

func (c *Client) ListIdentityProviders(ctx context.Context, accountID string) ([]IdentityProvider, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("account id is required")
	}
	var providers []IdentityProvider
	err := c.listAll(ctx, accessPath(accountID, "identity_providers"), func(data json.RawMessage) error {
		var page []IdentityProvider
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		providers = append(providers, page...)
		return nil
	})
	return providers, err
}

func (c *Client) CreateIdentityProvider(ctx context.Context, accountID string, input IdentityProvider) (IdentityProvider, error) {
	if strings.TrimSpace(accountID) == "" {
		return IdentityProvider{}, errors.New("account id is required")
	}
	var provider IdentityProvider
	err := c.do(ctx, http.MethodPost, accessPath(accountID, "identity_providers"), input, &provider)
	return provider, err
}

func (c *Client) DeleteIdentityProvider(ctx context.Context, accountID, id string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(id) == "" {
		return errors.New("account id and provider id are required")
	}
	return c.do(ctx, http.MethodDelete, accessPath(accountID, "identity_providers", id), nil, nil)
}

func (c *Client) ListAccessApps(ctx context.Context, accountID string) ([]AccessApp, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("account id is required")
	}
	var apps []AccessApp
	err := c.listAll(ctx, accessPath(accountID, "apps"), func(data json.RawMessage) error {
		var page []AccessApp
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		apps = append(apps, page...)
		return nil
	})
	return apps, err
}

func (c *Client) CreateAccessApp(ctx context.Context, accountID string, input AccessApp) (AccessApp, error) {
	if strings.TrimSpace(accountID) == "" {
		return AccessApp{}, errors.New("account id is required")
	}
	var app AccessApp
	err := c.do(ctx, http.MethodPost, accessPath(accountID, "apps"), input, &app)
	return app, err
}

func (c *Client) UpdateAccessApp(ctx context.Context, accountID, appID string, input AccessApp) (AccessApp, error) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(appID) == "" {
		return AccessApp{}, errors.New("account id and app id are required")
	}
	var app AccessApp
	err := c.do(ctx, http.MethodPut, accessPath(accountID, "apps", appID), input, &app)
	return app, err
}

func (c *Client) DeleteAccessApp(ctx context.Context, accountID, appID string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(appID) == "" {
		return errors.New("account id and app id are required")
	}
	return c.do(ctx, http.MethodDelete, accessPath(accountID, "apps", appID), nil, nil)
}

// CreateAccessPolicy attaches an app-scoped policy. Deleting the app cascades
// its app-scoped policies, which keeps teardown simple.
func (c *Client) CreateAccessPolicy(ctx context.Context, accountID, appID string, input AccessPolicy) (AccessPolicy, error) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(appID) == "" {
		return AccessPolicy{}, errors.New("account id and app id are required")
	}
	var policy AccessPolicy
	err := c.do(ctx, http.MethodPost, accessPath(accountID, "apps", appID, "policies"), input, &policy)
	return policy, err
}

func (c *Client) DeleteAccessPolicy(ctx context.Context, accountID, appID, policyID string) error {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(appID) == "" || strings.TrimSpace(policyID) == "" {
		return errors.New("account id, app id and policy id are required")
	}
	return c.do(ctx, http.MethodDelete, accessPath(accountID, "apps", appID, "policies", policyID), nil, nil)
}
