package systemd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vtunnel/internal/service"
)

type fakeRunner struct {
	calls  [][]string
	active bool
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	for _, a := range args {
		if a == "is-active" {
			if f.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), nil
		}
	}
	return nil, nil
}

func (f *fakeRunner) ran(sub string) bool {
	for _, call := range f.calls {
		if strings.Contains(strings.Join(call, " "), sub) {
			return true
		}
	}
	return false
}

func daemonSpec(home string) service.Spec {
	return service.DaemonSpec("/usr/local/bin/vtunnel", "/home/u/.config/vtunnel/config.yml", filepath.Join(home, "logs"), home)
}

func TestRenderUnit(t *testing.T) {
	unit := RenderUnit(daemonSpec("/home/u"))
	for _, want := range []string{
		"[Unit]",
		"Description=vtunnel daemon",
		"[Service]",
		"ExecStart=/usr/local/bin/vtunnel daemon --config /home/u/.config/vtunnel/config.yml",
		"StandardOutput=append:/home/u/logs/daemon.launchd.out.log",
		"Restart=always",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestInstallWritesUnitAndStarts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	fake := &fakeRunner{}
	m := NewForTest(home, fake.run)

	spec := daemonSpec(home)
	if err := m.Install(context.Background(), []service.Spec{spec}, true); err != nil {
		t.Fatalf("install: %v", err)
	}

	unitPath := filepath.Join(home, ".config", "systemd", "user", "sh.vltn.vtunnel.daemon.service")
	if _, err := os.Stat(unitPath); err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	if !fake.ran("daemon-reload") {
		t.Fatal("expected daemon-reload")
	}
	if !fake.ran("enable --now sh.vltn.vtunnel.daemon.service") {
		t.Fatalf("expected enable --now; calls: %v", fake.calls)
	}
}

func TestStatusReflectsActive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	fake := &fakeRunner{active: true}
	m := NewForTest(home, fake.run)
	spec := daemonSpec(home)
	if err := m.Write(spec); err != nil {
		t.Fatal(err)
	}
	st := m.Status(context.Background(), spec)
	if !st.Exists {
		t.Fatal("Exists should be true after Write")
	}
	if !st.Loaded {
		t.Fatal("Loaded should be true when is-active reports active")
	}
}

func TestUninstallRemovesUnit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	fake := &fakeRunner{}
	m := NewForTest(home, fake.run)
	spec := daemonSpec(home)
	if err := m.Write(spec); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(context.Background(), []service.Spec{spec}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(m.Path(spec)); !os.IsNotExist(err) {
		t.Fatalf("unit file should be gone, stat err = %v", err)
	}
	if !fake.ran("disable --now") {
		t.Fatal("expected disable --now")
	}
}
