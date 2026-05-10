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

// findCalendarFolder lists calendar folders and returns the default
// calendar's id (the first non-tasks calendar surfaced by Z-Push's
// BackendCalDAV).
func findCalendarFolder(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	var out server.CalendarListFoldersOutput
	callTool(t, cs, "calendar_list_folders",
		server.CalendarListFoldersInput{Account: "test"}, &out)
	for _, f := range out.Folders {
		if f.Type == "Calendar" {
			return f.ID
		}
	}
	t.Fatalf("no Calendar folder in: %+v", out.Folders)
	return ""
}

func TestE2E_CalendarListFolders(t *testing.T) {
	cs := e2eClient(t)
	var out server.CalendarListFoldersOutput
	callTool(t, cs, "calendar_list_folders",
		server.CalendarListFoldersInput{Account: "test"}, &out)
	if len(out.Folders) == 0 {
		t.Fatal("expected at least one Calendar folder")
	}
}

func TestE2E_CalendarListEvents(t *testing.T) {
	cs := e2eClient(t)
	cal := findCalendarFolder(t, cs)
	var out server.CalendarListEventsOutput
	callTool(t, cs, "calendar_list_events", server.CalendarListEventsInput{
		Account: "test", FolderID: cal, WindowSize: 5, DateWindow: "1m",
	}, &out)
	if out.SyncCursor == "" {
		t.Error("SyncCursor should be populated after list")
	}
}

func TestE2E_CalendarCreateEvent(t *testing.T) {
	cs := e2eClient(t)
	cal := findCalendarFolder(t, cs)
	start := time.Now().Add(24 * time.Hour).Truncate(time.Hour).UTC()
	subject := fmt.Sprintf("e2e-create-%d", time.Now().UnixNano())

	var out server.CalendarCreateEventOutput
	callTool(t, cs, "calendar_create_event", server.CalendarCreateEventInput{
		Account: "test", FolderID: cal,
		Subject:    subject,
		StartTime:  start,
		EndTime:    start.Add(time.Hour),
		BusyStatus: 2,
	}, &out)
	if out.ID == "" {
		t.Error("created event id is empty")
	}
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "calendar_delete_event", server.CalendarDeleteEventInput{
			Account: "test", FolderID: cal, ID: out.ID,
		})
	})
}

func TestE2E_CalendarUpdateEvent(t *testing.T) {
	cs := e2eClient(t)
	cal := findCalendarFolder(t, cs)
	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour).UTC()
	subject := fmt.Sprintf("e2e-update-%d", time.Now().UnixNano())

	// Create then update.
	var created server.CalendarCreateEventOutput
	callTool(t, cs, "calendar_create_event", server.CalendarCreateEventInput{
		Account: "test", FolderID: cal,
		Subject:   subject,
		StartTime: start, EndTime: start.Add(time.Hour),
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "calendar_delete_event", server.CalendarDeleteEventInput{
			Account: "test", FolderID: cal, ID: created.ID,
		})
	})

	callTool(t, cs, "calendar_update_event", server.CalendarUpdateEventInput{
		Account: "test", FolderID: cal, ID: created.ID,
		Subject:   subject + " (updated)",
		Location:  "updated location",
		StartTime: start, EndTime: start.Add(2 * time.Hour),
	}, nil)
}

func TestE2E_CalendarDeleteEvent(t *testing.T) {
	cs := e2eClient(t)
	cal := findCalendarFolder(t, cs)
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour).UTC()
	var created server.CalendarCreateEventOutput
	callTool(t, cs, "calendar_create_event", server.CalendarCreateEventInput{
		Account: "test", FolderID: cal,
		Subject:   fmt.Sprintf("e2e-delete-%d", time.Now().UnixNano()),
		StartTime: start, EndTime: start.Add(time.Hour),
	}, &created)
	// The delete is the test itself; no t.Cleanup here.
	callTool(t, cs, "calendar_delete_event", server.CalendarDeleteEventInput{
		Account: "test", FolderID: cal, ID: created.ID,
	}, nil)
}

func TestE2E_CalendarRespondInvite(t *testing.T) {
	// We don't have a real meeting invitation in the test inbox to
	// respond to (Z-Push BackendCalDAV doesn't generate one for our
	// own create_event call). The tool's per-request validation +
	// schema check is what we're proving here — pass an obvious
	// not-an-invite id and assert IsError comes back without crashing
	// the server.
	cs := e2eClient(t)
	inbox := findInboxID(t, cs)
	res := callToolExpectError(t, cs, "calendar_respond_invite", server.CalendarRespondInviteInput{
		Account: "test", FolderID: inbox, ID: "not-an-invite-id", Response: "accept",
	})
	_ = res // having reached IsError without panic is the contract
}
