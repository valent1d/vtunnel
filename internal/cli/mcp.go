package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"vtunnel/internal/api"
	cfapi "vtunnel/internal/cloudflare"
	"vtunnel/internal/config"
	"vtunnel/internal/requestlog"
	"vtunnel/internal/routes"
)

// newMCPCommand is the parent for vtunnel's Model Context Protocol surface:
// `serve` runs the stdio server that AI clients launch, while add/remove/status
// (and the bare interactive form) register the server into those clients and
// report their setup. Bare `vtunnel mcp` opens the management dashboard.
func newMCPCommand(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage the vtunnel MCP server for AI agents (Claude Code, Cursor, …)",
		Long: "vtunnel ships a Model Context Protocol server so AI clients can create and\n" +
			"inspect tunnels — e.g. spin up a public URL for a webhook and read the\n" +
			"requests that arrive.\n\n" +
			"Run with no arguments to open the management dashboard (add/remove the\n" +
			"server in Claude Code, Cursor, VS Code, Codex, OpenCode, Antigravity and\n" +
			"check that it works). `vtunnel mcp serve` is the server itself, launched\n" +
			"by the clients.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interactiveTerminal() {
				return runMCPSetupTUI(cmd.Context())
			}
			return runMCPStatus(cmd.Context(), cmd.OutOrStdout(), mcpStatusOptions{})
		},
	}
	cmd.AddCommand(
		newMCPServeCommand(configPath),
		newMCPAddCommand(),
		newMCPRemoveCommand(),
		newMCPStatusCommand(),
	)
	return cmd
}

// newMCPServeCommand runs vtunnel as an MCP server over stdio. MCP clients are
// configured to launch this; you normally don't run it by hand.
func newMCPServeCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server over stdio (launched by MCP clients)",
		Long: "Run vtunnel as a Model Context Protocol server on stdio.\n\n" +
			"Tools: list_tunnels, create_http_tunnel, stop_tunnel, protect_tunnel,\n" +
			"unprotect_tunnel, inspect_requests, replay_request. Resource:\n" +
			"vtunnel://requests (recent captured requests).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCPServer(cmd.Context(), *configPath)
		},
	}
}

func runMCPServer(ctx context.Context, configPath string) error {
	err := buildMCPServer(configPath).Run(ctx, &mcp.StdioTransport{})
	// A closed stdin/stdout pipe or a cancelled context is a normal client
	// disconnect for a stdio server, not a failure — exit 0 in those cases.
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	if msg := err.Error(); strings.Contains(msg, "is closing") || strings.Contains(msg, "EOF") || strings.Contains(msg, "closed pipe") {
		return nil
	}
	return err
}

// buildMCPServer wires the vtunnel tools and resources onto an MCP server. It is
// separate from runMCPServer so tests can drive it over an in-memory transport.
func buildMCPServer(configPath string) *mcp.Server {
	svc := mcpService{configPath: configPath}
	server := mcp.NewServer(&mcp.Implementation{Name: "vtunnel", Version: versionInfo()}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tunnels",
		Description: "List the active vtunnel tunnels: hostname, public URL, upstream target, and whether each is protected by a Cloudflare Access login.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listTunnelsOut, error) {
		out, err := svc.listTunnels(ctx)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "create_http_tunnel",
		Description: "Create a PUBLIC HTTPS tunnel to a local HTTP service and return its URL. " +
			"Ideal for receiving webhooks (Stripe, GitHub, …) on a local dev server. " +
			"Anyone with the URL can reach it; use inspect_requests to see what arrives.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createHTTPIn) (*mcp.CallToolResult, createHTTPOut, error) {
		out, err := svc.createHTTP(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "stop_tunnel",
		Description: "Remove a tunnel by hostname or subdomain. Also tears down its Cloudflare Access protection if it had any.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopIn) (*mcp.CallToolResult, stopOut, error) {
		out, err := svc.stopTunnel(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "protect_tunnel",
		Description: "Put a Cloudflare Access login in front of an existing tunnel (Zero Trust). " +
			"Use mode=otp (email one-time PIN, the default) with allow=[emails/@domains], or mode=sso with an idp. " +
			"Requires a Cloudflare token with Access write scope.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in protectIn) (*mcp.CallToolResult, protectOut, error) {
		out, err := svc.protectTunnel(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "unprotect_tunnel",
		Description: "Remove Cloudflare Access protection from a tunnel, making it public again (anyone with the URL can reach it).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in unprotectIn) (*mcp.CallToolResult, unprotectOut, error) {
		out, err := svc.unprotectTunnel(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "inspect_requests",
		Description: "Inspect HTTP requests captured by the tunnels. Without an id, returns recent request summaries (optionally filtered by hostname). " +
			"With an id, returns the full request and response (headers + body) — useful for debugging a webhook payload.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in inspectIn) (*mcp.CallToolResult, inspectOut, error) {
		out, err := svc.inspect(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "replay_request",
		Description: "Re-issue a captured request to its upstream by id (e.g. replay a webhook while you fix the handler). Requires the vtunnel daemon to be running.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in replayIn) (*mcp.CallToolResult, replayOut, error) {
		out, err := svc.replay(ctx, in)
		return nil, out, err
	})

	server.AddResource(&mcp.Resource{
		URI:         "vtunnel://requests",
		Name:        "Recent requests",
		Description: "The most recent HTTP requests captured across all tunnels (JSON).",
		MIMEType:    "application/json",
	}, svc.requestsResource)

	return server
}

// mcpService backs the MCP tools. It reloads config per call so newly added
// domains are picked up, and reuses the same code paths as the CLI.
type mcpService struct {
	configPath string
}

func (s mcpService) config() (config.Config, error) { return config.Load(s.configPath) }

// --- list_tunnels ---

type tunnelInfo struct {
	Hostname  string `json:"hostname"`
	URL       string `json:"url"`
	Target    string `json:"target"`
	Protected bool   `json:"protected"`
	Paused    bool   `json:"paused,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

type listTunnelsOut struct {
	Tunnels []tunnelInfo `json:"tunnels"`
	Source  string       `json:"source"`
}

func (s mcpService) listTunnels(ctx context.Context) (listTunnelsOut, error) {
	cfg, err := s.config()
	if err != nil {
		return listTunnelsOut{}, err
	}
	source := "daemon"
	list, err := api.New(cfg).ListRoutes(ctx)
	if err != nil {
		list, err = readSavedRoutes()
		if err != nil {
			return listTunnelsOut{}, fmt.Errorf("could not read routes: %w", err)
		}
		source = "saved file (daemon not running)"
	}
	out := listTunnelsOut{Source: source, Tunnels: make([]tunnelInfo, 0, len(list))}
	for _, route := range list {
		out.Tunnels = append(out.Tunnels, tunnelToInfo(route))
	}
	return out, nil
}

func tunnelToInfo(route routes.Route) tunnelInfo {
	info := tunnelInfo{
		Hostname:  route.Hostname,
		URL:       "https://" + route.Hostname,
		Target:    route.Target,
		Protected: route.Access != nil && !route.Access.Paused,
	}
	if route.Access != nil {
		info.Paused = route.Access.Paused
	}
	if !route.CreatedAt.IsZero() {
		info.CreatedAt = route.CreatedAt.Format(time.RFC3339)
	}
	return info
}

// --- create_http_tunnel ---

type createHTTPIn struct {
	Target    string `json:"target" jsonschema:"a local port like \"3000\", or a full URL / host:port to forward to"`
	Subdomain string `json:"subdomain,omitempty" jsonschema:"optional subdomain; a random one is generated if omitted"`
	Domain    string `json:"domain,omitempty" jsonschema:"optional domain to use; defaults to the configured default_domain"`
}

type createHTTPOut struct {
	Hostname string `json:"hostname"`
	URL      string `json:"url"`
	Target   string `json:"target"`
	Public   bool   `json:"public"`
	Note     string `json:"note"`
}

func (s mcpService) createHTTP(ctx context.Context, in createHTTPIn) (createHTTPOut, error) {
	cfg, err := s.config()
	if err != nil {
		return createHTTPOut{}, err
	}
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return createHTTPOut{}, errors.New(`target is required (a local port like "3000", or a URL / host:port)`)
	}
	routeTarget, err := resolveHTTPTarget(target)
	if err != nil {
		return createHTTPOut{}, err
	}
	domain := strings.TrimSpace(in.Domain)
	hostname, err := hostnameForRoute(strings.TrimSpace(in.Subdomain), domain, cfg)
	if err != nil {
		return createHTTPOut{}, err
	}

	engine := newCLIOnboardingEngine(s.configPath, configForRouteDomain(cfg, domain))
	if _, err := engine.EnsureHTTPRuntime(ctx); err != nil {
		return createHTTPOut{}, fmt.Errorf("could not start the tunnel runtime (is vtunnel set up? run `vtunnel setup`): %w", err)
	}
	route := routes.Route{Hostname: hostname, Target: routeTarget}
	if err := api.New(cfg).AddRoute(ctx, route); err != nil {
		return createHTTPOut{}, err
	}
	return createHTTPOut{
		Hostname: hostname,
		URL:      "https://" + hostname,
		Target:   routeTarget,
		Public:   true,
		Note:     "PUBLIC — anyone with the URL can reach it. If this is not a webhook endpoint, protect it with `vtunnel access protect` or the dashboard.",
	}, nil
}

// --- stop_tunnel ---

type stopIn struct {
	Hostname string `json:"hostname" jsonschema:"the tunnel hostname or subdomain to remove"`
}

type stopOut struct {
	Removed  bool   `json:"removed"`
	Hostname string `json:"hostname"`
	Note     string `json:"note"`
}

func (s mcpService) stopTunnel(ctx context.Context, in stopIn) (stopOut, error) {
	cfg, err := s.config()
	if err != nil {
		return stopOut{}, err
	}
	hostname, err := hostnameForStop(strings.TrimSpace(in.Hostname), cfg)
	if err != nil {
		return stopOut{}, err
	}
	route, found := findRoute(ctx, cfg, hostname)

	removed := false
	if derr := api.New(cfg).DeleteRoute(ctx, hostname); derr == nil {
		removed = true
	} else if store, serr := savedRouteStore(); serr == nil {
		ok, fileErr := store.Delete(hostname)
		if fileErr != nil {
			return stopOut{}, fileErr
		}
		removed = ok
	} else {
		return stopOut{}, derr
	}
	if !removed {
		return stopOut{Removed: false, Hostname: hostname, Note: "no such tunnel"}, nil
	}

	note := "tunnel removed"
	if found && route.Access != nil && strings.TrimSpace(route.Access.AppID) != "" {
		if aerr := teardownAccessForMCP(ctx, route.Access); aerr != nil {
			note = "tunnel removed; its Cloudflare Access app may remain — remove it via the dashboard (" + aerr.Error() + ")"
		} else {
			note = "tunnel and its Cloudflare Access protection removed"
		}
	}
	return stopOut{Removed: true, Hostname: hostname, Note: note}, nil
}

func teardownAccessForMCP(ctx context.Context, info *routes.AccessInfo) error {
	client, _, err := newCloudflareClientFromKeychain()
	if err != nil {
		return err
	}
	accountID, err := resolveAccountID(ctx, client)
	if err != nil {
		return err
	}
	return unprotectHostname(ctx, client, accountID, info)
}

// --- protect_tunnel / unprotect_tunnel ---

type protectIn struct {
	Hostname string   `json:"hostname" jsonschema:"the tunnel hostname or subdomain to protect"`
	Mode     string   `json:"mode,omitempty" jsonschema:"login method: otp (email one-time PIN, default) or sso"`
	Allow    []string `json:"allow,omitempty" jsonschema:"who may sign in: emails or @domains (required for otp; optional for sso)"`
	IdP      string   `json:"idp,omitempty" jsonschema:"identity provider name for mode=sso"`
	Session  string   `json:"session,omitempty" jsonschema:"Access session duration, e.g. 24h (default 24h)"`
	Force    bool     `json:"force,omitempty" jsonschema:"allow risky choices such as allow=everyone"`
}

type protectOut struct {
	Hostname  string   `json:"hostname"`
	Protected bool     `json:"protected"`
	Mode      string   `json:"mode"`
	Allow     []string `json:"allow,omitempty"`
	IdP       string   `json:"idp,omitempty"`
	Note      string   `json:"note"`
}

type unprotectIn struct {
	Hostname string `json:"hostname" jsonschema:"the tunnel hostname or subdomain to make public again"`
}

type unprotectOut struct {
	Hostname  string `json:"hostname"`
	Protected bool   `json:"protected"`
	Note      string `json:"note"`
}

func (s mcpService) protectTunnel(ctx context.Context, in protectIn) (protectOut, error) {
	cfg, err := s.config()
	if err != nil {
		return protectOut{}, err
	}
	hostname, err := hostnameForStop(strings.TrimSpace(in.Hostname), cfg)
	if err != nil {
		return protectOut{}, err
	}
	route, ok := findRoute(ctx, cfg, hostname)
	if !ok {
		return protectOut{}, fmt.Errorf("no tunnel %s — create it first with create_http_tunnel", hostname)
	}

	client, _, err := newCloudflareClientFromKeychain()
	if errors.Is(err, cfapi.ErrMissingToken) {
		return protectOut{}, errAccessWriteScope
	}
	if err != nil {
		return protectOut{}, err
	}
	accountID, err := resolveAccountID(ctx, client)
	if err != nil {
		return protectOut{}, err
	}
	if status := cfapi.DetectAccess(ctx, client, accountID); status.State != cfapi.AccessReady {
		if status.State == cfapi.AccessTokenUnscoped {
			return protectOut{}, errAccessWriteScope
		}
		return protectOut{}, errors.New("Zero Trust is not enabled — run `vtunnel access setup` first")
	}

	info, err := protectHostname(ctx, client, accountID, hostname, protectOptions{
		Mode: in.Mode, Allow: in.Allow, IdP: in.IdP, Session: in.Session, Force: in.Force,
	})
	if err != nil {
		return protectOut{}, err
	}
	route.Access = info
	if err := api.New(cfg).AddRoute(ctx, route); err != nil {
		return protectOut{}, err
	}
	return protectOut{
		Hostname:  hostname,
		Protected: true,
		Mode:      info.Mode,
		Allow:     info.Allow,
		IdP:       info.IdP,
		Note:      "a Cloudflare Access login now guards https://" + hostname,
	}, nil
}

func (s mcpService) unprotectTunnel(ctx context.Context, in unprotectIn) (unprotectOut, error) {
	cfg, err := s.config()
	if err != nil {
		return unprotectOut{}, err
	}
	hostname, err := hostnameForStop(strings.TrimSpace(in.Hostname), cfg)
	if err != nil {
		return unprotectOut{}, err
	}
	route, ok := findRoute(ctx, cfg, hostname)
	if !ok || route.Access == nil {
		return unprotectOut{Hostname: hostname, Protected: false, Note: "tunnel is not protected"}, nil
	}
	client, _, err := newCloudflareClientFromKeychain()
	if err != nil {
		return unprotectOut{}, err
	}
	accountID, err := resolveAccountID(ctx, client)
	if err != nil {
		return unprotectOut{}, err
	}
	if err := unprotectHostname(ctx, client, accountID, route.Access); err != nil {
		return unprotectOut{}, err
	}
	route.Access = nil
	if err := api.New(cfg).AddRoute(ctx, route); err != nil {
		return unprotectOut{}, err
	}
	return unprotectOut{Hostname: hostname, Protected: false, Note: "https://" + hostname + " is now PUBLIC"}, nil
}

// --- inspect_requests ---

type inspectIn struct {
	Hostname string `json:"hostname,omitempty" jsonschema:"only show requests for this tunnel hostname or subdomain"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max number of recent requests to return (default 20)"`
	ID       uint64 `json:"id,omitempty" jsonschema:"if set, return the full request and response for this request id"`
}

type requestSummary struct {
	ID         uint64 `json:"id"`
	Time       string `json:"time"`
	Hostname   string `json:"hostname"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	Bytes      int64  `json:"bytes"`
	DurationMs int64  `json:"duration_ms"`
}

type requestDetail struct {
	ID                uint64              `json:"id"`
	Hostname          string              `json:"hostname"`
	Method            string              `json:"method"`
	Path              string              `json:"path"`
	Target            string              `json:"target"`
	Status            int                 `json:"status"`
	RequestHeaders    map[string][]string `json:"request_headers,omitempty"`
	RequestBody       string              `json:"request_body,omitempty"`
	RequestTruncated  bool                `json:"request_truncated,omitempty"`
	ResponseHeaders   map[string][]string `json:"response_headers,omitempty"`
	ResponseBody      string              `json:"response_body,omitempty"`
	ResponseTruncated bool                `json:"response_truncated,omitempty"`
}

type inspectOut struct {
	Requests []requestSummary `json:"requests,omitempty"`
	Detail   *requestDetail   `json:"detail,omitempty"`
}

func (s mcpService) inspect(ctx context.Context, in inspectIn) (inspectOut, error) {
	cfg, err := s.config()
	if err != nil {
		return inspectOut{}, err
	}
	client := api.New(cfg)

	if in.ID != 0 {
		exchange, err := client.GetExchange(ctx, in.ID)
		if err != nil {
			return inspectOut{}, fmt.Errorf("could not fetch request %d (the daemon keeps only recent requests in memory; is it running?): %w", in.ID, err)
		}
		detail := exchangeToDetail(exchange)
		return inspectOut{Detail: &detail}, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	host := strings.TrimSpace(in.Hostname)
	if host != "" {
		if resolved, herr := hostnameForStop(host, cfg); herr == nil {
			host = resolved
		}
	}
	filter := requestlog.Filter{Hostname: host, Limit: limit}
	entries, err := client.ListLogs(ctx, filter)
	if err != nil {
		store, serr := savedRequestLogStore()
		if serr != nil {
			return inspectOut{}, fmt.Errorf("could not read request logs: %w", err)
		}
		entries = store.List(filter)
	}
	out := inspectOut{Requests: make([]requestSummary, 0, len(entries))}
	for _, entry := range entries {
		out.Requests = append(out.Requests, entryToSummary(entry))
	}
	return out, nil
}

func entryToSummary(entry requestlog.Entry) requestSummary {
	return requestSummary{
		ID:         entry.ID,
		Time:       entry.Time.Format(time.RFC3339),
		Hostname:   entry.Hostname,
		Method:     entry.Method,
		Path:       entry.Path,
		Status:     entry.Status,
		Bytes:      entry.Bytes,
		DurationMs: entry.Duration.Milliseconds(),
	}
}

func exchangeToDetail(exchange requestlog.Exchange) requestDetail {
	return requestDetail{
		ID:                exchange.ID,
		Hostname:          exchange.Hostname,
		Method:            exchange.Method,
		Path:              exchange.Path,
		Target:            exchange.Target,
		Status:            exchange.Status,
		RequestHeaders:    exchange.RequestHeaders,
		RequestBody:       string(exchange.RequestBody),
		RequestTruncated:  exchange.RequestTruncated,
		ResponseHeaders:   exchange.ResponseHeaders,
		ResponseBody:      string(exchange.ResponseBody),
		ResponseTruncated: exchange.ResponseTruncated,
	}
}

// --- replay_request ---

type replayIn struct {
	ID uint64 `json:"id" jsonschema:"the request id to replay (re-issue to the upstream)"`
}

type replayOut struct {
	Replayed bool   `json:"replayed"`
	ID       uint64 `json:"id"`
	Note     string `json:"note"`
}

func (s mcpService) replay(ctx context.Context, in replayIn) (replayOut, error) {
	if in.ID == 0 {
		return replayOut{}, errors.New("id is required")
	}
	cfg, err := s.config()
	if err != nil {
		return replayOut{}, err
	}
	if err := api.New(cfg).ReplayRequest(ctx, in.ID); err != nil {
		return replayOut{}, fmt.Errorf("could not replay request %d (needs the daemon running): %w", in.ID, err)
	}
	return replayOut{Replayed: true, ID: in.ID, Note: "re-issued the captured request to the upstream"}, nil
}

// --- vtunnel://requests resource ---

func (s mcpService) requestsResource(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	cfg, err := s.config()
	if err != nil {
		return nil, err
	}
	filter := requestlog.Filter{Limit: 50}
	entries, err := api.New(cfg).ListLogs(ctx, filter)
	if err != nil {
		store, serr := savedRequestLogStore()
		if serr != nil {
			return nil, fmt.Errorf("could not read request logs: %w", err)
		}
		entries = store.List(filter)
	}
	summaries := make([]requestSummary, 0, len(entries))
	for _, entry := range entries {
		summaries = append(summaries, entryToSummary(entry))
	}
	data, err := json.MarshalIndent(summaries, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      "vtunnel://requests",
			MIMEType: "application/json",
			Text:     string(data),
		}},
	}, nil
}
