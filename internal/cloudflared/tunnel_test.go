package cloudflared

import "testing"

func TestParseTunnelListIgnoresTrailingCloudflaredLogs(t *testing.T) {
	output := []byte(`[
  {"id":"tunnel-id","name":"dev","connections":[]}
]
{"level":"warn","message":"outdated"}
`)

	tunnels, err := parseTunnelList(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("tunnels = %#v, want one", tunnels)
	}
	if tunnels[0].ID != "tunnel-id" {
		t.Fatalf("tunnel id = %q", tunnels[0].ID)
	}
}

func TestParseCreateTunnelResultIgnoresTrailingCloudflaredLogs(t *testing.T) {
	output := []byte(`{"id":"tunnel-id","name":"vtunnel"}
{"level":"warn","message":"outdated"}
`)

	result, err := parseCreateTunnelResult(output)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "tunnel-id" || result.Name != "vtunnel" {
		t.Fatalf("result = %#v", result)
	}
}

func TestFindTunnel(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tunnel-id", Name: "dev"},
	}

	if tunnel, ok := FindTunnel(tunnels, "tunnel-id"); !ok || tunnel.Name != "dev" {
		t.Fatalf("find by id = %#v, %v", tunnel, ok)
	}
	if tunnel, ok := FindTunnel(tunnels, "dev"); !ok || tunnel.ID != "tunnel-id" {
		t.Fatalf("find by name = %#v, %v", tunnel, ok)
	}
	if _, ok := FindTunnel(tunnels, "missing"); ok {
		t.Fatal("missing tunnel should not be found")
	}
}
