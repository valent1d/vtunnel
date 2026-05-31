package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := NewFileStore(path)

	if _, err := store.Get("cloudflare-api-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before Set = %v, want ErrNotFound", err)
	}
	if err := store.Set("cloudflare-api-token", "  secret-123  "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get("cloudflare-api-token")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "secret-123" {
		t.Fatalf("Get = %q, want trimmed secret-123", got)
	}

	// Second account is independent.
	if err := store.Set("other", "v2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("cloudflare-api-token"); got != "secret-123" {
		t.Fatalf("first account clobbered: %q", got)
	}

	if err := store.Delete("cloudflare-api-token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get("cloudflare-api-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
	// Delete is idempotent.
	if err := store.Delete("cloudflare-api-token"); err != nil {
		t.Fatalf("idempotent Delete: %v", err)
	}
}

func TestFileStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := NewFileStore(path)
	if err := store.Set("token", "x"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials file perm = %o, want 600", perm)
	}
}

func TestFileStoreEmptySecretRejected(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Set("token", "   "); err == nil {
		t.Fatal("Set with blank secret should error")
	}
}
