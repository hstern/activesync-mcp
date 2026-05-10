// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

package main

import (
	"testing"

	"activesync-mcp/lib/server"
)

// TestE2E_AccountsList: the simplest possible round-trip — spawn the
// binary, call the one tool that doesn't even hit EAS, validate the
// MCP boundary works at all. If this fails the rest of Tier 3 is moot.
func TestE2E_AccountsList(t *testing.T) {
	cs := e2eClient(t)
	var out server.AccountsListOutput
	callTool(t, cs, "accounts_list", server.AccountsListInput{}, &out)
	if len(out.Accounts) != 1 {
		t.Fatalf("got %d accounts; want 1 (the testdata config defines one)", len(out.Accounts))
	}
	a := out.Accounts[0]
	if a.Name != "test" {
		t.Errorf("account name = %q, want test", a.Name)
	}
	if a.Username != "integration" {
		t.Errorf("username = %q", a.Username)
	}
	if a.DefaultAccess != "rw" {
		t.Errorf("default_access = %q, want rw (the e2e config opens up writes)", a.DefaultAccess)
	}
}
