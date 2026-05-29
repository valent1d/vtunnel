package onboarding

import (
	"fmt"
	"io"
	"strings"
)

type SetupTextOptions struct {
	ConfigPath    string
	ConfigCreated bool
	Notices       []string
}

func PrintSetupText(out io.Writer, report Report, options SetupTextOptions) {
	fmt.Fprintln(out, "vtunnel setup")
	if report.Ready {
		fmt.Fprintln(out, "Setup status: ready")
	} else {
		fmt.Fprintln(out, "Setup status: needs attention")
	}
	fmt.Fprintln(out)

	if len(options.Notices) > 0 {
		fmt.Fprintln(out, "Applied:")
		for _, notice := range options.Notices {
			if strings.TrimSpace(notice) == "" {
				continue
			}
			fmt.Fprintf(out, "  [OK] %s\n", notice)
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, "Checks:")
	if options.ConfigPath != "" {
		if options.ConfigCreated {
			fmt.Fprintf(out, "  [OK] vtunnel config created: %s\n", options.ConfigPath)
		} else {
			fmt.Fprintf(out, "  [OK] vtunnel config: %s\n", options.ConfigPath)
		}
	}
	for _, phase := range report.Phases {
		if phase.Title == "Welcome" || phase.Title == "Completion" {
			continue
		}
		for _, check := range phase.Checks {
			if options.ConfigPath != "" && check.Label == "vtunnel config" {
				continue
			}
			fmt.Fprintf(out, "  %s %s: %s\n", setupMarker(check.Status), check.Label, check.Detail)
			if check.Label == "ingress plan" && check.Status == StatusAction {
				fmt.Fprintln(out, "  [ACTION] cloudflared config needs changes; run: vtunnel setup --write-cloudflared")
			}
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Next:")
	if report.Ready {
		fmt.Fprintln(out, "  1. Run: vtunnel http 3000 dev")
		return
	}
	if len(report.Actions) == 0 {
		fmt.Fprintln(out, "  1. Re-run: vtunnel setup")
		return
	}
	action := report.Actions[0]
	if printSetupNext(out, action) {
		return
	}
	if action.Command != "" {
		fmt.Fprintf(out, "  1. %s: %s\n", action.Label, action.Command)
		return
	}
	if action.InputPrompt != "" {
		fmt.Fprintf(out, "  1. %s in onboarding: vtunnel onboarding\n", action.Label)
		return
	}
	fmt.Fprintf(out, "  1. %s: vtunnel setup --%s\n", action.Label, setupFlagForAction(action.ID))
}

func printSetupNext(out io.Writer, action Action) bool {
	switch action.ID {
	case "fix-dns":
		fmt.Fprintln(out, "  1. Fix Cloudflare wildcard DNS: go run ./cmd/vtunnel setup --fix-dns")
		fmt.Fprintln(out, "     Once installed: vtunnel setup --fix-dns")
		return true
	case "start-cloudflared":
		fmt.Fprintln(out, "  1. Start cloudflared with this config: go run ./cmd/vtunnel setup --start-cloudflared")
		fmt.Fprintln(out, "     Once installed: vtunnel setup --start-cloudflared")
		return true
	case "write-cloudflared":
		fmt.Fprintln(out, "  1. Update local cloudflared config: go run ./cmd/vtunnel setup --write-cloudflared")
		fmt.Fprintln(out, "     Once installed: vtunnel setup --write-cloudflared")
		return true
	}
	return false
}

func setupMarker(status Status) string {
	switch status {
	case StatusOK:
		return "[OK]"
	case StatusWarn:
		return "[WARN]"
	default:
		return "[ACTION]"
	}
}

func setupFlagForAction(actionID string) string {
	switch actionID {
	case "write-cloudflared":
		return "write-cloudflared"
	case "fix-tunnel":
		return "fix-tunnel"
	case "fix-dns":
		return "fix-dns"
	case "start-cloudflared":
		return "start-cloudflared"
	default:
		return actionID
	}
}
