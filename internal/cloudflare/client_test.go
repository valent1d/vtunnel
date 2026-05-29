package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientDiscoversCloudflareResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
			t.Fatalf("authorization = %q, want %q", got, want)
		}
		switch r.URL.Path {
		case "/user/tokens/verify":
			writeEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case "/accounts":
			writeEnvelope(t, w, []map[string]any{{"id": "account-id", "name": "Example", "type": "standard"}})
		case "/zones":
			writeEnvelope(t, w, []map[string]any{{
				"id": "zone-id", "name": "example.test", "status": "active", "type": "full",
				"account": map[string]any{"id": "account-id", "name": "Example"},
			}})
		case "/zones/zone-id/dns_records":
			if got, want := r.URL.Query().Get("name"), "*.example.test"; got != want {
				t.Fatalf("name = %q, want %q", got, want)
			}
			writeEnvelope(t, w, []map[string]any{{
				"id": "record-id", "type": "CNAME", "name": "*.example.test", "content": "tunnel-id.cfargotunnel.com", "proxied": true,
			}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New("test-token", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	discovery := Discover(context.Background(), client)
	if len(discovery.Errors) != 0 {
		t.Fatalf("errors = %#v", discovery.Errors)
	}
	if discovery.Token.Status != "active" {
		t.Fatalf("token status = %q", discovery.Token.Status)
	}
	if len(discovery.Accounts) != 1 {
		t.Fatalf("accounts = %d", len(discovery.Accounts))
	}
	if len(discovery.Zones) != 1 {
		t.Fatalf("zones = %d", len(discovery.Zones))
	}
	if len(discovery.WildcardDNS["zone-id"]) != 1 {
		t.Fatalf("wildcard dns = %#v", discovery.WildcardDNS)
	}
}

func TestClientDiscoversWithoutAccountHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			writeEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case "/accounts":
			writeEnvelope(t, w, []map[string]any{{"id": "account-id", "name": "Example", "type": "standard"}})
		case "/zones":
			writeEnvelope(t, w, []map[string]any{{"id": "zone-id", "name": "example.test", "status": "active"}})
		case "/zones/zone-id/dns_records":
			writeEnvelope(t, w, []map[string]any{})
		default:
			t.Fatalf("unexpected path %s", r.URL.String())
		}
	}))
	defer server.Close()

	client, err := New("test-token", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	discovery := Discover(context.Background(), client)
	if len(discovery.Errors) != 0 {
		t.Fatalf("errors = %#v", discovery.Errors)
	}
	if discovery.Token.ID != "token-id" {
		t.Fatalf("token id = %q", discovery.Token.ID)
	}
	if len(discovery.Accounts) != 1 || discovery.Accounts[0].ID != "account-id" {
		t.Fatalf("accounts = %#v", discovery.Accounts)
	}
}

func TestDiscoveryUsesZoneAccountsWhenAccountListingIsForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			writeEnvelope(t, w, map[string]any{"id": "token-id", "status": "active"})
		case "/accounts":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": false,
				"errors":  []map[string]any{{"code": 9109, "message": "Unauthorized to access requested resource"}},
			})
		case "/zones":
			writeEnvelope(t, w, []map[string]any{{
				"id": "zone-id", "name": "example.test", "status": "active",
				"account": map[string]any{"id": "account-id", "name": "Example"},
			}})
		case "/zones/zone-id/dns_records":
			writeEnvelope(t, w, []map[string]any{})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New("test-token", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	discovery := Discover(context.Background(), client)
	if len(discovery.Accounts) != 1 || discovery.Accounts[0].ID != "account-id" {
		t.Fatalf("accounts = %#v", discovery.Accounts)
	}
}

func TestClientCreatesAndUpdatesDNSRecords(t *testing.T) {
	var created bool
	var updated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-id/dns_records":
			var input DNSRecordInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Name != "*.example.test" || input.Content != "tunnel.cfargotunnel.com" || !input.Proxied {
				t.Fatalf("create input = %#v", input)
			}
			created = true
			writeEnvelope(t, w, map[string]any{"id": "record-id", "type": input.Type, "name": input.Name, "content": input.Content, "proxied": input.Proxied})
		case r.Method == http.MethodPut && r.URL.Path == "/zones/zone-id/dns_records/record-id":
			var input DNSRecordInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Name != "*.example.test" || input.Content != "next.cfargotunnel.com" || !input.Proxied {
				t.Fatalf("update input = %#v", input)
			}
			updated = true
			writeEnvelope(t, w, map[string]any{"id": "record-id", "type": input.Type, "name": input.Name, "content": input.Content, "proxied": input.Proxied})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New("test-token", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.CreateDNSRecord(context.Background(), "zone-id", DNSRecordInput{
		Type: "CNAME", Name: "*.example.test", Content: "tunnel.cfargotunnel.com", Proxied: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateDNSRecord(context.Background(), "zone-id", "record-id", DNSRecordInput{
		Type: "CNAME", Name: "*.example.test", Content: "next.cfargotunnel.com", Proxied: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !created || !updated {
		t.Fatalf("created=%v updated=%v", created, updated)
	}
}

func TestNewRejectsMissingToken(t *testing.T) {
	_, err := New("")
	if err != ErrMissingToken {
		t.Fatalf("err = %v", err)
	}
}

func TestClientReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 10000, "message": "permission denied"}},
		})
	}))
	defer server.Close()

	client, err := New("test-token", WithBaseURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.VerifyToken(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"result":  result,
		"result_info": map[string]any{
			"page":        1,
			"per_page":    50,
			"count":       1,
			"total_count": 1,
			"total_pages": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}
