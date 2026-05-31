package mcpsetup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testEnv(t *testing.T, scope Scope) Env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	return Env{
		Scope:  scope,
		Server: Server{Name: "vtunnel", Command: "/usr/local/bin/vtunnel", Args: []string{"mcp", "serve"}},
	}
}

func TestJSONClientsRoundTrip(t *testing.T) {
	for _, id := range []string{"cursor", "vscode", "opencode", "antigravity"} {
		t.Run(id, func(t *testing.T) {
			env := testEnv(t, ScopeUser)
			client, ok := Find(id)
			if !ok {
				t.Fatalf("client %q not found", id)
			}
			ctx := context.Background()

			if st := client.Status(ctx, env); st.Configured {
				t.Fatalf("configured before add")
			}
			if err := client.Add(ctx, env); err != nil {
				t.Fatalf("add: %v", err)
			}
			if st := client.Status(ctx, env); !st.Configured {
				t.Fatalf("not configured after add (err=%v)", st.Err)
			}
			if err := client.Remove(ctx, env); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if st := client.Status(ctx, env); st.Configured {
				t.Fatalf("still configured after remove")
			}
		})
	}
}

func TestJSONEntryShapes(t *testing.T) {
	ctx := context.Background()

	// VS Code uses the "servers" key with type=stdio.
	env := testEnv(t, ScopeUser)
	vscode, _ := Find("vscode")
	if err := vscode.Add(ctx, env); err != nil {
		t.Fatal(err)
	}
	path, _ := vscodeUserMCPPath()
	entry := readEntry(t, path, "servers", "vtunnel")
	if entry["type"] != "stdio" {
		t.Fatalf("vscode entry type = %v, want stdio", entry["type"])
	}
	if entry["command"] != "/usr/local/bin/vtunnel" {
		t.Fatalf("vscode command = %v", entry["command"])
	}

	// OpenCode uses the "mcp" key with type=local and a command array.
	env2 := testEnv(t, ScopeUser)
	opencode, _ := Find("opencode")
	if err := opencode.Add(ctx, env2); err != nil {
		t.Fatal(err)
	}
	ocPath, _ := xdgConfigJoin("opencode", "opencode.json")
	oc := readEntry(t, ocPath, "mcp", "vtunnel")
	if oc["type"] != "local" {
		t.Fatalf("opencode type = %v, want local", oc["type"])
	}
	cmd, ok := oc["command"].([]any)
	if !ok || len(cmd) != 3 || cmd[0] != "/usr/local/bin/vtunnel" {
		t.Fatalf("opencode command = %v, want [bin mcp serve]", oc["command"])
	}
}

func readEntry(t *testing.T, path, mapKey, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	bucket, ok := m[mapKey].(map[string]any)
	if !ok {
		t.Fatalf("%s missing key %q: %v", path, mapKey, m)
	}
	entry, ok := bucket[name].(map[string]any)
	if !ok {
		t.Fatalf("%s missing entry %q", path, name)
	}
	return entry
}

func TestProjectScopeWritesProjectFile(t *testing.T) {
	env := testEnv(t, ScopeProject)
	env.ProjectDir = t.TempDir()
	cursor, _ := Find("cursor")
	if err := cursor.Add(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(env.ProjectDir, ".cursor", "mcp.json")); err != nil {
		t.Fatalf("project file not written: %v", err)
	}
}

func TestUpsertPreservesOtherEntries(t *testing.T) {
	env := testEnv(t, ScopeUser)
	path, _ := homeJoin(".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"mcpServers":{"other":{"command":"x"}},"otherTopKey":42}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	cursor, _ := Find("cursor")
	if err := cursor.Add(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["otherTopKey"] != float64(42) {
		t.Fatalf("top-level key clobbered: %v", m["otherTopKey"])
	}
	servers := m["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("sibling server entry clobbered")
	}
	if _, ok := servers["vtunnel"]; !ok {
		t.Fatal("vtunnel entry not added")
	}
}

func TestCLIClientArgs(t *testing.T) {
	srv := Server{Name: "vtunnel", Command: "/bin/vt", Args: []string{"mcp", "serve"}}

	claude := claudeClient().(cliClient)
	gotAdd := claude.addArgs(srv, Env{Scope: ScopeUser})
	wantAdd := []string{"mcp", "add", "--scope", "user", "vtunnel", "--", "/bin/vt", "mcp", "serve"}
	if !reflect.DeepEqual(gotAdd, wantAdd) {
		t.Fatalf("claude addArgs = %v, want %v", gotAdd, wantAdd)
	}
	gotRm := claude.removeArgs(srv, Env{Scope: ScopeProject})
	wantRm := []string{"mcp", "remove", "--scope", "project", "vtunnel"}
	if !reflect.DeepEqual(gotRm, wantRm) {
		t.Fatalf("claude removeArgs = %v, want %v", gotRm, wantRm)
	}

	codex := codexClient().(cliClient)
	gotCodex := codex.addArgs(srv, Env{Scope: ScopeUser})
	wantCodex := []string{"mcp", "add", "vtunnel", "--", "/bin/vt", "mcp", "serve"}
	if !reflect.DeepEqual(gotCodex, wantCodex) {
		t.Fatalf("codex addArgs = %v, want %v", gotCodex, wantCodex)
	}
}

func TestCLIClientAddAndStatusWithFakeRunner(t *testing.T) {
	var captured []string
	claude := claudeClient().(cliClient)
	claude.available = func() bool { return true }
	claude.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		captured = append([]string{name}, args...)
		return []byte("vtunnel\nother\n"), nil
	}
	env := Env{Scope: ScopeUser, Server: Server{Name: "vtunnel", Command: "/bin/vt", Args: []string{"mcp", "serve"}}}

	if err := claude.Add(context.Background(), env); err != nil {
		t.Fatalf("add: %v", err)
	}
	want := []string{"claude", "mcp", "add", "--scope", "user", "vtunnel", "--", "/bin/vt", "mcp", "serve"}
	if !reflect.DeepEqual(captured, want) {
		t.Fatalf("ran %v, want %v", captured, want)
	}
	if st := claude.Status(context.Background(), env); !st.Configured {
		t.Fatalf("status should report configured when list output contains the name")
	}
}

func TestManualInstructions(t *testing.T) {
	env := testEnv(t, ScopeUser)
	claude, _ := Find("claude")
	if m := claude.Manual(env); m.Command == "" {
		t.Fatal("claude manual should be a CLI command")
	}
	cursor, _ := Find("cursor")
	m := cursor.Manual(env)
	if m.Path == "" || m.Snippet == "" {
		t.Fatal("cursor manual should have a path and a snippet")
	}
	if !json.Valid([]byte(m.Snippet)) {
		t.Fatalf("cursor manual snippet is not valid JSON:\n%s", m.Snippet)
	}
}
