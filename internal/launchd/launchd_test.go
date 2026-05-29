package launchd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPlistEscapesArguments(t *testing.T) {
	spec := VtunnelDaemonSpec("/opt/homebrew/bin/vtunnel", "/tmp/a&b/config.yml", "/tmp/logs", "/Users/example")
	plist := RenderPlist(spec)

	for _, want := range []string{
		"<string>sh.vltn.vtunnel.daemon</string>",
		"<string>/opt/homebrew/bin/vtunnel</string>",
		"<string>daemon</string>",
		"<string>--config</string>",
		"<string>/tmp/a&amp;b/config.yml</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist = %q, want %q", plist, want)
		}
	}
}

func TestInstallWritesPlistsAndStartsServices(t *testing.T) {
	tempDir := t.TempDir()
	var commands []string
	manager := NewForTest(tempDir, 501, func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	})
	specs := []Spec{
		VtunnelDaemonSpec("/bin/vtunnel", "/tmp/config.yml", filepath.Join(tempDir, "logs"), tempDir),
		CloudflaredSpec("/bin/cloudflared", "/tmp/cloudflared.yml", filepath.Join(tempDir, "logs"), tempDir),
	}

	if err := manager.Install(context.Background(), specs, true); err != nil {
		t.Fatal(err)
	}

	for _, spec := range specs {
		if _, err := os.Stat(manager.PlistPath(spec)); err != nil {
			t.Fatalf("expected plist for %s: %v", spec.Name, err)
		}
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{
		"launchctl bootout gui/501",
		"launchctl bootstrap gui/501",
		"launchctl enable gui/501/sh.vltn.vtunnel.daemon",
		"launchctl kickstart -k gui/501/sh.vltn.vtunnel.daemon",
		"launchctl enable gui/501/sh.vltn.vtunnel.cloudflared",
		"launchctl kickstart -k gui/501/sh.vltn.vtunnel.cloudflared",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("commands = %q, want %q", joined, want)
		}
	}
}

func TestUninstallRemovesPlists(t *testing.T) {
	tempDir := t.TempDir()
	manager := NewForTest(tempDir, 501, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, nil
	})
	spec := VtunnelDaemonSpec("/bin/vtunnel", "/tmp/config.yml", filepath.Join(tempDir, "logs"), tempDir)
	if err := manager.Write(spec); err != nil {
		t.Fatal(err)
	}

	if err := manager.Uninstall(context.Background(), []Spec{spec}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.PlistPath(spec)); !os.IsNotExist(err) {
		t.Fatalf("plist should be removed, stat err = %v", err)
	}
}
