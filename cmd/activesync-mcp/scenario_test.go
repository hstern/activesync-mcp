// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build scenario

// Tier 4 — scripted multi-call flows that exercise the kind of
// dependent-call sequence an MCP host typically issues: state
// preservation across calls, out-of-band changes between steps,
// chains where one tool's output feeds another's input.
//
// Reuses the binary-spawn helpers from e2e_helpers_test.go (tagged
// `e2e || scenario`) so this file stays focused on the flows.

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"activesync-mcp/lib/server"
)

// TestScenario_TriageInbox: send a few messages, poll until each one
// is visible via email_list, then email_get on the last one.
// Validates the list→get chain and that EAS Sync's incremental
// delta-deliver model surfaces every queued change across multiple
// calls (the inbox-summary path an agent takes).
func TestScenario_TriageInbox(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)

	subjects := make([]string, 3)
	for i := range subjects {
		subjects[i] = sendLoopbackEmail(t, cs, fmt.Sprintf("triage body %d", i))
	}

	// Accumulate across deltas — each email_list call only returns
	// what's been added since the last sync cursor advance, so we
	// have to merge across iterations to see all 3.
	seen := map[string]server.EmailRow{}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(seen) < len(subjects) {
		var listed server.EmailListOutput
		callTool(t, cs, "email_list", server.EmailListInput{
			Account: "test", FolderID: inbox, WindowSize: 10,
		}, &listed)
		for _, m := range listed.Items {
			for _, s := range subjects {
				if m.Subject == s {
					seen[s] = m
				}
			}
		}
		if len(seen) < len(subjects) {
			time.Sleep(1 * time.Second)
		}
	}
	if len(seen) < len(subjects) {
		t.Fatalf("only %d of %d messages appeared in 30s", len(seen), len(subjects))
	}
	t.Cleanup(func() {
		for _, row := range seen {
			_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
				Account: "test", FolderID: inbox, ID: row.ID,
			})
		}
	})

	// Get the full body of the latest message we sent (the chain's
	// final step: list → pick → fetch).
	last := seen[subjects[len(subjects)-1]]
	var got server.EmailGetOutput
	callTool(t, cs, "email_get", server.EmailGetInput{
		Account: "test", FolderID: inbox, ID: last.ID, Format: "plain",
	}, &got)
	if got.Subject != last.Subject {
		t.Errorf("got body for wrong message: %q vs %q", got.Subject, last.Subject)
	}
}

// TestScenario_ReplyWithContext: list → get → reply, then check Sent
// to verify the reply landed. Reply may IsError on Z-Push BackendIMAP
// (testenv quirk) — we skip the verification leg in that case but
// still exercise the call chain.
func TestScenario_ReplyWithContext(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	subject := sendLoopbackEmail(t, cs, "original — please reply")
	row := waitForMessageInInbox(t, cs, inbox, subject)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
			Account: "test", FolderID: inbox, ID: row.ID,
		})
	})

	// get → reply chain.
	var orig server.EmailGetOutput
	callTool(t, cs, "email_get", server.EmailGetInput{
		Account: "test", FolderID: inbox, ID: row.ID, Format: "plain",
	}, &orig)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "email_reply",
		Arguments: server.EmailReplyInput{
			Account: "test", FolderID: inbox, ID: row.ID,
			BodyText: "scenario reply: thanks for: " + orig.Subject,
		},
	})
	if err != nil {
		t.Fatalf("email_reply transport error: %v", err)
	}
	if res.IsError {
		t.Logf("email_reply IsError (Z-Push BackendIMAP testenv limit; hstern/go-activesync#3): %s",
			formatContent(res.Content))
		return
	}

	// Verification: Sent should now contain a reply with the same
	// subject prefix. SmartReply sets Subject="RE: <original>" or
	// keeps the original subject depending on server convention.
	sent := findFolderByType(t, cs, "SentItems")
	if sent == "" {
		t.Skip("no SentItems folder")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var listed server.EmailListOutput
		callTool(t, cs, "email_list", server.EmailListInput{
			Account: "test", FolderID: sent, WindowSize: 20,
		}, &listed)
		for _, m := range listed.Items {
			if strings.Contains(m.Subject, subject) {
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Logf("reply not in Sent within 15s — Z-Push BackendIMAP may not echo replies into Sent")
}

// TestScenario_TodaysCalendar: create a calendar event, then poll
// calendar_list_events until the create propagates to the CalDAV
// store and is reflected in a Sync delta. Validates the create →
// list-and-find chain across calls (the agent flow for "what's on my
// calendar today after I added something").
func TestScenario_TodaysCalendar(t *testing.T) {
	cs := e2eClient(t)
	cal := findCalendarFolder(t, cs)
	subject := fmt.Sprintf("scenario-today-%d", time.Now().UnixNano())
	start := time.Now().Add(2 * time.Hour).Truncate(time.Hour).UTC()

	var created server.CalendarCreateEventOutput
	callTool(t, cs, "calendar_create_event", server.CalendarCreateEventInput{
		Account: "test", FolderID: cal,
		Subject:    subject,
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
		BusyStatus: 2,
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "calendar_delete_event", server.CalendarDeleteEventInput{
			Account: "test", FolderID: cal, ID: created.ID,
		})
	})

	// Z-Push BackendCalDAV writes the event asynchronously; the first
	// few Sync calls may return empty Added lists. Poll across deltas.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var listed server.CalendarListEventsOutput
		callTool(t, cs, "calendar_list_events", server.CalendarListEventsInput{
			Account: "test", FolderID: cal, WindowSize: 20, DateWindow: "1d",
		}, &listed)
		for _, ev := range listed.Events {
			if ev.Subject == subject {
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Logf("created event %q didn't surface via calendar_list_events within 20s — Z-Push BackendCalDAV doesn't echo caller-created events through subsequent Sync deltas in the same device session (hstern/go-activesync#3)",
		subject)
}

// TestScenario_OOFCycle: read OOF state, set it to global, read back,
// set to disabled, read back. Exercises the settings state machine
// across four calls. Z-Push BackendIMAP doesn't actually persist OOF
// settings (the testenv has no Settings store), so we exercise the
// chain but tolerate readback divergence — the value of this test on
// testenv is regression coverage for the call sequence itself.
// Tracked at hstern/go-activesync#3.
func TestScenario_OOFCycle(t *testing.T) {
	cs := e2eClient(t)

	// Snapshot existing state so we can restore on cleanup.
	var before server.OofGetOutput
	callTool(t, cs, "oof_get", server.OofGetInput{Account: "test"}, &before)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "oof_set", server.OofSetInput{
			Account: "test", State: before.State,
			InternalReply: before.InternalReply.ReplyMessage,
		})
	})

	const replyMsg = "scenario test: I am out"
	callTool(t, cs, "oof_set", server.OofSetInput{
		Account:       "test",
		State:         "global",
		InternalReply: replyMsg,
	}, nil)

	var enabled server.OofGetOutput
	callTool(t, cs, "oof_get", server.OofGetInput{Account: "test"}, &enabled)
	if enabled.State != "global" {
		t.Logf("OOF readback after enable = %q (Z-Push testenv doesn't persist OOF; hstern/go-activesync#3)",
			enabled.State)
	} else if !strings.Contains(enabled.InternalReply.ReplyMessage, "scenario test") {
		t.Logf("OOF reply not round-tripped: %q (testenv limit)",
			enabled.InternalReply.ReplyMessage)
	}

	callTool(t, cs, "oof_set", server.OofSetInput{
		Account: "test", State: "disabled",
	}, nil)

	var disabled server.OofGetOutput
	callTool(t, cs, "oof_get", server.OofGetInput{Account: "test"}, &disabled)
	if disabled.State != "disabled" && enabled.State == "global" {
		// Only flag this if the enable readback worked — otherwise
		// the testenv just isn't tracking state at all.
		t.Errorf("State after disable = %q, want disabled", disabled.State)
	}
}

// TestScenario_FolderHierarchyChange: prime FolderSync, create a
// folder, then poll for the new folder to surface in subsequent
// list_folders calls. Validates that mid-session hierarchy changes
// reflect in the cached folder list — the chain a folder-management
// UI takes after a "create new folder" action.
//
// History: an earlier version of this test interpreted empty-second-
// delta results as a Z-Push BackendIMAP quirk and t.Logf'd through
// it. The actual root cause was on our side: the *_list_folders
// handlers returned fs.Added directly, which is empty on the second
// FolderSync call. Fixed by caching the cumulative list in bbolt
// (lib/store FolderCache); now the second call returns whatever the
// server has reported plus everything the cache already knew about.
//
// Cleanup folder_delete is still best-effort: Z-Push BackendCombined
// returns HTTP 500 on FolderDelete for caller-created top-level
// folders (testenv Dovecot ACL surface, hstern/go-activesync#3).
func TestScenario_FolderHierarchyChange(t *testing.T) {
	cs := e2eClient(t)

	// Initial sync — primes the FolderSync key. We don't need the
	// returned folders, just the side effect of advancing the cursor.
	var before server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "test"}, &before)

	name := fmt.Sprintf("scenario-fhc-%d", time.Now().UnixNano())
	var created server.FolderCreateOutput
	callTool(t, cs, "folder_create", server.FolderCreateInput{
		Account: "test", ParentID: "0", DisplayName: name, Type: "email",
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "folder_delete", server.FolderDeleteInput{
			Account: "test", ID: created.ID,
		})
	})

	// Z-Push's IMAP backend may not register the folder with FolderSync
	// on the very next call; poll across deltas.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var after server.EmailListFoldersOutput
		callTool(t, cs, "email_list_folders",
			server.EmailListFoldersInput{Account: "test"}, &after)
		for _, f := range after.Folders {
			if f.DisplayName == name {
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Errorf("new folder %q never surfaced in email_list_folders within 20s",
		name)
}

// TestScenario_SearchThenGet: search for content, then fetch the body
// of the result. Validates the search → ID → get chain that an agent
// takes when asked "find me X and tell me what it says".
//
// On Z-Push testenv, the ServerID returned by Search isn't always
// directly addressable via ItemOperations/Fetch (the backends use
// different ID namespaces); we tolerate that and fall back to the
// Sync-side ID we already have for the verification leg, since the
// chain that matters for this test is search-finds-it.
func TestScenario_SearchThenGet(t *testing.T) {
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)

	marker := fmt.Sprintf("ZZSCENARIO%dZZ", time.Now().UnixNano())
	subject := sendLoopbackEmail(t, cs, "body containing the marker "+marker)
	row := waitForMessageInInbox(t, cs, inbox, subject)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "email_delete", server.EmailDeleteInput{
			Account: "test", FolderID: inbox, ID: row.ID,
		})
	})

	// Z-Push EAS Search may need a moment to index the new message.
	deadline := time.Now().Add(20 * time.Second)
	var found *server.EmailRow
	for time.Now().Before(deadline) {
		var hits server.EmailSearchOutput
		callTool(t, cs, "email_search", server.EmailSearchInput{
			Account: "test", Query: marker, Limit: 5,
		}, &hits)
		for i := range hits.Items {
			if hits.Items[i].Subject == subject {
				found = &hits.Items[i]
				break
			}
		}
		if found != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if found == nil {
		t.Skip("Z-Push EAS Search didn't surface the new message within 20s — testenv index lag")
	}

	// Try email_get with the search-returned ID first; if that fails
	// with InvalidArguments (testenv ID-namespace mismatch), fall back
	// to the Sync-side ID we already captured from waitForMessageInInbox.
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "email_get",
		Arguments: server.EmailGetInput{
			Account: "test", FolderID: inbox, ID: found.ID, Format: "plain",
		},
	})
	if err != nil {
		t.Fatalf("email_get transport error: %v", err)
	}
	if res.IsError {
		t.Logf("email_get on search-returned ID failed (Z-Push testenv ID namespace; hstern/go-activesync#3): %s",
			formatContent(res.Content))
		callTool(t, cs, "email_get", server.EmailGetInput{
			Account: "test", FolderID: inbox, ID: row.ID, Format: "plain",
		}, &server.EmailGetOutput{})
		return
	}
	var got server.EmailGetOutput
	decodeStructured(t, res, &got)
	if got.Subject != subject {
		t.Errorf("Subject mismatch on the search→get chain: %q vs %q",
			got.Subject, subject)
	}
}
