package cli

import (
	"context"
	"fmt"

	"vtunnel/internal/mcpsetup"
	"vtunnel/internal/mcpsetupui"
)

// runMCPSetupTUI opens the MCP setup dashboard at user scope.
func runMCPSetupTUI(ctx context.Context) error {
	env, err := mcpEnv("user", "")
	if err != nil {
		return err
	}
	return mcpsetupui.Run(ctx, mcpUIManager{env: env})
}

// mcpUIManager adapts mcpsetup to the dashboard's Manager interface.
type mcpUIManager struct {
	env mcpsetup.Env
}

func (m mcpUIManager) Scope() string { return string(m.env.Scope) }

func (m mcpUIManager) List(ctx context.Context) ([]mcpsetupui.Client, error) {
	clients := mcpsetup.Clients()
	out := make([]mcpsetupui.Client, 0, len(clients))
	for _, client := range clients {
		st := client.Status(ctx, m.env)
		manual := client.Manual(m.env)
		note := st.Note
		if st.Err != nil {
			note = st.Err.Error()
		}
		out = append(out, mcpsetupui.Client{
			ID:            st.ID,
			Name:          st.Name,
			Installed:     st.Installed,
			Configured:    st.Configured,
			Location:      st.Location,
			Note:          note,
			ManualPath:    manual.Path,
			ManualCommand: manual.Command,
			ManualSnippet: manual.Snippet,
		})
	}
	return out, nil
}

func (m mcpUIManager) Add(ctx context.Context, id string) error {
	client, ok := mcpsetup.Find(id)
	if !ok {
		return fmt.Errorf("unknown client %q", id)
	}
	return client.Add(ctx, m.env)
}

func (m mcpUIManager) Remove(ctx context.Context, id string) error {
	client, ok := mcpsetup.Find(id)
	if !ok {
		return fmt.Errorf("unknown client %q", id)
	}
	return client.Remove(ctx, m.env)
}

func (m mcpUIManager) SelfCheck(ctx context.Context) ([]string, error) {
	return mcpsetup.SelfCheck(ctx, m.env.Server)
}
