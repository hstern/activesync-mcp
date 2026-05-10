// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

package main

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"activesync-mcp/lib/server"
)

func TestE2E_EmailListFolders(t *testing.T) {
	cs := e2eClient(t)
	var out server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "test"}, &out)
	have := map[string]bool{}
	for _, f := range out.Folders {
		have[f.Type] = true
	}
	for _, want := range []string{"Inbox", "SentItems", "Drafts", "DeletedItems"} {
		if !have[want] {
			t.Errorf("missing %s folder in: %+v", want, out.Folders)
		}
	}
}

func TestE2E_EmailList(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	var out server.EmailListOutput
	callTool(t, cs, "email_list", server.EmailListInput{
		Account: "test", FolderID: inbox, WindowSize: 5,
	}, &out)
	// Empty Inbox is fine on a fresh testenv. The assertion is just
	// that the tool completed without error and gave us a valid envelope.
	if out.SyncCursor == "" {
		t.Error("SyncCursor should always be populated after list")
	}
}

func TestE2E_EmailSend(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	subject := sendLoopbackEmail(t, cs, "hello from e2e")
	got := waitForMessageInInbox(t, cs, inbox, subject)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
			Account: "test", FolderID: inbox, ID: got.ID,
		})
	})
	if got.From == "" {
		t.Error("delivered message has no From")
	}
}

func TestE2E_EmailGet(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	subject := sendLoopbackEmail(t, cs, "body for email_get test")
	row := waitForMessageInInbox(t, cs, inbox, subject)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
			Account: "test", FolderID: inbox, ID: row.ID,
		})
	})

	var out server.EmailGetOutput
	callTool(t, cs, "email_get", server.EmailGetInput{
		Account: "test", FolderID: inbox, ID: row.ID, Format: "plain",
	}, &out)
	if out.Subject != subject {
		t.Errorf("Subject = %q, want %q", out.Subject, subject)
	}
}

func TestE2E_EmailSearch(t *testing.T) {
	cs := e2eClient(t)
	// Search "e2e" is broad enough to match anything our other tests
	// have left in the inbox; it doesn't need to find anything specific
	// to be a successful round-trip — the call just has to come back
	// without error and return a valid envelope.
	var out server.EmailSearchOutput
	callTool(t, cs, "email_search", server.EmailSearchInput{
		Account: "test", Query: "e2e", Limit: 5,
	}, &out)
	// Range is always populated, even on zero hits.
	if out.Range == "" {
		t.Error("Range should always be populated")
	}
}

func TestE2E_EmailReply(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	subject := sendLoopbackEmail(t, cs, "original body")
	row := waitForMessageInInbox(t, cs, inbox, subject)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
			Account: "test", FolderID: inbox, ID: row.ID,
		})
	})

	// Z-Push BackendIMAP rejects SmartReply with Status=120 on
	// plain-text-only loopback messages — testenv limitation tracked at
	// hstern/go-activesync#3.
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "email_reply",
		Arguments: server.EmailReplyInput{
			Account: "test", FolderID: inbox, ID: row.ID,
			BodyText: "thanks for the e2e test message",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}
	if res.IsError {
		t.Logf("email_reply returned IsError (Z-Push BackendIMAP SmartReply limitation): %s",
			formatContent(res.Content))
	}
}

func TestE2E_EmailForward(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	subject := sendLoopbackEmail(t, cs, "to be forwarded")
	row := waitForMessageInInbox(t, cs, inbox, subject)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
			Account: "test", FolderID: inbox, ID: row.ID,
		})
	})

	// SmartForward works since the testenv fix for IMAP_DEFAULT_CHARSET
	// + IMAP_INLINE_FORWARD landed (hstern/go-activesync#3).
	callTool(t, cs, "email_forward", server.EmailForwardInput{
		Account: "test", FolderID: inbox, ID: row.ID,
		To:       []server.EmailAddress{{Address: "integration@asmcp.test"}},
		BodyText: "fwd from e2e test",
	}, nil)
}

func TestE2E_EmailMove(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	trash := findFolderByType(t, cs, "DeletedItems")
	if trash == "" {
		t.Skip("no DeletedItems folder; testenv may be misconfigured")
	}
	subject := sendLoopbackEmail(t, cs, "to be moved")
	row := waitForMessageInInbox(t, cs, inbox, subject)

	var out server.EmailMoveOutput
	callTool(t, cs, "email_move", server.EmailMoveInput{
		Account: "test", FromFolder: inbox, ToFolder: trash, IDs: []string{row.ID},
	}, &out)
	if len(out.Results) != 1 {
		t.Fatalf("got %d move results", len(out.Results))
	}
	if out.Results[0].SrcID != row.ID {
		t.Errorf("SrcID = %q, want %q", out.Results[0].SrcID, row.ID)
	}
	// Schedule cleanup against the new id in trash.
	t.Cleanup(func() {
		if out.Results[0].NewID != "" {
			_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
				Account: "test", FolderID: trash, ID: out.Results[0].NewID,
			})
		}
	})
}

func TestE2E_EmailSetFlags(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	subject := sendLoopbackEmail(t, cs, "to be flagged")
	row := waitForMessageInInbox(t, cs, inbox, subject)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
			Account: "test", FolderID: inbox, ID: row.ID,
		})
	})

	flagged := true
	read := true
	callTool(t, cs, "email_set_flags", server.EmailSetFlagsInput{
		Account: "test", FolderID: inbox, ID: row.ID,
		Read: &read, Flagged: &flagged,
	}, nil)
}

func TestE2E_EmailDelete(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	subject := sendLoopbackEmail(t, cs, "to be deleted")
	row := waitForMessageInInbox(t, cs, inbox, subject)

	callTool(t, cs, "email_delete", server.EmailDeleteInput{
		Account: "test", FolderID: inbox, ID: row.ID,
	}, nil)
	// Don't t.Cleanup deletion — that's the test itself.
}

