package orbstack

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeDocker returns canned output per docker subcommand.
func fakeDocker(ps string, inspect string) Runner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, errors.New("no args")
		}
		switch args[0] {
		case "ps":
			return []byte(ps), nil
		case "inspect":
			return []byte(inspect), nil
		case "context":
			return []byte("orbstack\n"), nil
		}
		return nil, errors.New("unexpected docker call: " + strings.Join(args, " "))
	}
}

const sampleInspect = `[
  {"Name":"/dolibarr-v23","Config":{"Image":"dolibarr/dolibarr:23","Labels":{"dev.orbstack.domains":"doli23.local","com.docker.compose.project":"doli_local","com.docker.compose.service":"dolibarr-v23"},"ExposedPorts":{"80/tcp":{}}}},
  {"Name":"/doli-db","Config":{"Image":"mariadb:lts","Labels":{},"ExposedPorts":{"3306/tcp":{}}}}
]`

func TestListParsesContainers(t *testing.T) {
	client := New(fakeDocker("id1\nid2\n", sampleInspect))
	containers, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 2 {
		t.Fatalf("got %d containers, want 2", len(containers))
	}

	web := containers[0] // sorted by name: doli-db < dolibarr-v23? "doli-db" < "dolibarr-v23"
	db := containers[1]
	if web.Name != "doli-db" || db.Name != "dolibarr-v23" {
		t.Fatalf("unexpected sort order: %s, %s", web.Name, db.Name)
	}

	if db.OrbDomain != "dolibarr-v23.orb.local" {
		t.Fatalf("orb domain = %q", db.OrbDomain)
	}
	if db.Target() != "http://dolibarr-v23.orb.local" {
		t.Fatalf("target = %q", db.Target())
	}
	if !db.HTTP {
		t.Fatal("dolibarr-v23 (port 80) should be HTTP")
	}
	if web.HTTP {
		t.Fatal("doli-db (port 3306) should not be HTTP")
	}
	if len(db.CustomDomains) != 1 || db.CustomDomains[0] != "doli23.local" {
		t.Fatalf("custom domains = %v", db.CustomDomains)
	}
}

func TestDefaultSubdomainPrefersCustomDomain(t *testing.T) {
	withCustom := Container{Name: "dolibarr-v23", CustomDomains: []string{"doli23.local"}}
	if got := withCustom.DefaultSubdomain(); got != "doli23" {
		t.Fatalf("subdomain = %q, want doli23", got)
	}

	noCustom := Container{Name: "my_app_1"}
	if got := noCustom.DefaultSubdomain(); got != "my-app-1" {
		t.Fatalf("subdomain = %q, want my-app-1", got)
	}
}

func TestFindContainer(t *testing.T) {
	containers := []Container{
		{Name: "dolibarr-v23", OrbDomain: "dolibarr-v23.orb.local", CustomDomains: []string{"doli23.local"}},
	}
	for _, ref := range []string{"dolibarr-v23", "dolibarr-v23.orb.local", "doli23"} {
		if _, ok := FindContainer(containers, ref); !ok {
			t.Fatalf("expected to find container by %q", ref)
		}
	}
	if _, ok := FindContainer(containers, "nope"); ok {
		t.Fatal("did not expect to find unknown container")
	}
}

func TestListEmptyWhenNoContainers(t *testing.T) {
	client := New(fakeDocker("\n", "[]"))
	containers, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 0 {
		t.Fatalf("expected no containers, got %d", len(containers))
	}
}

func TestAvailableDetectsOrbStackContext(t *testing.T) {
	if !Available(context.Background(), fakeDocker("", "")) {
		t.Fatal("expected OrbStack to be available")
	}
	notOrb := func(context.Context, ...string) ([]byte, error) { return []byte("default\n"), nil }
	if Available(context.Background(), notOrb) {
		t.Fatal("expected non-orbstack context to be unavailable")
	}
}
