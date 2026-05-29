package cloudflared

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseEdgeStatusPicksNewestLog(t *testing.T) {
	dir := t.TempDir()
	foreground := filepath.Join(dir, "cloudflared.log")
	service := filepath.Join(dir, "cloudflared.launchd.out.log")
	if err := os.WriteFile(foreground, []byte("INF Registered tunnel connection connIndex=0 ip=1.1.1.1 location=cdg01 protocol=quic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service, []byte("INF Registered tunnel connection connIndex=0 ip=2.2.2.2 location=dfw08 protocol=quic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make the service log the most recently written (the active cloudflared).
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(foreground, old, old); err != nil {
		t.Fatal(err)
	}

	status, err := ParseEdgeStatus(foreground, service)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Connections) != 1 || status.Connections[0].Location != "dfw08" {
		t.Fatalf("expected newest log (dfw08), got %+v", status.Connections)
	}

	// Missing paths are skipped rather than erroring.
	empty, err := ParseEdgeStatus(filepath.Join(dir, "nope.log"))
	if err != nil || len(empty.Connections) != 0 {
		t.Fatalf("missing path should be empty/no error, got %+v err=%v", empty, err)
	}
}

func TestParseEdgeStatus(t *testing.T) {
	log := `2026-05-29T12:00:00Z INF Starting tunnel tunnelID=demo-webapp
2026-05-29T12:00:00Z INF Version 2024.12.2
2026-05-29T12:00:00Z INF Starting metrics server on 127.0.0.1:20241/metrics
2026-05-29T12:00:01Z INF Registered tunnel connection connIndex=0 connection=demo-0 event=0 ip=198.41.192.77 location=dfw08 protocol=quic
2026-05-29T12:00:01Z INF Registered tunnel connection connIndex=1 connection=demo-1 event=0 ip=198.41.200.33 location=den01 protocol=quic
2026-05-29T12:00:02Z INF Registered tunnel connection connIndex=2 connection=demo-2 event=0 ip=198.41.200.13 location=iad02 protocol=quic
2026-05-29T12:00:05Z INF Unregistered tunnel connection connIndex=2
2026-05-29T12:00:06Z INF Registered tunnel connection connIndex=2 connection=demo-2b event=0 ip=198.41.200.99 location=lhr01 protocol=http2
`
	status := parseEdgeStatus(log)

	if status.Version != "2024.12.2" {
		t.Fatalf("Version = %q, want 2024.12.2", status.Version)
	}
	if status.MetricsAddr != "127.0.0.1:20241/metrics" {
		t.Fatalf("MetricsAddr = %q", status.MetricsAddr)
	}
	if len(status.Connections) != 3 {
		t.Fatalf("connections = %d, want 3 (conn 2 reconnected, not duplicated)", len(status.Connections))
	}
	// Ordered by index.
	if status.Connections[0].Index != 0 || status.Connections[2].Index != 2 {
		t.Fatalf("connections not ordered by index: %+v", status.Connections)
	}
	if got := status.Connections[0]; got.Location != "dfw08" || got.City != "Dallas" || got.Protocol != "quic" || got.IP != "198.41.192.77" {
		t.Fatalf("conn0 = %+v", got)
	}
	// Reconnected conn 2 reflects the latest registration (London, http2).
	if got := status.Connections[2]; got.Location != "lhr01" || got.City != "London" || got.Protocol != "http2" {
		t.Fatalf("conn2 should reflect reconnection: %+v", got)
	}
}

func TestColoCity(t *testing.T) {
	cases := map[string]string{"dfw08": "Dallas", "CDG": "Paris", "xyz99": "", "ab": ""}
	for colo, want := range cases {
		if got := coloCity(colo); got != want {
			t.Errorf("coloCity(%q) = %q, want %q", colo, got, want)
		}
	}
}
