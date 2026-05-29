package requestlog

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsAndFiltersEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	store, err := NewStore(path, DefaultMaxEntries)
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Add(Entry{
		Time:     time.Now(),
		Hostname: "Dev.Example.Test",
		Method:   "GET",
		Path:     "/one",
		Status:   200,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Add(Entry{
		Time:     time.Now(),
		Hostname: "api.example.test",
		Method:   "POST",
		Path:     "/two",
		Status:   201,
	})
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(path, DefaultMaxEntries)
	if err != nil {
		t.Fatal(err)
	}

	entries := reloaded.List(Filter{Hostname: "dev.example.test"})
	if len(entries) != 1 {
		t.Fatalf("entries length = %d, want 1", len(entries))
	}
	if entries[0].Hostname != "dev.example.test" {
		t.Fatalf("hostname = %q", entries[0].Hostname)
	}

	entries = reloaded.List(Filter{AfterID: first.ID})
	if len(entries) != 1 {
		t.Fatalf("entries after first length = %d, want 1", len(entries))
	}
	if entries[0].Path != "/two" {
		t.Fatalf("path = %q", entries[0].Path)
	}
}

func TestStoreRespectsLimit(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "requests.jsonl"), 2)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/one", "/two", "/three"} {
		if _, err := store.Add(Entry{Hostname: "dev.example.test", Method: "GET", Path: path, Status: 200}); err != nil {
			t.Fatal(err)
		}
	}

	entries := store.List(Filter{})
	if len(entries) != 2 {
		t.Fatalf("entries length = %d, want 2", len(entries))
	}
	if entries[0].Path != "/two" || entries[1].Path != "/three" {
		t.Fatalf("entries = %#v", entries)
	}
}
