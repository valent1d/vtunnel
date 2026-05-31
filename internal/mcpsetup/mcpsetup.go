// Package mcpsetup registers vtunnel's MCP server into the configuration of AI
// coding clients (Claude Code, Cursor, VS Code, Codex, OpenCode, Antigravity)
// and reports whether each is set up. Each client stores MCP servers in its own
// place and format, so a per-client adapter encapsulates those differences
// behind a small Client interface.
package mcpsetup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Scope is where a server registration is written.
type Scope string

const (
	// ScopeUser registers the server for the whole user (all projects).
	ScopeUser Scope = "user"
	// ScopeProject registers the server in the current project directory.
	ScopeProject Scope = "project"
)

// Server describes the MCP server entry vtunnel writes into clients.
type Server struct {
	Name    string   // registration key, e.g. "vtunnel"
	Command string   // absolute path to the vtunnel binary
	Args    []string // e.g. ["mcp", "serve"]
}

// DefaultServer builds the server entry for the running vtunnel binary.
func DefaultServer() (Server, error) {
	exe, err := os.Executable()
	if err != nil {
		return Server{}, err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return Server{Name: "vtunnel", Command: exe, Args: []string{"mcp", "serve"}}, nil
}

// Env carries the parameters an operation runs against.
type Env struct {
	Scope      Scope
	ProjectDir string // used for ScopeProject; defaults to the working directory
	Server     Server
}

// Status is a client's setup state.
type Status struct {
	ID         string
	Name       string
	Installed  bool   // the client appears to be installed
	Configured bool   // the vtunnel server is registered
	Location   string // config file path, or "<bin> CLI"
	Note       string // human-readable extra detail (errors, hints)
	Err        error
}

// Manual is the guided manual-setup instruction for a client: either a CLI
// command to run, or a config file path plus a snippet to paste.
type Manual struct {
	Path    string
	Command string
	Snippet string
}

// Client is one AI coding client vtunnel can register itself with.
type Client interface {
	ID() string
	Name() string
	Detect(ctx context.Context, env Env) (installed bool, location string)
	Status(ctx context.Context, env Env) Status
	Add(ctx context.Context, env Env) error
	Remove(ctx context.Context, env Env) error
	Manual(env Env) Manual
}

// Clients returns every supported client adapter, in display order.
func Clients() []Client {
	return []Client{
		claudeClient(),
		cursorClient(),
		vscodeClient(),
		codexClient(),
		opencodeClient(),
		antigravityClient(),
	}
}

// Find returns the client with the given id.
func Find(id string) (Client, bool) {
	for _, client := range Clients() {
		if client.ID() == id {
			return client, true
		}
	}
	return nil, false
}

// SelfCheck launches `vtunnel mcp serve` as a subprocess and performs an MCP
// handshake, returning the advertised tool names. It validates that the server
// the clients will spawn actually starts and responds.
func SelfCheck(ctx context.Context, srv Server) ([]string, error) {
	cmd := exec.CommandContext(ctx, srv.Command, srv.Args...)
	transport := &mcp.CommandTransport{Command: cmd}
	client := mcp.NewClient(&mcp.Implementation{Name: "vtunnel-selfcheck", Version: "dev"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	var tools []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return tools, err
		}
		tools = append(tools, tool.Name)
	}
	return tools, nil
}

func binaryOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
