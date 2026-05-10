// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/json"
	"testing"

	"activesync-mcp/lib/config"

	"github.com/hstern/go-activesync/eas"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mockManagerOpts configures the test Manager built by
// newMockManager. account names the single account; access controls
// the per-class default. Set access= to config.AccessRW for write tools.
type mockManagerOpts struct {
	account string
	access  string
	classes map[string]string // per-class access overrides
}

// newMockManager builds a Manager whose client cache is pre-populated
// with c (via the SetClientForTest seam) so tool handlers running
// against this Manager never go through the real Provision path. The
// rest of the Manager — Config, StateProvider, SecretResolver,
// DeviceIDProvider — is wired with placeholder values that are never
// consulted because the cache hit short-circuits getOrBuildClient.
func newMockManager(t *testing.T, c eas.Client, opts mockManagerOpts) *Manager {
	t.Helper()
	if opts.account == "" {
		opts.account = "alpha"
	}
	if opts.access == "" {
		opts.access = config.AccessRO
	}
	a := config.Account{
		Name:          opts.account,
		ServerURL:     "http://placeholder.invalid",
		Username:      "u",
		ASVersion:     "14.1",
		DefaultAccess: opts.access,
		Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: opts.account},
	}
	if opts.classes != nil {
		a.Access = opts.classes
	}
	cfg := &config.Config{Accounts: []config.Account{a}}
	m := NewManager(cfg,
		&fakeStateProvider{},
		&fakeResolver{pw: map[string]string{opts.account: "p"}},
		staticDeviceIDs{opts.account: "abc123"},
	)
	m.SetClientForTest(opts.account, c)
	return m
}

// callToolErr invokes a registered tool by name and returns the
// CallToolResult so callers can inspect IsError + content. Useful for
// asserting that a handler wraps an EAS-layer error correctly.
func callToolErr(t *testing.T, srv *mcp.Server, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// errText returns the IsError result's first text content for substring
// assertions. Calls t.Fatal if the result isn't an error or has no text.
func errText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("want IsError, got success: %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type: %T", res.Content[0])
	}
	return tc.Text
}

// callTool invokes a registered tool by name on srv via the in-process
// MCP transport pair, returning the parsed JSON object from the first
// TextContent.
func callTool(t *testing.T, srv *mcp.Server, name string, args any) map[string]any {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type: %T", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, tc.Text)
	}
	return out
}
