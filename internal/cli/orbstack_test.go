package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"vtunnel/internal/config"
	"vtunnel/internal/orbstack"
	"vtunnel/internal/routes"
)

const orbstackInspectJSON = `[
  {"Name":"/dolibarr-v23","Config":{"Image":"dolibarr/dolibarr:23","Labels":{"dev.orbstack.domains":"doli23.local"},"ExposedPorts":{"80/tcp":{}}}},
  {"Name":"/doli-db","Config":{"Image":"mariadb:lts","Labels":{},"ExposedPorts":{"3306/tcp":{}}}}
]`

func fakeOrbstackDocker(t *testing.T) orbstack.Runner {
	t.Helper()
	return func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case len(args) == 0:
			return nil, errors.New("no args")
		case args[0] == "ps":
			return []byte("id1\nid2\n"), nil
		case args[0] == "inspect":
			return []byte(orbstackInspectJSON), nil
		case args[0] == "context":
			return []byte("orbstack\n"), nil
		}
		return nil, errors.New("unexpected docker call: " + strings.Join(args, " "))
	}
}

func withFakeOrbstack(t *testing.T) {
	t.Helper()
	previous := orbstackRunner
	orbstackRunner = fakeOrbstackDocker(t)
	t.Cleanup(func() { orbstackRunner = previous })
}

func TestResolveHTTPTarget(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "3000", want: "http://127.0.0.1:3000"},
		{in: "web.orb.local", want: "http://web.orb.local"},
		{in: "web.orb.local:8080", want: "http://web.orb.local:8080"},
		{in: "https://secure.orb.local", want: "https://secure.orb.local"},
		{in: "ftp://nope", wantErr: true},
		{in: "0", wantErr: true},
	}
	for _, tc := range cases {
		got, err := resolveHTTPTarget(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("resolveHTTPTarget(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("resolveHTTPTarget(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("resolveHTTPTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOrbstackListOutput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withFakeOrbstack(t)

	out, err := executeCommand(context.Background(), "orbstack", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"dolibarr-v23", "dolibarr-v23.orb.local", "yes",
		"doli-db", "doli-db.orb.local", "no",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("orbstack list output = %q, want %q", out, want)
		}
	}
}

func TestOrbstackExposeUnknownContainer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	withFakeOrbstack(t)

	out, err := executeCommand(context.Background(), "orbstack", "expose", "does-not-exist")
	if err == nil {
		t.Fatalf("expected error for unknown container, output: %q", out)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want it to mention 'not found'", err)
	}
}

func TestReconcileCreatesRoutesForRunningContainers(t *testing.T) {
	cfg := config.Config{DefaultDomain: "example.test"}
	containers := []orbstack.Container{
		{Name: "dolibarr-v23", OrbDomain: "dolibarr-v23.orb.local", CustomDomains: []string{"doli23.local"}, HTTP: true},
		{Name: "doli-db", OrbDomain: "doli-db.orb.local", HTTP: false}, // non-HTTP: ignored
	}

	create, remove, err := reconcileOrbstackRoutes(containers, nil, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(remove) != 0 {
		t.Fatalf("nothing to remove, got %v", remove)
	}
	if len(create) != 1 {
		t.Fatalf("expected 1 route to create, got %d (%+v)", len(create), create)
	}
	r := create[0]
	if r.Hostname != "doli23.example.test" {
		t.Fatalf("hostname = %q, want doli23.example.test", r.Hostname)
	}
	if r.Target != "http://dolibarr-v23.orb.local" {
		t.Fatalf("target = %q", r.Target)
	}
	if r.Orbstack == nil || !r.Orbstack.Managed {
		t.Fatalf("created route should be watch-managed: %+v", r.Orbstack)
	}
}

func TestReconcileSkipsAlreadyRoutedContainers(t *testing.T) {
	cfg := config.Config{DefaultDomain: "example.test"}
	containers := []orbstack.Container{
		{Name: "dolibarr-v23", OrbDomain: "dolibarr-v23.orb.local", HTTP: true},
	}
	existing := []routes.Route{
		{Hostname: "manual.example.test", Target: "http://dolibarr-v23.orb.local"}, // manual, no metadata
	}
	create, remove, err := reconcileOrbstackRoutes(containers, existing, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(create) != 0 {
		t.Fatalf("should not re-create an already-routed container, got %+v", create)
	}
	if len(remove) != 0 {
		t.Fatalf("should not remove anything, got %v", remove)
	}
}

func TestReconcileRemovesManagedRouteWhenContainerGone(t *testing.T) {
	cfg := config.Config{DefaultDomain: "example.test"}
	existing := []routes.Route{
		{
			Hostname: "doli23.example.test",
			Target:   "http://dolibarr-v23.orb.local",
			Orbstack: &routes.OrbstackInfo{Container: "dolibarr-v23", OrbDomain: "dolibarr-v23.orb.local", Managed: true},
		},
		{
			Hostname: "kept.example.test",
			Target:   "http://other.orb.local",
			Orbstack: &routes.OrbstackInfo{Container: "other", OrbDomain: "other.orb.local"}, // manual: not managed
		},
	}
	// No containers running.
	create, remove, err := reconcileOrbstackRoutes(nil, existing, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(create) != 0 {
		t.Fatalf("nothing to create, got %+v", create)
	}
	if len(remove) != 1 || remove[0] != "doli23.example.test" {
		t.Fatalf("should remove only the managed orphan, got %v", remove)
	}
}

func TestReconcileErrorsWithoutDomain(t *testing.T) {
	containers := []orbstack.Container{{Name: "web", OrbDomain: "web.orb.local", HTTP: true}}
	if _, _, err := reconcileOrbstackRoutes(containers, nil, config.Config{}, ""); err == nil {
		t.Fatal("expected error when no domain is configured")
	}
}
