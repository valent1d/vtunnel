package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestZeroTrustOrgReadsAuthDomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/accounts/acct/access/organizations" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		writeEnvelope(t, w, map[string]any{"name": "acme", "auth_domain": "acme.cloudflareaccess.com"})
	}))
	defer server.Close()

	client, err := New("test-token", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	org, err := client.ZeroTrustOrg(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	if org.AuthDomain != "acme.cloudflareaccess.com" {
		t.Fatalf("auth_domain = %q", org.AuthDomain)
	}
}

func TestListIdentityProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acct/access/identity_providers" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeEnvelope(t, w, []map[string]any{
			{"id": "otp1", "type": "onetimepin", "name": "One-time PIN"},
			{"id": "oidc1", "type": "oidc", "name": "Authentik", "config": map[string]any{"client_id": "abc", "auth_url": "https://auth/authorize"}},
		})
	}))
	defer server.Close()

	client, _ := New("test-token", WithBaseURL(server.URL))
	idps, err := client.ListIdentityProviders(context.Background(), "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(idps) != 2 || idps[0].Type != "onetimepin" || idps[1].Type != "oidc" {
		t.Fatalf("idps = %+v", idps)
	}
	if idps[1].Config.ClientID != "abc" || idps[1].Config.AuthURL != "https://auth/authorize" {
		t.Fatalf("oidc config = %+v", idps[1].Config)
	}
}

func TestCreateIdentityProviderSendsOIDCConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts/acct/access/identity_providers" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var input IdentityProvider
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Type != "oidc" || input.Config.ClientID != "cid" || input.Config.TokenURL != "https://auth/token" {
			t.Fatalf("create input = %+v", input)
		}
		writeEnvelope(t, w, map[string]any{"id": "new-idp", "type": input.Type, "name": input.Name})
	}))
	defer server.Close()

	client, _ := New("test-token", WithBaseURL(server.URL))
	idp, err := client.CreateIdentityProvider(context.Background(), "acct", IdentityProvider{
		Name: "Authentik",
		Type: "oidc",
		Config: IDPConfig{
			ClientID: "cid", ClientSecret: "secret",
			AuthURL: "https://auth/authorize", TokenURL: "https://auth/token", CertsURL: "https://auth/certs",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if idp.ID != "new-idp" {
		t.Fatalf("id = %q", idp.ID)
	}
}

func TestCreateAccessAppSendsDestinations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts/acct/access/apps" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var input AccessApp
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Type != "self_hosted" || len(input.Destinations) != 1 || input.Destinations[0].URI != "doli23.example.com" {
			t.Fatalf("app input = %+v", input)
		}
		if len(input.AllowedIDPs) != 1 || input.AllowedIDPs[0] != "otp1" || !input.AutoRedirectToIdentity {
			t.Fatalf("app idp config = %+v", input)
		}
		writeEnvelope(t, w, map[string]any{"id": "app1", "name": input.Name, "type": input.Type})
	}))
	defer server.Close()

	client, _ := New("test-token", WithBaseURL(server.URL))
	app, err := client.CreateAccessApp(context.Background(), "acct", AccessApp{
		Name:                   "vtunnel-doli23",
		Type:                   "self_hosted",
		Destinations:           []AccessDestination{{Type: "public", URI: "doli23.example.com"}},
		AllowedIDPs:            []string{"otp1"},
		AutoRedirectToIdentity: true,
		SessionDuration:        "24h",
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.ID != "app1" {
		t.Fatalf("id = %q", app.ID)
	}
}

func TestCreateAccessPolicyIncludesEmailDomain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts/acct/access/apps/app1/policies" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var input AccessPolicy
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Decision != "allow" || len(input.Include) != 1 {
			t.Fatalf("policy = %+v", input)
		}
		rule, ok := input.Include[0]["email_domain"].(map[string]any)
		if !ok || rule["domain"] != "progiseize.com" {
			t.Fatalf("include rule = %+v", input.Include[0])
		}
		writeEnvelope(t, w, map[string]any{"id": "pol1", "decision": "allow", "name": input.Name})
	}))
	defer server.Close()

	client, _ := New("test-token", WithBaseURL(server.URL))
	policy, err := client.CreateAccessPolicy(context.Background(), "acct", "app1", AccessPolicy{
		Name:     "vtunnel-doli23",
		Decision: "allow",
		Include:  []map[string]any{EmailDomainRule("progiseize.com")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.ID != "pol1" {
		t.Fatalf("id = %q", policy.ID)
	}
}

func TestDeleteAccessAppHitsDelete(t *testing.T) {
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/accounts/acct/access/apps/app1" {
			deleted = true
			writeEnvelope(t, w, map[string]any{"id": "app1"})
			return
		}
		t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client, _ := New("test-token", WithBaseURL(server.URL))
	if err := client.DeleteAccessApp(context.Background(), "acct", "app1"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("expected DELETE")
	}
}

func TestAccessMethodsValidateInput(t *testing.T) {
	client, _ := New("test-token")
	if _, err := client.ZeroTrustOrg(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty account id")
	}
	if err := client.DeleteAccessApp(context.Background(), "acct", ""); err == nil {
		t.Fatal("expected error for empty app id")
	}
	if err := client.DeleteAccessPolicy(context.Background(), "acct", "app1", ""); err == nil {
		t.Fatal("expected error for empty policy id")
	}
}
