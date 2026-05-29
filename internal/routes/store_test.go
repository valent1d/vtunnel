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

func TestValidateRejectsNonHTTPRoutes(t *testing.T) {
	err := Validate(Route{Hostname: "dev.example.test", Target: "file:///tmp/app"})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
