package onboarding

import (
	"context"
	"fmt"
)

type HTTPRuntimeResult struct {
	Cloudflared   *StartResult
	DaemonStarted bool
}

func (engine *Engine) EnsureHTTPRuntime(ctx context.Context) (HTTPRuntimeResult, error) {
	s := engine.snapshot(ctx)
	if err := validateHTTPRuntime(s); err != nil {
		return HTTPRuntimeResult{}, err
	}
	if shouldBlockHTTPForDNS(s.dns) {
		return HTTPRuntimeResult{}, HTTPRuntimeError(
			"Cloudflare wildcard DNS is not pointing to this tunnel.",
			"Run: vtunnel setup --fix-dns",
		)
	}

	result := HTTPRuntimeResult{}
	if s.process.RunningForConfig() {
		result.Cloudflared = &StartResult{PID: s.process.Matches[0].PID, Skipped: true}
	} else {
		if s.process.Err != nil {
			return HTTPRuntimeResult{}, fmt.Errorf("cannot inspect cloudflared process: %w", s.process.Err)
		}
		started, err := startConfiguredCloudflared(ctx, s.cfg, s.cloudflared, s.process, s.diag, s.diagErr, s.plan, s.planErr, engine.deps.startCloudflared)
		if err != nil {
			if len(s.process.Processes) > 0 {
				return HTTPRuntimeResult{}, HTTPRuntimeError(
					"cloudflared is already running, but not with the vtunnel config.",
					"Stop the existing cloudflared process, then run: vtunnel setup --start-cloudflared",
				)
			}
			return HTTPRuntimeResult{}, err
		}
		result.Cloudflared = &started
	}

	if !s.daemonRunning {
		if err := engine.deps.startDaemon(ctx, s.cfg, s.configPath); err != nil {
			return HTTPRuntimeResult{}, err
		}
		result.DaemonStarted = true
	}
	return result, nil
}

func validateHTTPRuntime(s snapshot) error {
	switch {
	case s.cloudflared.Err != nil:
		return HTTPRuntimeError(
			"cloudflared is not installed.",
			"Run: brew install cloudflared",
		)
	case !s.cloudflared.CertExists:
		return HTTPRuntimeError(
			"cloudflared login is missing.",
			"Run: vtunnel cloudflared login",
		)
	case s.cloudflared.TunnelListErr != nil:
		return fmt.Errorf("cloudflared cannot list tunnels: %w", s.cloudflared.TunnelListErr)
	case s.diagErr != nil:
		return fmt.Errorf("cloudflared config is not readable: %w\nRun: vtunnel setup", s.diagErr)
	case !s.diag.Exists:
		return HTTPRuntimeError(
			"cloudflared config is not ready.",
			"Run: vtunnel setup --write-cloudflared",
		)
	case s.diag.Config.Tunnel == "" || s.diag.Config.CredentialsFile == "":
		return HTTPRuntimeError(
			"cloudflared config is missing tunnel or credentials-file.",
			"Run: vtunnel setup --fix-tunnel",
		)
	case !configuredTunnelExists(s.diag, s.cloudflared):
		return HTTPRuntimeError(
			"cloudflared config points to a tunnel that no longer exists.",
			"Run: vtunnel setup --fix-tunnel",
		)
	case s.planErr != nil:
		return fmt.Errorf("cloudflared config cannot be checked: %w\nRun: vtunnel setup", s.planErr)
	case len(s.plan.Changes) > 0:
		return HTTPRuntimeError(
			"cloudflared config is not ready for this route.",
			"Run: vtunnel setup --write-cloudflared",
		)
	}

	for _, result := range s.diag.DomainResults {
		if !result.Found || !result.ServiceOK {
			return HTTPRuntimeError(
				"cloudflared wildcard ingress is not ready for "+result.Domain+".",
				"Run: vtunnel setup --write-cloudflared",
			)
		}
	}
	return nil
}

func shouldBlockHTTPForDNS(dns DNSInspection) bool {
	if dns.TokenMissing || dns.Err != nil || len(dns.Results) == 0 {
		return false
	}
	return !dns.Ready()
}

func HTTPRuntimeError(message string, action string) error {
	return fmt.Errorf("%s\n%s", message, action)
}
