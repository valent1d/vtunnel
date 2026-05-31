package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"vtunnel/internal/service"
)

const launchAgentsSubdir = "Library/LaunchAgents"

// These alias the shared service types so existing launchd.X references keep
// working, while the systemd backend shares the same Spec/Status/constructors.
const (
	VtunnelDaemonLabel = service.DaemonLabel
	CloudflaredLabel   = service.CloudflaredLabel
	DefaultPath        = service.DefaultPath
)

type (
	Runner = service.Runner
	Spec   = service.Spec
	Status = service.Status
)

var ExecRunner = service.ExecRunner

var (
	VtunnelDaemonSpec = service.DaemonSpec
	CloudflaredSpec   = service.CloudflaredSpec
)

type Manager struct {
	Home   string
	UID    int
	Runner Runner
}

func New(runner Runner) (Manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Manager{}, fmt.Errorf("find home directory: %w", err)
	}
	if runner == nil {
		runner = ExecRunner
	}
	return Manager{Home: home, UID: os.Getuid(), Runner: runner}, nil
}

func NewForTest(home string, uid int, runner Runner) Manager {
	if runner == nil {
		runner = ExecRunner
	}
	return Manager{Home: home, UID: uid, Runner: runner}
}

func (m Manager) Install(ctx context.Context, specs []Spec, start bool) error {
	for _, spec := range specs {
		if err := m.Write(spec); err != nil {
			return err
		}
		if start {
			_ = m.bootout(ctx, spec)
			if err := m.bootstrap(ctx, spec); err != nil {
				return err
			}
			if err := m.enable(ctx, spec); err != nil {
				return err
			}
			if err := m.kickstart(ctx, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m Manager) Start(ctx context.Context, specs []Spec) error {
	for _, spec := range specs {
		if _, err := os.Stat(m.PlistPath(spec)); err != nil {
			return fmt.Errorf("%s service is not installed; run vtunnel service install", spec.Name)
		}
		status := m.Status(ctx, spec)
		if !status.Loaded {
			if err := m.bootstrap(ctx, spec); err != nil {
				return err
			}
		}
		if err := m.enable(ctx, spec); err != nil {
			return err
		}
		if err := m.kickstart(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) Stop(ctx context.Context, specs []Spec) error {
	for _, spec := range specs {
		if err := m.bootout(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}

func (m Manager) Uninstall(ctx context.Context, specs []Spec) error {
	for _, spec := range specs {
		_ = m.bootout(ctx, spec)
		if err := os.Remove(m.PlistPath(spec)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s plist: %w", spec.Name, err)
		}
	}
	return nil
}

func (m Manager) Status(ctx context.Context, spec Spec) Status {
	path := m.PlistPath(spec)
	status := Status{Spec: spec, Path: path}
	if _, err := os.Stat(path); err == nil {
		status.Exists = true
	} else if !os.IsNotExist(err) {
		status.Err = err
	}
	output, err := m.Runner(ctx, "launchctl", "print", m.ServiceTarget(spec))
	status.Output = string(output)
	if err == nil {
		status.Loaded = true
	} else if status.Err == nil {
		status.Err = err
	}
	return status
}

func (m Manager) PlistPath(spec Spec) string {
	return filepath.Join(m.Home, launchAgentsSubdir, spec.Label+".plist")
}

// Path implements service.Manager: the plist backing a spec.
func (m Manager) Path(spec Spec) string { return m.PlistPath(spec) }

func (m Manager) Domain() string {
	return "gui/" + strconv.Itoa(m.UID)
}

func (m Manager) ServiceTarget(spec Spec) string {
	return m.Domain() + "/" + spec.Label
}

func (m Manager) Write(spec Spec) error {
	if len(spec.ProgramArguments) == 0 || spec.ProgramArguments[0] == "" {
		return fmt.Errorf("%s service has no executable", spec.Name)
	}
	path := m.PlistPath(spec)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.StandardOutPath), 0o755); err != nil {
		return fmt.Errorf("create service log directory: %w", err)
	}
	data := []byte(RenderPlist(spec))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s plist: %w", spec.Name, err)
	}
	return nil
}

func (m Manager) bootstrap(ctx context.Context, spec Spec) error {
	output, err := m.Runner(ctx, "launchctl", "bootstrap", m.Domain(), m.PlistPath(spec))
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w%s", spec.Name, err, commandOutput(output))
	}
	return nil
}

func (m Manager) bootout(ctx context.Context, spec Spec) error {
	output, err := m.Runner(ctx, "launchctl", "bootout", m.Domain(), m.PlistPath(spec))
	if err != nil {
		return fmt.Errorf("stop %s: %w%s", spec.Name, err, commandOutput(output))
	}
	return nil
}

func (m Manager) enable(ctx context.Context, spec Spec) error {
	output, err := m.Runner(ctx, "launchctl", "enable", m.ServiceTarget(spec))
	if err != nil {
		return fmt.Errorf("enable %s: %w%s", spec.Name, err, commandOutput(output))
	}
	return nil
}

func (m Manager) kickstart(ctx context.Context, spec Spec) error {
	output, err := m.Runner(ctx, "launchctl", "kickstart", "-k", m.ServiceTarget(spec))
	if err != nil {
		return fmt.Errorf("start %s: %w%s", spec.Name, err, commandOutput(output))
	}
	return nil
}

func RenderPlist(spec Spec) string {
	path := spec.EnvironmentPath
	if path == "" {
		path = DefaultPath
	}
	var out bytes.Buffer
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>`)
	out.WriteString(escape(spec.Label))
	out.WriteString(`</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, arg := range spec.ProgramArguments {
		out.WriteString("    <string>")
		out.WriteString(escape(arg))
		out.WriteString("</string>\n")
	}
	out.WriteString(`  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>WorkingDirectory</key>
  <string>`)
	out.WriteString(escape(spec.WorkingDirectory))
	out.WriteString(`</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>`)
	out.WriteString(escape(path))
	out.WriteString(`</string>
  </dict>
  <key>StandardOutPath</key>
  <string>`)
	out.WriteString(escape(spec.StandardOutPath))
	out.WriteString(`</string>
  <key>StandardErrorPath</key>
  <string>`)
	out.WriteString(escape(spec.StandardErrorPath))
	out.WriteString(`</string>
  <key>ThrottleInterval</key>
  <integer>5</integer>
</dict>
</plist>
`)
	return out.String()
}

func escape(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}

func commandOutput(output []byte) string {
	if len(output) == 0 {
		return ""
	}
	return ": " + string(bytes.TrimSpace(output))
}
