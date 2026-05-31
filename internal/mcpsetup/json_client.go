package mcpsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// jsonClient is a client whose MCP servers live in a JSON config file under a
// single map key (e.g. "mcpServers" or "servers"). The mapKey, the per-entry
// shape, and the file location vary per client and are supplied as fields.
type jsonClient struct {
	id, name string
	mapKey   string
	userPath func() (string, error)
	projPath func(projectDir string) string
	entry    func(srv Server) map[string]any
	detect   func() bool // whether the client looks installed (user scope)
}

func (c jsonClient) ID() string   { return c.id }
func (c jsonClient) Name() string { return c.name }

func (c jsonClient) path(env Env) (string, error) {
	if env.Scope == ScopeProject {
		dir := env.ProjectDir
		if dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			dir = wd
		}
		return c.projPath(dir), nil
	}
	return c.userPath()
}

func (c jsonClient) Detect(_ context.Context, env Env) (bool, string) {
	path, err := c.path(env)
	if err != nil {
		return false, ""
	}
	if env.Scope == ScopeProject {
		return true, path
	}
	return c.detect(), path
}

func (c jsonClient) Status(_ context.Context, env Env) Status {
	st := Status{ID: c.id, Name: c.name}
	path, err := c.path(env)
	if err != nil {
		st.Err = err
		return st
	}
	st.Location = path
	st.Installed = env.Scope == ScopeProject || c.detect()
	ok, perr := jsonEntryPresent(path, c.mapKey, env.Server.Name)
	if perr != nil {
		st.Err = perr
		return st
	}
	st.Configured = ok
	return st
}

func (c jsonClient) Add(_ context.Context, env Env) error {
	path, err := c.path(env)
	if err != nil {
		return err
	}
	return jsonUpsertEntry(path, c.mapKey, env.Server.Name, c.entry(env.Server))
}

func (c jsonClient) Remove(_ context.Context, env Env) error {
	path, err := c.path(env)
	if err != nil {
		return err
	}
	_, err = jsonRemoveEntry(path, c.mapKey, env.Server.Name)
	return err
}

func (c jsonClient) Manual(env Env) Manual {
	path, _ := c.path(env)
	wrapper := map[string]any{c.mapKey: map[string]any{env.Server.Name: c.entry(env.Server)}}
	snippet, _ := json.MarshalIndent(wrapper, "", "  ")
	return Manual{Path: path, Snippet: string(snippet)}
}

// --- JSON file helpers ---

func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func writeJSONObject(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func jsonBucket(m map[string]any, mapKey string) map[string]any {
	if bucket, ok := m[mapKey].(map[string]any); ok && bucket != nil {
		return bucket
	}
	return map[string]any{}
}

func jsonUpsertEntry(path, mapKey, name string, entry map[string]any) error {
	m, err := readJSONObject(path)
	if err != nil {
		return err
	}
	bucket := jsonBucket(m, mapKey)
	bucket[name] = entry
	m[mapKey] = bucket
	return writeJSONObject(path, m)
}

func jsonRemoveEntry(path, mapKey, name string) (bool, error) {
	m, err := readJSONObject(path)
	if err != nil {
		return false, err
	}
	bucket := jsonBucket(m, mapKey)
	if _, ok := bucket[name]; !ok {
		return false, nil
	}
	delete(bucket, name)
	m[mapKey] = bucket
	return true, writeJSONObject(path, m)
}

func jsonEntryPresent(path, mapKey, name string) (bool, error) {
	m, err := readJSONObject(path)
	if err != nil {
		return false, err
	}
	_, ok := jsonBucket(m, mapKey)[name]
	return ok, nil
}

// --- path helpers ---

func homeJoin(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}

func xdgConfigJoin(parts ...string) (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(append([]string{base}, parts...)...), nil
	}
	return homeJoin(append([]string{".config"}, parts...)...)
}

func vscodeUserMCPPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "mcp.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Code", "User", "mcp.json"), nil
	default:
		return xdgConfigJoin("Code", "User", "mcp.json")
	}
}

func parentDirExists(path string, err error) bool {
	if err != nil {
		return false
	}
	return dirExists(filepath.Dir(path))
}

// --- JSON client constructors ---

func cursorClient() Client {
	return jsonClient{
		id:       "cursor",
		name:     "Cursor",
		mapKey:   "mcpServers",
		userPath: func() (string, error) { return homeJoin(".cursor", "mcp.json") },
		projPath: func(dir string) string { return filepath.Join(dir, ".cursor", "mcp.json") },
		entry: func(srv Server) map[string]any {
			return map[string]any{"command": srv.Command, "args": srv.Args}
		},
		detect: func() bool {
			path, err := homeJoin(".cursor")
			return (err == nil && dirExists(path)) || binaryOnPath("cursor")
		},
	}
}

func vscodeClient() Client {
	return jsonClient{
		id:       "vscode",
		name:     "VS Code",
		mapKey:   "servers",
		userPath: vscodeUserMCPPath,
		projPath: func(dir string) string { return filepath.Join(dir, ".vscode", "mcp.json") },
		entry: func(srv Server) map[string]any {
			return map[string]any{"type": "stdio", "command": srv.Command, "args": srv.Args}
		},
		detect: func() bool {
			return parentDirExists(vscodeUserMCPPath()) || binaryOnPath("code")
		},
	}
}

func opencodeClient() Client {
	return jsonClient{
		id:       "opencode",
		name:     "OpenCode",
		mapKey:   "mcp",
		userPath: func() (string, error) { return xdgConfigJoin("opencode", "opencode.json") },
		projPath: func(dir string) string { return filepath.Join(dir, "opencode.json") },
		entry: func(srv Server) map[string]any {
			command := append([]string{srv.Command}, srv.Args...)
			return map[string]any{"type": "local", "command": command, "enabled": true}
		},
		detect: func() bool {
			path, err := xdgConfigJoin("opencode")
			return (err == nil && dirExists(path)) || binaryOnPath("opencode")
		},
	}
}

func antigravityClient() Client {
	userPath := func() (string, error) { return homeJoin(".gemini", "antigravity", "mcp_config.json") }
	return jsonClient{
		id:       "antigravity",
		name:     "Antigravity",
		mapKey:   "mcpServers",
		userPath: userPath,
		// Antigravity has no documented project-scoped config; fall back to user.
		projPath: func(string) string { p, _ := userPath(); return p },
		entry: func(srv Server) map[string]any {
			return map[string]any{"command": srv.Command, "args": srv.Args}
		},
		detect: func() bool {
			path, err := homeJoin(".gemini")
			return err == nil && dirExists(path)
		},
	}
}
