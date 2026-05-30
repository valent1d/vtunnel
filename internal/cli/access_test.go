package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	cfapi "vtunnel/internal/cloudflare"
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
