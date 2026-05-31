package mcpsetup

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runner runs an external command and returns its combined output. It is a
// field so tests can inject a fake without spawning real processes.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// cliClient is a client managed through its own CLI (e.g. `claude mcp add`),
// which is more robust than editing its config file by hand.
type cliClient struct {
	id, name, bin string
	run           runner
	available     func() bool // install check; injectable for tests
	addArgs       func(srv Server, env Env) []string
	removeArgs    func(srv Server, env Env) []string
	listArgs      func(env Env) []string
	manual        func(srv Server, env Env) string
}

func (c cliClient) ID() string   { return c.id }
func (c cliClient) Name() string { return c.name }

func (c cliClient) installed() bool {
	if c.available != nil {
		return c.available()
	}
	return binaryOnPath(c.bin)
}

func (c cliClient) Detect(_ context.Context, _ Env) (bool, string) {
	return c.installed(), c.bin + " CLI"
}

func (c cliClient) Status(ctx context.Context, env Env) Status {
	st := Status{ID: c.id, Name: c.name, Location: c.bin + " CLI"}
	if !c.installed() {
		st.Note = c.bin + " not found on PATH"
		return st
	}
	st.Installed = true
	out, err := c.run(ctx, c.bin, c.listArgs(env)...)
	if err != nil {
		st.Note = "could not query " + c.bin + ": " + firstLine(out)
		return st
	}
	st.Configured = bytes.Contains(out, []byte(env.Server.Name))
	return st
}

func (c cliClient) Add(ctx context.Context, env Env) error {
	if !c.installed() {
		return fmt.Errorf("%s not found on PATH", c.bin)
	}
	if out, err := c.run(ctx, c.bin, c.addArgs(env.Server, env)...); err != nil {
		return fmt.Errorf("%s: %s", c.bin, firstLine(out))
	}
	return nil
}

func (c cliClient) Remove(ctx context.Context, env Env) error {
	if !c.installed() {
		return fmt.Errorf("%s not found on PATH", c.bin)
	}
	if out, err := c.run(ctx, c.bin, c.removeArgs(env.Server, env)...); err != nil {
		return fmt.Errorf("%s: %s", c.bin, firstLine(out))
	}
	return nil
}

func (c cliClient) Manual(env Env) Manual {
	return Manual{Command: c.manual(env.Server, env)}
}

func firstLine(out []byte) string {
	text := strings.TrimSpace(string(out))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	if text == "" {
		return "(no output)"
	}
	return text
}

// serverCommand returns [command, args...] for the "-- <cmd> <args>" tail both
// CLIs use.
func serverCommand(srv Server) []string {
	return append([]string{srv.Command}, srv.Args...)
}

func claudeScope(env Env) string {
	if env.Scope == ScopeProject {
		return "project"
	}
	return "user"
}

func claudeClient() Client {
	return cliClient{
		id:   "claude",
		name: "Claude Code",
		bin:  "claude",
		run:  execRunner,
		addArgs: func(srv Server, env Env) []string {
			args := []string{"mcp", "add", "--scope", claudeScope(env), srv.Name, "--"}
			return append(args, serverCommand(srv)...)
		},
		removeArgs: func(srv Server, env Env) []string {
			return []string{"mcp", "remove", "--scope", claudeScope(env), srv.Name}
		},
		listArgs: func(Env) []string { return []string{"mcp", "list"} },
		manual: func(srv Server, env Env) string {
			return "claude mcp add --scope " + claudeScope(env) + " " + srv.Name + " -- " + strings.Join(serverCommand(srv), " ")
		},
	}
}

func codexClient() Client {
	return cliClient{
		id:   "codex",
		name: "Codex",
		bin:  "codex",
		run:  execRunner,
		// Codex writes to ~/.codex/config.toml (global); it has no --scope flag.
		addArgs: func(srv Server, _ Env) []string {
			args := []string{"mcp", "add", srv.Name, "--"}
			return append(args, serverCommand(srv)...)
		},
		removeArgs: func(srv Server, _ Env) []string {
			return []string{"mcp", "remove", srv.Name}
		},
		listArgs: func(Env) []string { return []string{"mcp", "list"} },
		manual: func(srv Server, _ Env) string {
			return "codex mcp add " + srv.Name + " -- " + strings.Join(serverCommand(srv), " ")
		},
	}
}
