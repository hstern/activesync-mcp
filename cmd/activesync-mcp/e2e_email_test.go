// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"activesync-mcp/lib/server"
)

// findInboxID lists folders and returns the Inbox's server ID. Most
// email tests start by needing this; centralised so per-test setup
// stays small.
func findInboxID(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	var out server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "test"}, &out)
	for _, f := range out.Folders {
		if f.Type == "Inbox" {
			return f.ID
		}
	}
	t.Fatalf("no Inbox folder in %d-folder list", len(out.Folders))
	return ""
}

// findFolderByType returns the first folder of the given type (Sent,
// Drafts, DeletedItems, etc.). Returns "" if none.
func findFolderByType(t *testing.T, cs *mcp.ClientSession, want string) string {
	t.Helper()
	var out server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "test"}, &out)
	for _, f := range out.Folders {
		if f.Type == want {
			return f.ID
		}
	}
	return ""
}

// sendLoopbackEmail sends a message to integration@asmcp.test (the
// testenv user) and returns the unique subject that identifies it.
// Caller polls the inbox to find the resulting EmailRow.
func sendLoopbackEmail(t *testing.T, cs *mcp.ClientSession, body string) string {
	t.Helper()
	subject := fmt.Sprintf("e2e-%s-%d", t.Name(), time.Now().UnixNano())
	callTool(t, cs, "email_send", server.EmailSendInput{
		Account:  "test",
		To:       []server.EmailAddress{{Address: "integration@asmcp.test"}},
		Subject:  subject,
		BodyText: body,
	}, nil)
	return subject
}

// waitForMessageInInbox polls email_list (with windowed re-bootstrap)
// until a message with the given subject appears, or fails the test.
func waitForMessageInInbox(t *testing.T, cs *mcp.ClientSession, inboxID, subject string) server.EmailRow {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var out server.EmailListOutput
		callTool(t, cs, "email_list", server.EmailListInput{
			Account: "test", FolderID: inboxID, WindowSize: 50,
		}, &out)
		for _, e := range out.Items {
			if e.Subject == subject {
				return e
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("message %q never appeared in Inbox after 20s", subject)
	return server.EmailRow{}
}

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

// mustCallToolBool is a sloppy variant for cleanup paths that just
// want to fire-and-forget — it never fails the test. Uses its own
// background context with a 30s timeout because t.Context() is
// already cancelled by the time t.Cleanup runs.
func mustCallToolBool(t *testing.T, cs *mcp.ClientSession, name string, args any) bool {
	t.Helper()
	ctx, cancel := contextWithTimeout(30 * time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Logf("cleanup CallTool(%s): %v", name, err)
		return false
	}
	if res.IsError {
		t.Logf("cleanup CallTool(%s) IsError: %s", name, formatContent(res.Content))
		return false
	}
	return true
}

// trim is duplicated from the std strings to keep this file's import
// list manageable; only used in error messages above.
var _ = strings.TrimSpace
