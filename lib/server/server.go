// Package server wires the activesync-mcp configuration into an MCP
// stdio server. Phase 1 only registers the `accounts_list` admin tool;
// later phases attach email/calendar/contacts/tasks/notes tool sets.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"activesync-mcp/lib/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Build constructs the MCP server with all currently-registered tools.
//
// If m is non-nil, data tools (email_list_folders, email_list, email_get,
// and so on) are registered. Pass nil to build a server with only the
// admin tools — used by tests that don't care about the EAS data path.
func Build(cfg *config.Config, m *Manager) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "activesync-mcp", Version: version},
		nil,
	)
	registerAccountsList(s, cfg)
	if m != nil {
		registerEmailReadTools(s, cfg, m)
		registerEmailWriteTools(s, cfg, m)
		registerCalendarTools(s, cfg, m)
		registerPIMTools(s, cfg, m)
		registerExtraTools(s, cfg, m)
	}
	return s
}

// version is bumped per release. Phase 1 placeholder.
const version = "0.1.0-dev"

// AccountsListInput is empty; all configured accounts are returned.
type AccountsListInput struct{}

// AccountSummary is the per-account row in the accounts_list output.
type AccountSummary struct {
	Name           string            `json:"name"`
	ServerURL      string            `json:"server_url"`
	Username       string            `json:"username"`
	ASVersion      string            `json:"as_version"`
	DefaultAccess  string            `json:"default_access"`
	AccessOverride map[string]string `json:"access_override,omitempty"`
	Writable       []string          `json:"writable_classes"`
}

// AccountsListOutput wraps the list so the schema describes a top-level object.
type AccountsListOutput struct {
	Accounts []AccountSummary `json:"accounts"`
}

func registerAccountsList(s *mcp.Server, cfg *config.Config) {
	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "accounts_list",
			Description: "List ActiveSync accounts known to this server, with each account's read/write capabilities per class.",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, _ AccountsListInput) (*mcp.CallToolResult, AccountsListOutput, error) {
			out := buildAccountsList(cfg)
			payload, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return nil, AccountsListOutput{}, fmt.Errorf("marshal: %w", err)
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}},
			}, out, nil
		},
	)
}

// buildAccountsList is factored out so it can be unit-tested without an
// MCP server roundtrip.
func buildAccountsList(cfg *config.Config) AccountsListOutput {
	out := AccountsListOutput{Accounts: make([]AccountSummary, 0, len(cfg.Accounts))}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		summary := AccountSummary{
			Name:          a.Name,
			ServerURL:     a.ServerURL,
			Username:      a.Username,
			ASVersion:     a.ASVersion,
			DefaultAccess: a.DefaultAccess,
		}
		if len(a.Access) > 0 {
			summary.AccessOverride = make(map[string]string, len(a.Access))
			maps.Copy(summary.AccessOverride, a.Access)
		}
		for _, c := range config.AllClasses() {
			if a.CanWrite(c) {
				summary.Writable = append(summary.Writable, string(c))
			}
		}
		out.Accounts = append(out.Accounts, summary)
	}
	return out
}
