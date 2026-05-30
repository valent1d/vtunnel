package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	cf "vtunnel/internal/cloudflared"
	"vtunnel/internal/config"
	"vtunnel/internal/launchd"
)

func newTCPCommand(configPath *string) *cobra.Command {
	var domain string

	cmd := &cobra.Command{
		Use:   "tcp <target> <subdomain>",
		Short: "Expose a local or remote TCP service (database, SSH, …) over the tunnel",
		Long: "Expose a TCP service (Postgres, MySQL, SSH, …) through Cloudflare Tunnel.\n\n" +
			"Unlike HTTP, TCP is NOT zero-install: there is no public db.<domain>:port to\n" +
			"dial on the free plan. The machine that connects must run cloudflared via\n" +
			"`vtunnel tcp connect <subdomain>`. Use this to reach your own service from\n" +
			"another machine, or for a teammate who can install cloudflared — not for\n" +
			"anonymous browser access.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			target, err := normalizeTCPTarget(args[0])
			if err != nil {
				return err
			}
			hostname, err := hostnameForRoute(args[1], domain, cfg)
			if err != nil {
				return err
			}
			return applyTCPExpose(cmd, cfg, *configPath, hostname, target)
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain to use for this tunnel")
	cmd.AddCommand(newTCPListCommand(configPath), newTCPRemoveCommand(configPath), newTCPConnectCommand(configPath))
	return cmd
}

func newTCPListCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List TCP tunnels",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			rules, err := cf.TCPIngress(cloudflaredConfigPathFor(cfg))
			if err != nil {
				return err
			}
			if len(rules) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No TCP tunnels.")
				return nil
			}
			for _, rule := range rules {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-30s %s\n", rule.Hostname, rule.Service)
			}
			return nil
		},
	}
}

func newTCPRemoveCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <subdomain>",
		Aliases: []string{"remove"},
		Short:   "Remove a TCP tunnel",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			hostname, err := hostnameForStop(args[0], cfg)
			if err != nil {
				return err
			}
			plan, err := cf.PlanRemoveIngress(cloudflaredConfigPathFor(cfg), hostname)
			if err != nil {
				return err
			}
			if len(plan.Changes) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No TCP tunnel for %s.\n", hostname)
				return nil
			}
			if _, err := cf.WritePlan(plan, time.Now()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "↻ restarting cloudflared (all tunnels reconnect briefly)…")
			if err := reloadCloudflared(cmd.Context(), cfg, *configPath); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "⚠ could not restart cloudflared automatically (%v); restart it to apply.\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ removed TCP tunnel %s\n", hostname)
			return nil
		},
	}
}

func newTCPConnectCommand(configPath *string) *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "connect <subdomain>",
		Short: "Open a local port that tunnels to a TCP service (client side)",
		Long: "Run on the machine that wants to CONNECT (needs cloudflared installed).\n" +
			"It opens a local port that tunnels to the remote TCP service via\n" +
			"`cloudflared access tcp` — keep it running and point your client\n" +
			"(psql, mysql, ssh, …) at 127.0.0.1:<port>.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			hostname, err := hostnameForStop(args[0], cfg)
			if err != nil {
				return err
			}
			if port == 0 {
				port = portFromService(cf.IngressService(cloudflaredConfigPathFor(cfg), hostname))
			}
			if port == 0 {
				return errors.New("could not determine a local port; pass --port")
			}
			cloudflaredPath, err := exec.LookPath("cloudflared")
			if err != nil {
				return fmt.Errorf("cloudflared not found: %w", err)
			}
			url := fmt.Sprintf("127.0.0.1:%d", port)
			fmt.Fprintf(cmd.OutOrStdout(), "Connecting %s → %s  (Ctrl+C to stop)\n", hostname, url)
			access := exec.CommandContext(cmd.Context(), cloudflaredPath, "access", "tcp", "--hostname", hostname, "--url", url)
			access.Stdout = cmd.OutOrStdout()
			access.Stderr = cmd.ErrOrStderr()
			access.Stdin = cmd.InOrStdin()
			return access.Run()
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "local port to listen on (defaults to the service's port)")
	return cmd
}

// applyTCPExpose writes a tcp:// ingress rule for hostname → target and restarts
// cloudflared so it takes effect.
func applyTCPExpose(cmd *cobra.Command, cfg config.Config, configPath, hostname, target string) error {
	out := cmd.OutOrStdout()
	plan, err := cf.PlanAddIngress(cloudflaredConfigPathFor(cfg), hostname, "tcp://"+target)
	if err != nil {
		return err
	}
	if _, err := cf.WritePlan(plan, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(out, "+ ingress %s → tcp://%s\n", hostname, target)
	fmt.Fprintln(out, "↻ restarting cloudflared (all tunnels reconnect briefly)…")
	if err := reloadCloudflared(cmd.Context(), cfg, configPath); err != nil {
		fmt.Fprintf(out, "⚠ could not restart cloudflared automatically (%v); restart it to apply.\n", err)
	}
	fmt.Fprintf(out, "✓ %s exposed over TCP\n", hostname)
	fmt.Fprintln(out, "  Not browser-reachable — the connecting machine needs cloudflared:")
	fmt.Fprintf(out, "    vtunnel tcp connect %s\n", hostname)
	return nil
}

// reloadCloudflared restarts cloudflared so config changes take effect:
// kickstart the LaunchAgent if installed, else restart the foreground process.
func reloadCloudflared(ctx context.Context, cfg config.Config, configPath string) error {
	cfPath := cloudflaredConfigPathFor(cfg)
	if runtime.GOOS == "darwin" {
		if manager, specs, err := serviceRuntimeContext(configPath); err == nil {
			for _, spec := range specs {
				if spec.Label == launchd.CloudflaredLabel && manager.Status(ctx, spec).Exists {
					return manager.Start(ctx, []launchd.Spec{spec})
				}
			}
		}
	}
	cloudflaredPath, err := exec.LookPath("cloudflared")
	if err != nil {
		return fmt.Errorf("cloudflared not found: %w", err)
	}
	inspection := inspectCloudflaredProcessForCLI(ctx, cloudflaredPath, cfPath, "")
	for _, process := range inspection.Matches {
		_ = syscall.Kill(process.PID, syscall.SIGTERM)
	}
	if len(inspection.Matches) > 0 {
		time.Sleep(700 * time.Millisecond)
	}
	_, err = startCloudflaredForCLI(ctx, cloudflaredPath, cfPath)
	return err
}

func cloudflaredConfigPathFor(cfg config.Config) string {
	return strings.TrimSpace(config.ExpandPath(cfg.Cloudflared.ConfigPath))
}

// normalizeTCPTarget turns "3306" into 127.0.0.1:3306, accepts host:port, and
// strips a leading tcp:// scheme.
func normalizeTCPTarget(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	arg = strings.TrimPrefix(arg, "tcp://")
	if arg == "" {
		return "", errors.New("target is required")
	}
	if _, err := strconv.Atoi(arg); err == nil {
		return "127.0.0.1:" + arg, nil
	}
	if !strings.Contains(arg, ":") {
		return "", fmt.Errorf("target %q must be a port or host:port", arg)
	}
	return arg, nil
}

// portFromService extracts the port from a "tcp://host:port" service string.
func portFromService(service string) int {
	service = strings.TrimPrefix(strings.TrimSpace(service), "tcp://")
	if i := strings.LastIndex(service, ":"); i >= 0 {
		if port, err := strconv.Atoi(service[i+1:]); err == nil {
			return port
		}
	}
	return 0
}
