package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"vtunnel/internal/mcpsetup"
)

// mcpEnv builds the registration environment from the scope/project flags.
func mcpEnv(scopeFlag, projectDir string) (mcpsetup.Env, error) {
	scope, err := resolveScope(scopeFlag)
	if err != nil {
		return mcpsetup.Env{}, err
	}
	srv, err := mcpsetup.DefaultServer()
	if err != nil {
		return mcpsetup.Env{}, err
	}
	return mcpsetup.Env{Scope: scope, ProjectDir: strings.TrimSpace(projectDir), Server: srv}, nil
}

func resolveScope(s string) (mcpsetup.Scope, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "user":
		return mcpsetup.ScopeUser, nil
	case "project":
		return mcpsetup.ScopeProject, nil
	default:
		return "", fmt.Errorf("invalid --scope %q: use user or project", s)
	}
}

func clientIDs() string {
	ids := make([]string, 0)
	for _, c := range mcpsetup.Clients() {
		ids = append(ids, c.ID())
	}
	return strings.Join(ids, ", ")
}

// selectClients resolves which clients an add/remove targets: named ones, all
// of them (--all), or — by default — only those that look installed.
func selectClients(ctx context.Context, args []string, all bool, env mcpsetup.Env) ([]mcpsetup.Client, error) {
	if len(args) > 0 {
		out := make([]mcpsetup.Client, 0, len(args))
		for _, id := range args {
			client, ok := mcpsetup.Find(strings.ToLower(strings.TrimSpace(id)))
			if !ok {
				return nil, fmt.Errorf("unknown client %q (known: %s)", id, clientIDs())
			}
			out = append(out, client)
		}
		return out, nil
	}
	if all {
		return mcpsetup.Clients(), nil
	}
	var out []mcpsetup.Client
	for _, client := range mcpsetup.Clients() {
		if installed, _ := client.Detect(ctx, env); installed {
			out = append(out, client)
		}
	}
	return out, nil
}

func addScopeFlags(cmd *cobra.Command, scope, projectDir *string) {
	cmd.Flags().StringVar(scope, "scope", "user", "where to register: user (global) or project")
	cmd.Flags().StringVar(projectDir, "project-dir", "", "project directory for --scope=project (defaults to the working directory)")
}

func newMCPAddCommand() *cobra.Command {
	var scope, projectDir string
	var all bool
	cmd := &cobra.Command{
		Use:   "add [client...]",
		Short: "Register the vtunnel MCP server in one or more clients",
		Long: "Register `vtunnel mcp serve` in AI clients. With no arguments, adds it to\n" +
			"every client that looks installed; pass client ids to target specific ones\n" +
			"(" + clientIDs() + "), or --all for every supported client.",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			env, err := mcpEnv(scope, projectDir)
			if err != nil {
				return err
			}
			clients, err := selectClients(cmd.Context(), args, all, env)
			if err != nil {
				return err
			}
			if len(clients) == 0 {
				fmt.Fprintf(out, "No installed clients detected. Name one (%s) or pass --all.\n", clientIDs())
				return nil
			}
			for _, client := range clients {
				if err := client.Add(cmd.Context(), env); err != nil {
					fmt.Fprintf(out, "✗ %-12s %v\n", client.Name(), err)
					continue
				}
				fmt.Fprintf(out, "✓ %-12s %s\n", client.Name(), locationOf(client, env))
			}
			fmt.Fprintf(out, "\nServer: %s %s\n", env.Server.Command, strings.Join(env.Server.Args, " "))
			return nil
		},
	}
	addScopeFlags(cmd, &scope, &projectDir)
	cmd.Flags().BoolVar(&all, "all", false, "register in every supported client, installed or not")
	return cmd
}

func newMCPRemoveCommand() *cobra.Command {
	var scope, projectDir string
	var all bool
	cmd := &cobra.Command{
		Use:     "remove [client...]",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove the vtunnel MCP server from one or more clients",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			env, err := mcpEnv(scope, projectDir)
			if err != nil {
				return err
			}
			clients, err := selectClients(cmd.Context(), args, all, env)
			if err != nil {
				return err
			}
			if len(clients) == 0 {
				fmt.Fprintf(out, "No installed clients detected. Name one (%s) or pass --all.\n", clientIDs())
				return nil
			}
			for _, client := range clients {
				if err := client.Remove(cmd.Context(), env); err != nil {
					fmt.Fprintf(out, "✗ %-12s %v\n", client.Name(), err)
					continue
				}
				fmt.Fprintf(out, "✓ %-12s removed\n", client.Name())
			}
			return nil
		},
	}
	addScopeFlags(cmd, &scope, &projectDir)
	cmd.Flags().BoolVar(&all, "all", false, "remove from every supported client")
	return cmd
}

func newMCPStatusCommand() *cobra.Command {
	var scope, projectDir string
	var check bool
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"health"},
		Short:   "Show which clients have the vtunnel MCP server configured",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCPStatus(cmd.Context(), cmd.OutOrStdout(), mcpStatusOptions{scope: scope, projectDir: projectDir, check: check})
		},
	}
	addScopeFlags(cmd, &scope, &projectDir)
	cmd.Flags().BoolVar(&check, "check", false, "also launch the server and verify it responds")
	return cmd
}

type mcpStatusOptions struct {
	scope      string
	projectDir string
	check      bool
}

// runMCPStatus prints a per-client setup table and, with check, a server
// self-check. It is shared by `vtunnel mcp status` and the non-interactive bare
// `vtunnel mcp`.
func runMCPStatus(ctx context.Context, out io.Writer, opts mcpStatusOptions) error {
	env, err := mcpEnv(opts.scope, opts.projectDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "MCP clients (scope: %s)\n\n", env.Scope)
	for _, client := range mcpsetup.Clients() {
		st := client.Status(ctx, env)
		detail := st.Location
		if st.Note != "" {
			detail = strings.TrimSpace(detail + "  " + st.Note)
		}
		if st.Err != nil {
			detail = st.Err.Error()
		}
		fmt.Fprintf(out, "  %s %-12s %s\n", statusBadge(st), client.Name(), detail)
	}
	fmt.Fprintln(out, "\n  ✓ configured   ○ installed, not configured   · not detected")

	if opts.check {
		fmt.Fprintln(out)
		checkCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		tools, err := mcpsetup.SelfCheck(checkCtx, env.Server)
		if err != nil {
			fmt.Fprintf(out, "Server self-check: ✗ %v\n", err)
		} else {
			fmt.Fprintf(out, "Server self-check: ✓ responds with %d tools (%s)\n", len(tools), strings.Join(tools, ", "))
		}
	}
	return nil
}

func statusBadge(st mcpsetup.Status) string {
	switch {
	case st.Configured:
		return "✓"
	case st.Installed:
		return "○"
	default:
		return "·"
	}
}

func locationOf(client mcpsetup.Client, env mcpsetup.Env) string {
	manual := client.Manual(env)
	if manual.Path != "" {
		return manual.Path
	}
	return manual.Command
}
