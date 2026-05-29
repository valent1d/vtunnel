package cloudflared

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Tunnel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreatedAt   string `json:"created_at"`
	DeletedAt   string `json:"deleted_at"`
	Connections []any  `json:"connections"`
}

type CreateTunnelResult struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	CredentialsFile string
}

type TunnelRunner struct {
	Path string
}

func NewTunnelRunner(path string) TunnelRunner {
	return TunnelRunner{Path: strings.TrimSpace(path)}
}

func (runner TunnelRunner) List(ctx context.Context) ([]Tunnel, error) {
	path := runner.Path
	if path == "" {
		var err error
		path, err = exec.LookPath("cloudflared")
		if err != nil {
			return nil, err
		}
	}

	output, err := exec.CommandContext(ctx, path, "tunnel", "list", "--output", "json").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("cloudflared tunnel list: %s", strings.TrimSpace(string(output)))
	}

	tunnels, err := parseTunnelList(output)
	if err != nil {
		return nil, fmt.Errorf("parse cloudflared tunnel list: %w", err)
	}
	return tunnels, nil
}

func (runner TunnelRunner) RouteDNS(ctx context.Context, tunnel string, hostname string, overwrite bool) error {
	path := runner.Path
	if path == "" {
		var err error
		path, err = exec.LookPath("cloudflared")
		if err != nil {
			return err
		}
	}

	args := []string{"tunnel", "route", "dns"}
	if overwrite {
		args = append(args, "--overwrite-dns")
	}
	args = append(args, strings.TrimSpace(tunnel), strings.TrimSpace(hostname))

	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cloudflared tunnel route dns: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (runner TunnelRunner) Create(ctx context.Context, name string, credentialsFile string) (CreateTunnelResult, error) {
	path := runner.Path
	if path == "" {
		var err error
		path, err = exec.LookPath("cloudflared")
		if err != nil {
			return CreateTunnelResult{}, err
		}
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return CreateTunnelResult{}, fmt.Errorf("tunnel name is required")
	}
	credentialsFile = strings.TrimSpace(credentialsFile)
	args := []string{"tunnel", "create", "--output", "json"}
	if credentialsFile != "" {
		args = append(args, "--credentials-file", credentialsFile)
	}
	args = append(args, name)
	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return CreateTunnelResult{}, fmt.Errorf("cloudflared tunnel create: %s", strings.TrimSpace(string(output)))
	}

	result, err := parseCreateTunnelResult(output)
	if err != nil {
		return CreateTunnelResult{}, fmt.Errorf("parse cloudflared tunnel create: %w", err)
	}
	if credentialsFile == "" {
		credentialsFile = defaultCredentialsFile(result.ID)
	}
	result.CredentialsFile = filepath.Clean(credentialsFile)
	return result, nil
}

func defaultCredentialsFile(tunnelID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cloudflared", tunnelID+".json")
}

func parseTunnelList(output []byte) ([]Tunnel, error) {
	var tunnels []Tunnel
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&tunnels); err != nil {
		return nil, err
	}
	return tunnels, nil
}

func parseCreateTunnelResult(output []byte) (CreateTunnelResult, error) {
	var result CreateTunnelResult
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if strings.TrimSpace(result.ID) == "" {
		return result, fmt.Errorf("missing tunnel id in cloudflared output")
	}
	return result, nil
}

func FindTunnel(tunnels []Tunnel, idOrName string) (Tunnel, bool) {
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		return Tunnel{}, false
	}
	for _, tunnel := range tunnels {
		if tunnel.ID == idOrName || tunnel.Name == idOrName {
			return tunnel, true
		}
	}
	return Tunnel{}, false
}
