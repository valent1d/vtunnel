package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"vtunnel/internal/api"
	"vtunnel/internal/config"
	"vtunnel/internal/orbstack"
	"vtunnel/internal/routes"
)

// orbstackRunner lets tests inject a fake docker; nil shells out to docker.
var orbstackRunner orbstack.Runner

func newOrbstackClient() orbstack.Client {
	return orbstack.New(orbstackRunner)
}

func newOrbstackCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orbstack",
		Short: "Discover and expose OrbStack containers",
		Long: "Expose OrbStack Docker containers through vtunnel. With no subcommand,\n" +
			"opens an interactive picker. Each container is reachable at its\n" +
			"<name>.orb.local domain, which vtunnel forwards to.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runOrbstackPicker(cmd, configPath)
		},
	}
	cmd.AddCommand(
		newOrbstackListCommand(configPath),
		newOrbstackExposeCommand(configPath),
	)
	return cmd
}

func newOrbstackListCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List running OrbStack containers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			containers, err := newOrbstackClient().List(cmd.Context())
			if err != nil {
				return err
			}
			if len(containers) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No running OrbStack containers found.")
				return nil
			}
			cfg, _ := config.Load(*configPath)
			printOrbstackContainers(cmd.OutOrStdout(), containers, exposedTargets(cmd.Context(), cfg))
			return nil
		},
	}
}

func newOrbstackExposeCommand(configPath *string) *cobra.Command {
	var domain string
	var target string
	var detach bool

	cmd := &cobra.Command{
		Use:   "expose <container> [subdomain]",
		Short: "Expose an OrbStack container through vtunnel",
		Long: "Create a route from <subdomain>.<domain> to the container's\n" +
			"<name>.orb.local domain, then open the HTTP dashboard focused on it.\n" +
			"The subdomain defaults to the container's custom domain label, or its\n" +
			"name. Pass a second argument to choose your own (e.g. app1).",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			containers, err := newOrbstackClient().List(cmd.Context())
			if err != nil {
				return err
			}
			container, ok := orbstack.FindContainer(containers, args[0])
			if !ok {
				return fmt.Errorf("OrbStack container %q not found (try `vtunnel orbstack list`)", args[0])
			}
			subdomain := ""
			if len(args) == 2 {
				subdomain = args[1]
			}
			return exposeOrbstackContainer(cmd, configPath, container, subdomain, domain, target, detach)
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain to use for this route")
	cmd.Flags().StringVar(&target, "target", "", "override the upstream URL (defaults to http://<name>.orb.local)")
	cmd.Flags().BoolVar(&detach, "detach", false, "create the route and return instead of opening the dashboard")
	return cmd
}

// exposeOrbstackContainer creates the route for a container and (unless detach)
// opens the HTTP dashboard focused on it. Shared by the expose command and the
// interactive picker.
func exposeOrbstackContainer(cmd *cobra.Command, configPath *string, container orbstack.Container, subdomain, domainFlag, targetOverride string, detach bool) error {
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := config.EnsureDirs(); err != nil {
		return err
	}

	if !container.HTTP && strings.TrimSpace(targetOverride) == "" {
		return fmt.Errorf("%s does not expose an HTTP port; pass --target to forward anyway", container.Name)
	}

	routeTarget := container.Target()
	if override := strings.TrimSpace(targetOverride); override != "" {
		routeTarget, err = normalizeTargetURL(override)
		if err != nil {
			return err
		}
	}

	if strings.TrimSpace(subdomain) == "" {
		subdomain = container.DefaultSubdomain()
	}
	hostname, err := hostnameForRoute(subdomain, domainFlag, cfg)
	if err != nil {
		return err
	}

	runtimeCfg := configForRouteDomain(cfg, domainFlag)
	engine := newCLIOnboardingEngine(*configPath, runtimeCfg)
	if result, err := engine.EnsureHTTPRuntime(cmd.Context()); err != nil {
		return err
	} else if result.Cloudflared != nil && !result.Cloudflared.Skipped {
		fmt.Fprintf(cmd.OutOrStdout(), "cloudflared: started (pid %d)\n", result.Cloudflared.PID)
	}

	route := routes.Route{
		Hostname: hostname,
		Target:   routeTarget,
		Orbstack: &routes.OrbstackInfo{
			Container:     container.Name,
			Image:         container.Image,
			OrbDomain:     container.OrbDomain,
			CustomDomains: container.CustomDomains,
		},
	}
	if err := api.New(cfg).AddRoute(cmd.Context(), route); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Forwarding https://%s -> %s (OrbStack: %s)\n", hostname, routeTarget, container.Name)
	if detach {
		return nil
	}
	return runHTTPUI(cmd.Context(), cfg, hostname)
}

// exposedTargets maps an OrbStack domain to the public hostname currently
// forwarding to it, so `list` can show what is already exposed.
func exposedTargets(ctx context.Context, cfg config.Config) map[string]string {
	list, err := api.New(cfg).ListRoutes(ctx)
	if err != nil {
		list, _ = readSavedRoutes()
	}
	out := make(map[string]string, len(list))
	for _, route := range list {
		if parsed, perr := url.Parse(route.Target); perr == nil {
			out[strings.ToLower(parsed.Hostname())] = route.Hostname
		}
	}
	return out
}

func printOrbstackContainers(out io.Writer, containers []orbstack.Container, exposed map[string]string) {
	const (
		nameHeader   = "CONTAINER"
		domainHeader = "ORBSTACK DOMAIN"
	)
	nameWidth, domainWidth := len(nameHeader), len(domainHeader)
	for _, container := range containers {
		if len(container.Name) > nameWidth {
			nameWidth = len(container.Name)
		}
		if len(container.OrbDomain) > domainWidth {
			domainWidth = len(container.OrbDomain)
		}
	}

	fmt.Fprintf(out, "%-*s  %-*s  %-4s  %s\n", nameWidth, nameHeader, domainWidth, domainHeader, "HTTP", "EXPOSED AS")
	for _, container := range containers {
		httpMark := "no"
		if container.HTTP {
			httpMark = "yes"
		}
		exposedAs := "-"
		if hostname, ok := exposed[strings.ToLower(container.OrbDomain)]; ok {
			exposedAs = hostname
		}
		fmt.Fprintf(out, "%-*s  %-*s  %-4s  %s\n", nameWidth, container.Name, domainWidth, container.OrbDomain, httpMark, exposedAs)
	}
}
