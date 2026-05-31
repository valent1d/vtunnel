// Package systemd manages vtunnel's user services on Linux via systemd --user.
// It implements service.Manager over the shared Spec/Status types, mirroring the
// launchd backend used on macOS: it writes ~/.config/systemd/user/*.service unit
// files and drives them with `systemctl --user`.
package systemd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vtunnel/internal/service"
)

type Manager struct {
	Home   string
	Runner service.Runner
}

func New(runner service.Runner) (Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Manager{}, fmt.Errorf("find home directory: %w", err)
	}
	if runner == nil {
		runner = service.ExecRunner
	}
	return Manager{Home: home, Runner: runner}, nil
}

func NewForTest(home string, runner service.Runner) Manager {
	if runner == nil {
		runner = service.ExecRunner
	}
	return Manager{Home: home, Runner: runner}
}

func (m Manager) unitDir() string {
	if base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); base != "" {
		return filepath.Join(base, "systemd", "user")
	}
	return filepath.Join(m.Home, ".config", "systemd", "user")
}

// Path is the unit file backing a spec.
func (m Manager) Path(spec service.Spec) string {
	return filepath.Join(m.unitDir(), unitName(spec))
}

func unitName(spec service.Spec) string { return spec.Label + ".service" }

func (m Manager) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return m.Runner(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

func (m Manager) Install(ctx context.Context, specs []service.Spec, start bool) error {
	for _, spec := range specs {
		if err := m.Write(spec); err != nil {
			return err
		}
	}
	if !start {
		return nil
	}
	if out, err := m.systemctl(ctx, "daemon-reload"); err != nil {
		return wrap("reload systemd", out, err)
	}
	for _, spec := range specs {
		if out, err := m.systemctl(ctx, "enable", "--now", unitName(spec)); err != nil {
			return wrap("enable "+spec.Name, out, err)
		}
	}
	return nil
}

func (m Manager) Start(ctx context.Context, specs []service.Spec) error {
	for _, spec := range specs {
		if _, err := os.Stat(m.Path(spec)); err != nil {
			return fmt.Errorf("%s service is not installed; run vtunnel service install", spec.Name)
		}
		_, _ = m.systemctl(ctx, "daemon-reload")
		_, _ = m.systemctl(ctx, "enable", unitName(spec))
		if out, err := m.systemctl(ctx, "restart", unitName(spec)); err != nil {
			return wrap("start "+spec.Name, out, err)
		}
	}
	return nil
}

func (m Manager) Stop(ctx context.Context, specs []service.Spec) error {
	for _, spec := range specs {
		if out, err := m.systemctl(ctx, "stop", unitName(spec)); err != nil {
			return wrap("stop "+spec.Name, out, err)
		}
	}
	return nil
}

func (m Manager) Uninstall(ctx context.Context, specs []service.Spec) error {
	for _, spec := range specs {
		_, _ = m.systemctl(ctx, "disable", "--now", unitName(spec))
		if err := os.Remove(m.Path(spec)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s unit: %w", spec.Name, err)
		}
	}
	_, _ = m.systemctl(ctx, "daemon-reload")
	return nil
}

func (m Manager) Status(ctx context.Context, spec service.Spec) service.Status {
	path := m.Path(spec)
	status := service.Status{Spec: spec, Path: path}
	if _, err := os.Stat(path); err == nil {
		status.Exists = true
	} else if !os.IsNotExist(err) {
		status.Err = err
	}
	// `is-active` exits non-zero when the unit is inactive, which is expected;
	// only the "active" output marks it loaded.
	output, _ := m.systemctl(ctx, "is-active", unitName(spec))
	status.Output = strings.TrimSpace(string(output))
	if status.Output == "active" {
		status.Loaded = true
	}
	return status
}

func (m Manager) Write(spec service.Spec) error {
	if len(spec.ProgramArguments) == 0 || spec.ProgramArguments[0] == "" {
		return fmt.Errorf("%s service has no executable", spec.Name)
	}
	path := m.Path(spec)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.StandardOutPath), 0o755); err != nil {
		return fmt.Errorf("create service log directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(RenderUnit(spec)), 0o644); err != nil {
		return fmt.Errorf("write %s unit: %w", spec.Name, err)
	}
	return nil
}

// RenderUnit renders a systemd user .service file for the spec. Output goes to
// the same log files the launchd backend uses, so `vtunnel logs` works the same.
func RenderUnit(spec service.Spec) string {
	path := spec.EnvironmentPath
	if path == "" {
		path = service.DefaultPath
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", spec.Name)
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", execStart(spec.ProgramArguments))
	if spec.WorkingDirectory != "" {
		fmt.Fprintf(&b, "WorkingDirectory=%s\n", spec.WorkingDirectory)
	}
	fmt.Fprintf(&b, "Environment=PATH=%s\n", path)
	if spec.StandardOutPath != "" {
		fmt.Fprintf(&b, "StandardOutput=append:%s\n", spec.StandardOutPath)
	}
	if spec.StandardErrorPath != "" {
		fmt.Fprintf(&b, "StandardError=append:%s\n", spec.StandardErrorPath)
	}
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String()
}

// execStart joins program arguments for an ExecStart line, quoting any that
// contain whitespace or quotes (systemd splits on whitespace).
func execStart(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, " \t\"") {
			parts[i] = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " ")
}

func wrap(action string, output []byte, err error) error {
	msg := strings.TrimSpace(string(output))
	if msg == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, msg)
}
