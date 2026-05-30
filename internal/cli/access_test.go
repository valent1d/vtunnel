package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cfapi "vtunnel/internal/cloudflare"
	"vtunnel/internal/routes"
)

func TestCloudflareTokenTemplateAccessScope(t *testing.T) {
	scopeOf := func(includeWrite bool) map[string]string {
		raw := cloudflareTokenTemplateURL(includeWrite)
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		var perms []map[string]string
		if err := json.Unmarshal([]byte(u.Query().Get("permissionGroupKeys")), &perms); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, p := range perms {
			out[p["key"]] = p["type"]
		}
		return out
	}

	if got := scopeOf(false); got["access"] != "read" || got["access_acct"] != "read" {
		t.Fatalf("default scope should be read: %v", got)
	}
	if got := scopeOf(true); got["access"] != "edit" || got["access_acct"] != "edit" {
		t.Fatalf("--access scope should be edit: %v", got)
	}
}

// accessTestServer stubs the Cloudflare API for access-status tests.
func accessTestServer(t *testing.T, orgHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/accounts":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "acct-1", "name": "Acme"}})
		case r.URL.Path == "/accounts/acct-1/access/organizations":
			orgHandler(w, r)
		case r.URL.Path == "/accounts/acct-1/access/identity_providers":
			writeCloudflareEnvelope(t, w, []map[string]any{
				{"id": "otp", "type": "onetimepin", "name": "One-time PIN"},
				{"id": "oidc", "type": "oidc", "name": "Authentik"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
}

func TestAccessStatusReady(t *testing.T) {
	server := accessTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCloudflareEnvelope(t, w, map[string]any{"auth_domain": "acme.cloudflareaccess.com"})
	})
	defer server.Close()
	t.Setenv(cfapi.BaseURLEnv, server.URL)
	stubCloudflareKeychainToken(t, "tok", nil)

	out, err := executeCommand(context.Background(), "access", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ENABLED", "acme.cloudflareaccess.com", "onetimepin", "Authentik"} {
		if !strings.Contains(out, want) {
			t.Fatalf("access status = %q, want %q", out, want)
		}
	}
}

func TestAccessStatusNotSetUp(t *testing.T) {
	server := accessTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeCloudflareEnvelope(t, w, map[string]any{"auth_domain": ""})
	})
	defer server.Close()
	t.Setenv(cfapi.BaseURLEnv, server.URL)
	stubCloudflareKeychainToken(t, "tok", nil)

	out, err := executeCommand(context.Background(), "access", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "NOT ENABLED") || !strings.Contains(out, "one.dash.cloudflare.com") {
		t.Fatalf("access status = %q", out)
	}
}

func TestBuildAllowRules(t *testing.T) {
	// otp/sso: emails and @domains
	rules, err := buildAllowRules("otp", []string{"a@b.com", "@progiseize.com"}, false)
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %v err = %v", rules, err)
	}
	if _, ok := rules[0]["email"]; !ok {
		t.Fatalf("first rule not email: %v", rules[0])
	}
	if _, ok := rules[1]["email_domain"]; !ok {
		t.Fatalf("second rule not email_domain: %v", rules[1])
	}

	// otp requires an allow
	if _, err := buildAllowRules("otp", nil, false); err == nil {
		t.Fatal("otp with no allow should error")
	}
	// sso allow is optional → empty means everyone via the IdP
	ssoRules, err := buildAllowRules("sso", nil, false)
	if err != nil {
		t.Fatalf("sso with no allow should be allowed: %v", err)
	}
	if len(ssoRules) != 1 {
		t.Fatalf("sso empty allow should produce one rule, got %v", ssoRules)
	}
	if _, ok := ssoRules[0]["everyone"]; !ok {
		t.Fatalf("sso empty allow rule should be everyone: %v", ssoRules[0])
	}
	// everyone gated behind --force
	if _, err := buildAllowRules("otp", []string{"everyone"}, false); err == nil {
		t.Fatal("everyone without --force should error")
	}
	if _, err := buildAllowRules("otp", []string{"everyone"}, true); err != nil {
		t.Fatalf("everyone with --force should pass: %v", err)
	}
}

// protectTestServer stubs the Access API surface needed by protectHostname.
func protectTestServer(t *testing.T, createdApps, createdPolicies, deletedApps *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/accounts/acct-1/access/identity_providers" && r.Method == http.MethodGet:
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "otp-id", "type": "onetimepin", "name": ""}})
		case r.URL.Path == "/accounts/acct-1/access/apps" && r.Method == http.MethodGet:
			writeCloudflareEnvelope(t, w, []map[string]any{})
		case r.URL.Path == "/accounts/acct-1/access/apps" && r.Method == http.MethodPost:
			var app cfapi.AccessApp
			_ = json.NewDecoder(r.Body).Decode(&app)
			*createdApps = append(*createdApps, app.Name)
			writeCloudflareEnvelope(t, w, map[string]any{"id": "app-1", "name": app.Name, "type": app.Type})
		case r.URL.Path == "/accounts/acct-1/access/apps/app-1/policies" && r.Method == http.MethodPost:
			var pol cfapi.AccessPolicy
			_ = json.NewDecoder(r.Body).Decode(&pol)
			*createdPolicies = append(*createdPolicies, pol.Decision)
			writeCloudflareEnvelope(t, w, map[string]any{"id": "pol-1", "decision": pol.Decision})
		case strings.HasPrefix(r.URL.Path, "/accounts/acct-1/access/apps/") && r.Method == http.MethodDelete:
			*deletedApps = append(*deletedApps, r.URL.Path)
			writeCloudflareEnvelope(t, w, map[string]any{"id": "app-1"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
}

func TestProtectHostnameCreatesAppAndPolicy(t *testing.T) {
	var apps, policies, deleted []string
	server := protectTestServer(t, &apps, &policies, &deleted)
	defer server.Close()
	client, _ := cfapi.New("tok", cfapi.WithBaseURL(server.URL))

	info, err := protectHostname(context.Background(), client, "acct-1", "doli23.example.com", protectOptions{
		Mode:  "otp",
		Allow: []string{"@progiseize.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.AppID != "app-1" || len(info.PolicyIDs) != 1 || info.Mode != "otp" {
		t.Fatalf("info = %+v", info)
	}
	if len(apps) != 1 || apps[0] != "vtunnel-doli23.example.com" {
		t.Fatalf("created apps = %v", apps)
	}
	if len(policies) != 1 || policies[0] != "allow" {
		t.Fatalf("created policies = %v", policies)
	}
}

func TestPauseAndResumeProtection(t *testing.T) {
	var lastDecision string
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct-1/access/apps/app-1/policies":
			var pol cfapi.AccessPolicy
			_ = json.NewDecoder(r.Body).Decode(&pol)
			lastDecision = pol.Decision
			writeCloudflareEnvelope(t, w, map[string]any{"id": "newpol", "decision": pol.Decision})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/policies/"):
			deleted = append(deleted, r.URL.Path)
			writeCloudflareEnvelope(t, w, map[string]any{"id": "x"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client, _ := cfapi.New("tok", cfapi.WithBaseURL(server.URL))

	info := &routes.AccessInfo{AppID: "app-1", PolicyIDs: []string{"old"}, Mode: "otp", Allow: []string{"@x.com"}}

	if err := pauseProtection(context.Background(), client, "acct-1", info); err != nil {
		t.Fatal(err)
	}
	if !info.Paused || lastDecision != "bypass" || info.PolicyIDs[0] != "newpol" {
		t.Fatalf("after pause: paused=%v decision=%q ids=%v", info.Paused, lastDecision, info.PolicyIDs)
	}

	if err := resumeProtection(context.Background(), client, "acct-1", info); err != nil {
		t.Fatal(err)
	}
	if info.Paused || lastDecision != "allow" {
		t.Fatalf("after resume: paused=%v decision=%q", info.Paused, lastDecision)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected 2 policy deletes (one per swap), got %d", len(deleted))
	}
}

func TestProtectHostnameSSORequiresKnownIdP(t *testing.T) {
	var apps, policies, deleted []string
	server := protectTestServer(t, &apps, &policies, &deleted)
	defer server.Close()
	client, _ := cfapi.New("tok", cfapi.WithBaseURL(server.URL))

	_, err := protectHostname(context.Background(), client, "acct-1", "x.example.com", protectOptions{
		Mode:  "sso",
		IdP:   "Authentik",
		Allow: []string{"@progiseize.com"},
	})
	if err == nil {
		t.Fatal("expected error: SSO idp not present in stub")
	}
	if len(apps) != 0 {
		t.Fatalf("no app should be created when idp is missing: %v", apps)
	}
}

func TestFetchOIDCEndpoints(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/application/o/cf/.well-known/openid-configuration" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": "https://auth/authorize",
			"token_endpoint":         "https://auth/token",
			"jwks_uri":               "https://auth/jwks",
		})
	}))
	defer issuer.Close()

	auth, token, certs, err := fetchOIDCEndpoints(context.Background(), issuer.URL+"/application/o/cf/")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "https://auth/authorize" || token != "https://auth/token" || certs != "https://auth/jwks" {
		t.Fatalf("endpoints = %q %q %q", auth, token, certs)
	}
}

func TestAccessIdpAddAuthentik(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": "https://auth/authorize",
			"token_endpoint":         "https://auth/token",
			"jwks_uri":               "https://auth/jwks",
		})
	}))
	defer issuer.Close()

	var created cfapi.IdentityProvider
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/accounts":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "acct-1", "name": "Acme"}})
		case r.URL.Path == "/accounts/acct-1/access/identity_providers" && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&created)
			writeCloudflareEnvelope(t, w, map[string]any{"id": "idp-1", "type": created.Type, "name": created.Name})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer cf.Close()

	t.Setenv(cfapi.BaseURLEnv, cf.URL)
	stubCloudflareKeychainToken(t, "tok", nil)

	out, err := executeCommand(context.Background(), "access", "idp", "add", "authentik",
		"--issuer", issuer.URL, "--client-id", "cid", "--client-secret", "sec", "--name", "VLTN Connect")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Added identity provider") {
		t.Fatalf("output = %q", out)
	}
	if created.Type != "oidc" || created.Config.ClientID != "cid" || created.Config.AuthURL != "https://auth/authorize" || created.Config.CertsURL != "https://auth/jwks" {
		t.Fatalf("created idp = %+v", created)
	}
}

func TestAccessIdpAddAuthentikRequiresFlags(t *testing.T) {
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "acct-1", "name": "Acme"}})
		case "/accounts/acct-1/access/organizations":
			writeCloudflareEnvelope(t, w, map[string]any{"auth_domain": "acme.cloudflareaccess.com"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer cf.Close()
	t.Setenv(cfapi.BaseURLEnv, cf.URL)
	stubCloudflareKeychainToken(t, "tok", nil)

	out, err := executeCommand(context.Background(), "access", "idp", "add", "authentik")
	if err == nil {
		t.Fatalf("expected error for missing flags, out=%q", out)
	}
	// Should print the redirect URI to guide the user.
	if !strings.Contains(out, "cdn-cgi/access/callback") {
		t.Fatalf("expected redirect URI guidance, got %q", out)
	}
}

func TestAccessSetupBecomesReadyByPolling(t *testing.T) {
	var orgCalls int32
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "acct-1", "name": "Acme"}})
		case "/accounts/acct-1/access/organizations":
			// First check (initial detection) reports not-set-up; later polls report ready.
			if atomic.AddInt32(&orgCalls, 1) <= 1 {
				writeCloudflareEnvelope(t, w, map[string]any{"auth_domain": ""})
			} else {
				writeCloudflareEnvelope(t, w, map[string]any{"auth_domain": "acme.cloudflareaccess.com"})
			}
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer cf.Close()
	t.Setenv(cfapi.BaseURLEnv, cf.URL)
	stubCloudflareKeychainToken(t, "tok", nil)

	prev := accessSetupPoll
	accessSetupPoll = 5 * time.Millisecond
	t.Cleanup(func() { accessSetupPoll = prev })

	out, err := executeCommand(context.Background(), "access", "setup", "--no-open")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "now enabled") {
		t.Fatalf("expected setup to detect activation, got %q", out)
	}
}

func TestAccessSetupAlreadyReady(t *testing.T) {
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts":
			writeCloudflareEnvelope(t, w, []map[string]any{{"id": "acct-1", "name": "Acme"}})
		case "/accounts/acct-1/access/organizations":
			writeCloudflareEnvelope(t, w, map[string]any{"auth_domain": "acme.cloudflareaccess.com"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer cf.Close()
	t.Setenv(cfapi.BaseURLEnv, cf.URL)
	stubCloudflareKeychainToken(t, "tok", nil)

	out, err := executeCommand(context.Background(), "access", "setup", "--no-open")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Zero Trust is enabled") {
		t.Fatalf("out = %q", out)
	}
}

func TestAccessStatusNoToken(t *testing.T) {
	stubCloudflareKeychainToken(t, "", cfapi.ErrMissingToken)
	out, err := executeCommand(context.Background(), "access", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No Cloudflare API token") {
		t.Fatalf("access status = %q", out)
	}
}
