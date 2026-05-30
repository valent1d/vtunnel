package requestlog

import "testing"

func TestExchangeStoreGetAndEvict(t *testing.T) {
	store := NewExchangeStore(2)

	store.Put(Exchange{ID: 1, Path: "/a"})
	store.Put(Exchange{ID: 2, Path: "/b"})
	if got, ok := store.Get(1); !ok || got.Path != "/a" {
		t.Fatalf("get 1 = %+v, %v", got, ok)
	}

	// Adding a third evicts the oldest (1).
	store.Put(Exchange{ID: 3, Path: "/c"})
	if _, ok := store.Get(1); ok {
		t.Fatal("id 1 should have been evicted")
	}
	if _, ok := store.Get(2); !ok {
		t.Fatal("id 2 should be retained")
	}
	if _, ok := store.Get(3); !ok {
		t.Fatal("id 3 should be retained")
	}
	if store.Len() != 2 {
		t.Fatalf("len = %d, want 2", store.Len())
	}
}

func TestExchangeStoreUpdateInPlaceDoesNotEvict(t *testing.T) {
	store := NewExchangeStore(2)
	store.Put(Exchange{ID: 1})
	store.Put(Exchange{ID: 2})
	store.Put(Exchange{ID: 1, Path: "/updated"}) // same ID: update, not a new slot

	if got, ok := store.Get(2); !ok {
		t.Fatalf("id 2 should still be present: %+v", got)
	}
	if got, _ := store.Get(1); got.Path != "/updated" {
		t.Fatalf("id 1 path = %q, want /updated", got.Path)
	}
}
