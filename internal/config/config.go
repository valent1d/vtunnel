package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const AppName = "vtunnel"

type Config struct {
	DefaultDomain string            `yaml:"default_domain"`
	Domains       []string          `yaml:"domains"`
	Proxy         ListenConfig      `yaml:"proxy"`
	API           ListenConfig      `yaml:"api"`
	Cloudflared   CloudflaredConfig `yaml:"cloudflared"`
	Prefs         Preferences       `yaml:"prefs,omitempty"`
}

// Preferences are persisted UX choices the user makes interactively, such as
// "don't ask me this again" toggles.
type Preferences struct {
	// SuppressSSHSuggestion silences the "use vtunnel ssh instead?" prompt that
	// appears when exposing an SSH port (22) over a plain TCP tunnel.
	SuppressSSHSuggestion bool `yaml:"suppress_ssh_suggestion,omitempty"`
}

type ListenConfig struct {
	Listen string `yaml:"listen"`
}

type CloudflaredConfig struct {
	TunnelName string `yaml:"tunnel_name"`
	ConfigPath string `yaml:"config_path"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Proxy: ListenConfig{Listen: "127.0.0.1:8787"},
		API:   ListenConfig{Listen: "127.0.0.1:8788"},
		Cloudflared: CloudflaredConfig{
			ConfigPath: filepath.Join(home, ".cloudflared", "config.yml"),
		},
	}
}

func ConfigDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, AppName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", AppName), nil
}

func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yml"), nil
}

func RoutesPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "routes.json"), nil
}

func LogsDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

func RequestLogsPath() (string, error) {
	logs, err := LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(logs, "requests.jsonl"), nil
}

func CloudflaredLogPath() (string, error) {
	logs, err := LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(logs, "cloudflared.log"), nil
}

// CloudflaredServiceLogPath is where the macOS LaunchAgent captures cloudflared's
// output. The basename must match launchd.CloudflaredSpec's StandardOutPath.
func CloudflaredServiceLogPath() (string, error) {
	logs, err := LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(logs, "cloudflared.launchd.out.log"), nil
}

func Load(path string) (Config, error) {
	if path == "" {
		var err error
		path, err = ConfigPath()
		if err != nil {
			return Config{}, err
		}
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if path == "" {
		var err error
		path, err = ConfigPath()
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

func EnsureDirs() error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	logs, err := LogsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return fmt.Errorf("create logs directory: %w", err)
	}
	return nil
}

func ExpandPath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}
