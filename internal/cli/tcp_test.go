package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeTCPTarget(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "3306", want: "127.0.0.1:3306"},
		{in: "db.orb.local:3306", want: "db.orb.local:3306"},
		{in: "tcp://10.0.0.5:5432", want: "10.0.0.5:5432"},
		{in: "", wantErr: true},
		{in: "justahost", wantErr: true},
	}
	for _, tc := range cases {
		got, err := normalizeTCPTarget(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("normalizeTCPTarget(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("normalizeTCPTarget(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestPortFromService(t *testing.T) {
	if got := portFromService("tcp://127.0.0.1:3306"); got != 3306 {
		t.Fatalf("port = %d, want 3306", got)
	}
	if got := portFromService("http://127.0.0.1:8787"); got != 8787 {
		t.Fatalf("port = %d, want 8787", got)
	}
	if got := portFromService("tcp://hostonly"); got != 0 {
		t.Fatalf("port = %d, want 0", got)
	}
}

func TestTCPListShowsRules(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))

	cfDir := filepath.Join(home, ".cloudflared")
	if err := os.MkdirAll(cfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgYAML := `tunnel: t-1
credentials-file: /creds.json
ingress:
  - hostname: db.example.test
    service: tcp://127.0.0.1:3306
  - hostname: "*.example.test"
    service: http://127.0.0.1:8787
  - service: http_status:404
`
	if err := os.WriteFile(filepath.Join(cfDir, "config.yml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := executeCommand(context.Background(), "tcp", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "db.example.test") || !strings.Contains(out, "tcp://127.0.0.1:3306") {
		t.Fatalf("tcp list = %q", out)
	}
	if strings.Contains(out, "*.example.test") {
		t.Fatalf("tcp list should not show the HTTP wildcard rule: %q", out)
	}
}
