package routes

import (
	"path/filepath"
	"testing"
)

func TestStorePersistsRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Add(Route{Hostname: "Dev.Example.test", Target: "http://localhost:3000"}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	route, ok := reloaded.Get("dev.example.test")
	if !ok {
		t.Fatal("route was not persisted")
	}
	if route.Target != "http://localhost:3000" {
		t.Fatalf("target = %q", route.Target)
	}
}

func TestStorePersistsOrbstackMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Add(Route{
		Hostname: "doli23.example.test",
		Target:   "http://dolibarr-v23.orb.local",
		Orbstack: &OrbstackInfo{
			Container:     "dolibarr-v23",
			Image:         "dolibarr/dolibarr:23",
			OrbDomain:     "dolibarr-v23.orb.local",
			CustomDomains: []string{"doli23.local"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	route, ok := reloaded.Get("doli23.example.test")
	if !ok {
		t.Fatal("route was not persisted")
	}
	if route.Orbstack == nil {
		t.Fatal("orbstack metadata was not persisted")
	}
	if route.Orbstack.Container != "dolibarr-v23" || route.Orbstack.OrbDomain != "dolibarr-v23.orb.local" {
		t.Fatalf("orbstack metadata = %#v", route.Orbstack)
	}
	if len(route.Orbstack.CustomDomains) != 1 || route.Orbstack.CustomDomains[0] != "doli23.local" {
		t.Fatalf("custom domains = %v", route.Orbstack.CustomDomains)
	}
}

func TestPlainRouteHasNilOrbstack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store, _ := NewStore(path)
	if err := store.Add(Route{Hostname: "web.example.test", Target: "http://127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewStore(path)
	route, _ := reloaded.Get("web.example.test")
	if route.Orbstack != nil {
		t.Fatalf("plain route should have nil Orbstack, got %#v", route.Orbstack)
	}
}

func TestStorePersistsAccessMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store, _ := NewStore(path)
	if err := store.Add(Route{
		Hostname: "doli23.example.test",
		Target:   "http://127.0.0.1:8080",
		Access: &AccessInfo{
			AppID:     "app-1",
			PolicyIDs: []string{"pol-1"},
			Mode:      "sso",
			IdP:       "Authentik",
			Allow:     []string{"@progiseize.com"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewStore(path)
	route, ok := reloaded.Get("doli23.example.test")
	if !ok || route.Access == nil {
		t.Fatalf("access metadata not persisted: %+v", route)
	}
	if route.Access.AppID != "app-1" || route.Access.Mode != "sso" || route.Access.IdP != "Authentik" {
		t.Fatalf("access = %+v", route.Access)
	}
}

func TestValidateRejectsNonHTTPRoutes(t *testing.T) {
	err := Validate(Route{Hostname: "dev.example.test", Target: "file:///tmp/app"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
