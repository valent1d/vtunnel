// Package service defines the OS-agnostic types for vtunnel's user services
// (the daemon and cloudflared). Platform backends — launchd on macOS, systemd
// --user on Linux — implement Manager over these shared Spec/Status types.
package service

import (
	"context"
	"os/exec"
	"path/filepath"
)

const (
	DaemonLabel      = "sh.vltn.vtunnel.daemon"
	CloudflaredLabel = "sh.vltn.vtunnel.cloudflared"
	// DefaultPath is the PATH the services run with, covering common Homebrew
	// and system locations on macOS and Linux.
	DefaultPath = "/opt/homebrew/bin:/usr/local/bin:/home/linuxbrew/.linuxbrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
)

// Runner runs an external command and returns its combined output. Injected so
// tests can avoid spawning launchctl/systemctl.
type Runner func(context.Context, string, ...string) ([]byte, error)

// ExecRunner is the default Runner.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Spec describes one user service.
type Spec struct {
	Name              string
	Label             string
	ProgramArguments  []string
	StandardOutPath   string
	StandardErrorPath string
	WorkingDirectory  string
	EnvironmentPath   string
}

// Status is a service's installed/running state.
type Status struct {
	Spec   Spec
	Path   string // backing config file (plist or unit)
	Exists bool   // the service definition is installed
	Loaded bool   // the service is loaded/running
	Output string
	Err    error
}

// Manager installs and controls user services. launchd and systemd implement it.
type Manager interface {
	Install(ctx context.Context, specs []Spec, start bool) error
	Start(ctx context.Context, specs []Spec) error
	Stop(ctx context.Context, specs []Spec) error
	Uninstall(ctx context.Context, specs []Spec) error
	Status(ctx context.Context, spec Spec) Status
	// Path returns the backing config file for a spec (plist or unit file).
	Path(spec Spec) string
}

// DaemonSpec builds the spec for the vtunnel daemon service.
func DaemonSpec(vtunnelPath, configPath, logsDir, home string) Spec {
	return Spec{
		Name:              "vtunnel daemon",
		Label:             DaemonLabel,
		ProgramArguments:  []string{vtunnelPath, "daemon", "--config", configPath},
		StandardOutPath:   filepath.Join(logsDir, "daemon.launchd.out.log"),
		StandardErrorPath: filepath.Join(logsDir, "daemon.launchd.err.log"),
		WorkingDirectory:  home,
		EnvironmentPath:   DefaultPath,
	}
}

// CloudflaredSpec builds the spec for the cloudflared service.
func CloudflaredSpec(cloudflaredPath, configPath, logsDir, home string) Spec {
	return Spec{
		Name:              "cloudflared",
		Label:             CloudflaredLabel,
		ProgramArguments:  []string{cloudflaredPath, "--config", configPath, "tunnel", "run"},
		StandardOutPath:   filepath.Join(logsDir, "cloudflared.launchd.out.log"),
		StandardErrorPath: filepath.Join(logsDir, "cloudflared.launchd.err.log"),
		WorkingDirectory:  home,
		EnvironmentPath:   DefaultPath,
	}
}
