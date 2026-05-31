package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// closedPortAddr returns a loopback address that nothing is listening on, so a
// dial to it is refused immediately (used to force the api client's fallback).
func closedPortAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// isolateMCPEnv points HOME/XDG at temp dirs and writes a config whose daemon
// ports are closed, so MCP handlers fall back to (empty) saved files instead of
// touching a real daemon or the user's real config.
func isolateMCPEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "vtunnel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "default_domain: example.test\n" +
		"proxy:\n  listen: " + closedPortAddr(t) + "\n" +
		"api:\n  listen: " + closedPortAddr(t) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// connectMCP starts an in-memory MCP client/server pair for the test.
func connectMCP(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := buildMCPServer("")
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestMCPExposesTools(t *testing.T) {
	isolateMCPEnv(t)
	session := connectMCP(t)

	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	sort.Strings(names)

	want := []string{"create_http_tunnel", "inspect_requests", "list_tunnels", "protect_tunnel", "replay_request", "stop_tunnel", "unprotect_tunnel"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}
}

func TestMCPExposesRequestsResource(t *testing.T) {
	isolateMCPEnv(t)
	session := connectMCP(t)

	found := false
	for res, err := range session.Resources(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if res.URI == "vtunnel://requests" {
			found = true
		}
	}
	if !found {
		t.Fatal("vtunnel://requests resource not advertised")
	}
}

func TestMCPListTunnelsFallsBackToSavedFile(t *testing.T) {
	isolateMCPEnv(t)
	session := connectMCP(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_tunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("list_tunnels returned an error result: %+v", res.Content)
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %T, want object", res.StructuredContent)
	}
	if out["source"] != "saved file (daemon not running)" {
		t.Fatalf("source = %v, want saved-file fallback", out["source"])
	}
}

func TestMCPInspectRequestsEmpty(t *testing.T) {
	isolateMCPEnv(t)
	session := connectMCP(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "inspect_requests",
		Arguments: map[string]any{"limit": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("inspect_requests returned an error result: %+v", res.Content)
	}
}
