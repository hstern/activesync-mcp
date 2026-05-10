// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"activesync-mcp/lib/server"
)

// ---- folder management -------------------------------------------------

func TestE2E_FolderCreate(t *testing.T) {
	cs := e2eClient(t)
	name := fmt.Sprintf("e2e-folder-%d", time.Now().UnixNano())
	var out server.FolderCreateOutput
	callTool(t, cs, "folder_create", server.FolderCreateInput{
		Account:     "test",
		ParentID:    "0",
		DisplayName: name,
		Type:        "email",
	}, &out)
	if out.ID == "" {
		t.Error("created folder id empty")
	}
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "folder_delete", server.FolderDeleteInput{
			Account: "test", ID: out.ID,
		})
	})
}

func TestE2E_FolderRename(t *testing.T) {
	cs := e2eClient(t)
	var created server.FolderCreateOutput
	callTool(t, cs, "folder_create", server.FolderCreateInput{
		Account:     "test",
		ParentID:    "0",
		DisplayName: fmt.Sprintf("e2e-rename-pre-%d", time.Now().UnixNano()),
		Type:        "email",
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "folder_delete", server.FolderDeleteInput{
			Account: "test", ID: created.ID,
		})
	})
	callTool(t, cs, "folder_rename", server.FolderRenameInput{
		Account: "test", ID: created.ID,
		NewDisplayName: fmt.Sprintf("e2e-rename-post-%d", time.Now().UnixNano()),
	}, nil)
}

func TestE2E_FolderEmpty(t *testing.T) {
	cs := e2eClient(t)
	var created server.FolderCreateOutput
	callTool(t, cs, "folder_create", server.FolderCreateInput{
		Account:     "test",
		ParentID:    "0",
		DisplayName: fmt.Sprintf("e2e-empty-%d", time.Now().UnixNano()),
		Type:        "email",
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "folder_delete", server.FolderDeleteInput{
			Account: "test", ID: created.ID,
		})
	})
	callTool(t, cs, "folder_empty", server.FolderEmptyInput{
		Account: "test", FolderID: created.ID,
	}, nil)
}

func TestE2E_FolderDelete(t *testing.T) {
	cs := e2eClient(t)
	var created server.FolderCreateOutput
	callTool(t, cs, "folder_create", server.FolderCreateInput{
		Account:     "test",
		ParentID:    "0",
		DisplayName: fmt.Sprintf("e2e-del-%d", time.Now().UnixNano()),
		Type:        "email",
	}, &created)
	// Z-Push BackendCombined returns HTTP 500 on FolderDelete for
	// caller-created top-level folders (testenv Dovecot ACL surface);
	// tracked at hstern/go-activesync#3. Accept either success or the
	// known IsError — the contract here is "the tool reaches EAS".
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "folder_delete",
		Arguments: server.FolderDeleteInput{Account: "test", ID: created.ID},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if res.IsError {
		t.Logf("folder_delete returned IsError (Z-Push testenv limitation): %s",
			formatContent(res.Content))
	}
}

// ---- directory ---------------------------------------------------------

func TestE2E_GALSearch(t *testing.T) {
	cs := e2eClient(t)
	var out server.GALSearchOutput
	// "integration" matches at least the test user via Z-Push GAL.
	// May come back empty depending on backend config — empty is a
	// valid result for "no GAL backend wired".
	callTool(t, cs, "gal_search", server.GALSearchInput{
		Account: "test", Query: "integration", Limit: 5,
	}, &out)
	_ = out
}

func TestE2E_ResolveRecipients(t *testing.T) {
	cs := e2eClient(t)
	// Z-Push 2.7's ResolveRecipients returns Status=5 (ServerError)
	// when no GAL backend is configured — testenv has no LDAP. Tracked
	// at hstern/go-activesync#3. Tolerate to keep the tier green.
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "resolve_recipients",
		Arguments: server.ResolveRecipientsInput{
			Account:    "test",
			Recipients: []string{"integration@asmcp.test"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if res.IsError {
		t.Logf("resolve_recipients returned IsError (testenv has no GAL backend): %s",
			formatContent(res.Content))
		return
	}
	// If it did succeed, sanity-check the shape.
	var out server.ResolveRecipientsOutput
	decodeStructured(t, res, &out)
	if len(out.Rows) != 1 {
		t.Errorf("got %d rows for 1 input", len(out.Rows))
	}
}

// ---- settings ----------------------------------------------------------

func TestE2E_OofGet(t *testing.T) {
	cs := e2eClient(t)
	var out server.OofGetOutput
	callTool(t, cs, "oof_get", server.OofGetInput{Account: "test"}, &out)
	if out.State == "" {
		t.Error("OOF State should always be populated")
	}
}

func TestE2E_OofSet(t *testing.T) {
	cs := e2eClient(t)
	// Toggle disabled → global → disabled to leave the user's state
	// where we found it.
	var before server.OofGetOutput
	callTool(t, cs, "oof_get", server.OofGetInput{Account: "test"}, &before)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "oof_set", server.OofSetInput{
			Account: "test", State: before.State,
			InternalReply: before.InternalReply.ReplyMessage,
		})
	})

	callTool(t, cs, "oof_set", server.OofSetInput{
		Account:       "test",
		State:         "global",
		InternalReply: "I am out (e2e test)",
	}, nil)
}

// ---- misc --------------------------------------------------------------

func TestE2E_ItemCount(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	var out server.ItemCountOutput
	callTool(t, cs, "item_count", server.ItemCountInput{
		Account: "test", FolderIDs: []string{inbox},
	}, &out)
	if len(out.Rows) != 1 {
		t.Errorf("got %d rows for 1 folder", len(out.Rows))
	}
	if out.Rows[0].FolderID != inbox {
		t.Errorf("FolderID = %q, want %q", out.Rows[0].FolderID, inbox)
	}
}
