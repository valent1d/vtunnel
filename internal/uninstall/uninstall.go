// Package uninstall tears down what vtunnel installs: macOS LaunchAgents, the
// local config directory, the Keychain API token, and — only when explicitly
// requested — the Cloudflare tunnel and the wildcard DNS records it created.
//
// The engine is a pure orchestrator: it walks a Plan in a safe order and runs
// injected Actions, tolerating already-removed resources and collecting a
// per-step Report instead of aborting on the first error. Inventory
// (BuildPlan) and the real side effects live in the CLI, which keeps this
// package dependency-free and unit-testable with fakes.
package uninstall

import (
	"context"
	"fmt"
)

// Options controls the blast radius of an uninstall.
type Options struct {
	// IncludeCloudflare also deletes the Cloudflare tunnel and the wildcard DNS
	// records vtunnel created. Off by default: those live on the user's
	// Cloudflare account and unrelated things may depend on them.
	IncludeCloudflare bool
	// KeepConfig preserves ~/.config/vtunnel and the Keychain API token so a
	// later reinstall keeps the user's settings. LaunchAgents are still removed.
	KeepConfig bool
}

// Service is a macOS LaunchAgent vtunnel installed.
type Service struct {
	Name      string
	Label     string
	PlistPath string
	Installed bool
	Loaded    bool
}

// LocalPath is a file or directory on disk that vtunnel owns.
type LocalPath struct {
	Label   string
	Path    string
	Present bool
}

// DNSRecord is a wildcard CNAME vtunnel created, pinned to its zone + record id
// so deletion targets exactly that record and nothing else in the zone.
type DNSRecord struct {
	ZoneID   string
	RecordID string
	Hostname string
}

// Cloudflare groups the account-side resources vtunnel created.
type Cloudflare struct {
	// Resolved is true when the tunnel/DNS could be inspected (token present,
	// tunnel found). When false, Reason explains why nothing will be deleted.
	Resolved   bool
	Reason     string
	TunnelRef  string // id or name passed to `cloudflared tunnel delete`
	TunnelName string
	Records    []DNSRecord
	Creds      LocalPath
	// AccessApps are Cloudflare Access applications vtunnel created to protect
	// routes. They are independent of the tunnel, so they are removed whenever
	// Cloudflare cleanup is requested, even if the tunnel could not be resolved.
	AccessApps []AccessApp
}

// AccessApp is a Cloudflare Access application to delete.
type AccessApp struct {
	Hostname string
	AppID    string
}

// Label returns a human-friendly name for the tunnel.
func (c Cloudflare) Label() string {
	if c.TunnelName != "" {
		return c.TunnelName
	}
	return c.TunnelRef
}

// Plan is the full inventory plus the chosen options.
type Plan struct {
	Options       Options
	DaemonRunning bool
	Services      []Service
	Local         []LocalPath
	Token         bool
	Cloudflare    Cloudflare
}

// InstalledServices returns the subset of services that are present on disk.
func (p Plan) InstalledServices() []Service {
	installed := make([]Service, 0, len(p.Services))
	for _, svc := range p.Services {
		if svc.Installed {
			installed = append(installed, svc)
		}
	}
	return installed
}

// Actions are the side effects Execute performs. Each is injected so the engine
// is unit-testable with fakes. Optional actions may be nil; the matching step
// is then skipped.
type Actions struct {
	StopDaemon     func(context.Context) error
	RemoveServices func(context.Context) error
	DeleteDNS       func(context.Context, DNSRecord) error
	DeleteTunnel    func(context.Context, string) error
	DeleteAccessApp func(context.Context, string) error
	RemovePath      func(string) error
	DeleteToken     func() error
}

// Outcome is the result of a single teardown step.
type Outcome int

const (
	// Removed means the resource was deleted (or was already gone).
	Removed Outcome = iota
	// Failed means the deletion returned an error; the run continues regardless.
	Failed
)

// Step records the result of one teardown action.
type Step struct {
	Label   string
	Outcome Outcome
	Err     error
}

// Report is the ordered list of steps Execute performed.
type Report struct {
	Steps []Step
}

// Failed reports whether any step failed.
func (r Report) Failed() bool {
	for _, step := range r.Steps {
		if step.Outcome == Failed {
			return true
		}
	}
	return false
}

// Execute runs the teardown in a safe order:
//  1. stop the daemon (so the tunnel has no live connections and LaunchAgents
//     are not respawned mid-teardown);
//  2. remove the LaunchAgents;
//  3. delete Cloudflare DNS then the tunnel then its credentials (opt-in);
//  4. remove the local config dir and the Keychain token (unless kept).
//
// The Keychain token is removed last because the Cloudflare steps need it.
func Execute(ctx context.Context, plan Plan, actions Actions) Report {
	var report Report

	if plan.DaemonRunning && actions.StopDaemon != nil {
		report.run("Stop vtunnel daemon", func() error { return actions.StopDaemon(ctx) })
	}

	if len(plan.InstalledServices()) > 0 && actions.RemoveServices != nil {
		label := fmt.Sprintf("Remove %d macOS LaunchAgent(s)", len(plan.InstalledServices()))
		report.run(label, func() error { return actions.RemoveServices(ctx) })
	}

	if plan.Options.IncludeCloudflare && plan.Cloudflare.Resolved {
		if actions.DeleteDNS != nil {
			for _, record := range plan.Cloudflare.Records {
				rec := record
				report.run("Delete DNS "+rec.Hostname, func() error { return actions.DeleteDNS(ctx, rec) })
			}
		}
		if plan.Cloudflare.TunnelRef != "" && actions.DeleteTunnel != nil {
			report.run("Delete Cloudflare tunnel "+plan.Cloudflare.Label(), func() error {
				return actions.DeleteTunnel(ctx, plan.Cloudflare.TunnelRef)
			})
		}
		if plan.Cloudflare.Creds.Present && actions.RemovePath != nil {
			creds := plan.Cloudflare.Creds
			report.run("Remove "+creds.Label, func() error { return actions.RemovePath(creds.Path) })
		}
	}

	// Access apps are independent of the tunnel, so they are removed whenever
	// Cloudflare cleanup is requested (not gated on tunnel resolution).
	if plan.Options.IncludeCloudflare && actions.DeleteAccessApp != nil {
		for _, app := range plan.Cloudflare.AccessApps {
			accessApp := app
			report.run("Delete Access app "+accessApp.Hostname, func() error {
				return actions.DeleteAccessApp(ctx, accessApp.AppID)
			})
		}
	}

	if !plan.Options.KeepConfig {
		if actions.RemovePath != nil {
			for _, path := range plan.Local {
				if !path.Present {
					continue
				}
				p := path
				report.run("Remove "+p.Label, func() error { return actions.RemovePath(p.Path) })
			}
		}
		if plan.Token && actions.DeleteToken != nil {
			report.run("Remove Keychain API token", func() error { return actions.DeleteToken() })
		}
	}

	return report
}

func (r *Report) run(label string, fn func() error) {
	if err := fn(); err != nil {
		r.Steps = append(r.Steps, Step{Label: label, Outcome: Failed, Err: err})
		return
	}
	r.Steps = append(r.Steps, Step{Label: label, Outcome: Removed})
}
