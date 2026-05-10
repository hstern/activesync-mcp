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

// ---- contacts ----------------------------------------------------------

func findContactsFolder(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	var out server.ContactsListFoldersOutput
	callTool(t, cs, "contacts_list_folders",
		server.ContactsListFoldersInput{Account: "test"}, &out)
	if len(out.Folders) == 0 {
		t.Fatal("no contacts folders")
	}
	return out.Folders[0].ID
}

func TestE2E_ContactsListFolders(t *testing.T) {
	cs := e2eClient(t)
	var out server.ContactsListFoldersOutput
	callTool(t, cs, "contacts_list_folders",
		server.ContactsListFoldersInput{Account: "test"}, &out)
	if len(out.Folders) == 0 {
		t.Error("expected at least one contacts folder")
	}
}

func TestE2E_ContactsList(t *testing.T) {
	cs := e2eClient(t)
	folder := findContactsFolder(t, cs)
	var out server.ContactsListOutput
	callTool(t, cs, "contacts_list",
		server.ContactsListInput{Account: "test", FolderID: folder}, &out)
	if out.SyncCursor == "" {
		t.Error("SyncCursor should be populated")
	}
}

func TestE2E_ContactsCreate(t *testing.T) {
	cs := e2eClient(t)
	folder := findContactsFolder(t, cs)
	first := fmt.Sprintf("E2E-%d", time.Now().UnixNano())

	var out server.IDOutput
	callTool(t, cs, "contacts_create", server.ContactsCreateInput{
		Account: "test", FolderID: folder,
		FirstName: first, LastName: "Tester",
		Email1: "tester@e2e.example",
	}, &out)
	if out.ID == "" {
		t.Error("created contact id is empty")
	}
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "contacts_delete", server.ContactsDeleteInput{
			Account: "test", FolderID: folder, ID: out.ID,
		})
	})
}

func TestE2E_ContactsUpdate(t *testing.T) {
	cs := e2eClient(t)
	folder := findContactsFolder(t, cs)
	var created server.IDOutput
	callTool(t, cs, "contacts_create", server.ContactsCreateInput{
		Account: "test", FolderID: folder,
		FirstName: fmt.Sprintf("Upd-%d", time.Now().UnixNano()),
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "contacts_delete", server.ContactsDeleteInput{
			Account: "test", FolderID: folder, ID: created.ID,
		})
	})

	callTool(t, cs, "contacts_update", server.ContactsUpdateInput{
		ContactsCreateInput: server.ContactsCreateInput{
			Account: "test", FolderID: folder,
			FirstName: "Updated", LastName: "Name",
		},
		ID: created.ID,
	}, nil)
}

func TestE2E_ContactsDelete(t *testing.T) {
	cs := e2eClient(t)
	folder := findContactsFolder(t, cs)
	var created server.IDOutput
	callTool(t, cs, "contacts_create", server.ContactsCreateInput{
		Account: "test", FolderID: folder,
		FirstName: fmt.Sprintf("Del-%d", time.Now().UnixNano()),
	}, &created)
	// Delete is the test itself.
	callTool(t, cs, "contacts_delete", server.ContactsDeleteInput{
		Account: "test", FolderID: folder, ID: created.ID,
	}, nil)
}

// ---- tasks -------------------------------------------------------------

func findTasksFolder(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	var out server.TasksListFoldersOutput
	callTool(t, cs, "tasks_list_folders",
		server.TasksListFoldersInput{Account: "test"}, &out)
	if len(out.Folders) == 0 {
		t.Skip("no tasks folder (testenv routes tasks via BackendCalDAV; needs the T-prefix companion folder)")
	}
	return out.Folders[0].ID
}

func TestE2E_TasksListFolders(t *testing.T) {
	cs := e2eClient(t)
	var out server.TasksListFoldersOutput
	callTool(t, cs, "tasks_list_folders",
		server.TasksListFoldersInput{Account: "test"}, &out)
	if len(out.Folders) == 0 {
		t.Skip("no tasks folder (BackendCalDAV configuration)")
	}
}

func TestE2E_TasksList(t *testing.T) {
	cs := e2eClient(t)
	folder := findTasksFolder(t, cs)
	var out server.TasksListOutput
	callTool(t, cs, "tasks_list",
		server.TasksListInput{Account: "test", FolderID: folder}, &out)
	if out.SyncCursor == "" {
		t.Error("SyncCursor should be populated")
	}
}

func TestE2E_TasksCreate(t *testing.T) {
	cs := e2eClient(t)
	folder := findTasksFolder(t, cs)
	subject := fmt.Sprintf("e2e-task-%d", time.Now().UnixNano())
	due := time.Now().Add(48 * time.Hour).UTC()

	var out server.IDOutput
	callTool(t, cs, "tasks_create", server.TasksCreateInput{
		Account: "test", FolderID: folder,
		Subject: subject, DueDate: due, Importance: 1,
	}, &out)
	if out.ID == "" {
		t.Error("created task id empty")
	}
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "tasks_delete", server.TasksDeleteInput{
			Account: "test", FolderID: folder, ID: out.ID,
		})
	})
}

func TestE2E_TasksUpdate(t *testing.T) {
	cs := e2eClient(t)
	folder := findTasksFolder(t, cs)
	var created server.IDOutput
	callTool(t, cs, "tasks_create", server.TasksCreateInput{
		Account: "test", FolderID: folder,
		Subject: fmt.Sprintf("upd-%d", time.Now().UnixNano()),
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "tasks_delete", server.TasksDeleteInput{
			Account: "test", FolderID: folder, ID: created.ID,
		})
	})
	callTool(t, cs, "tasks_update", server.TasksUpdateInput{
		TasksCreateInput: server.TasksCreateInput{
			Account: "test", FolderID: folder,
			Subject: "updated subject", Importance: 2,
		},
		ID: created.ID,
	}, nil)
}

func TestE2E_TasksComplete(t *testing.T) {
	cs := e2eClient(t)
	folder := findTasksFolder(t, cs)
	var created server.IDOutput
	callTool(t, cs, "tasks_create", server.TasksCreateInput{
		Account: "test", FolderID: folder,
		Subject: fmt.Sprintf("complete-%d", time.Now().UnixNano()),
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "tasks_delete", server.TasksDeleteInput{
			Account: "test", FolderID: folder, ID: created.ID,
		})
	})
	callTool(t, cs, "tasks_complete", server.TasksCompleteInput{
		Account: "test", FolderID: folder, ID: created.ID,
	}, nil)
}

func TestE2E_TasksDelete(t *testing.T) {
	cs := e2eClient(t)
	folder := findTasksFolder(t, cs)
	var created server.IDOutput
	callTool(t, cs, "tasks_create", server.TasksCreateInput{
		Account: "test", FolderID: folder,
		Subject: fmt.Sprintf("del-%d", time.Now().UnixNano()),
	}, &created)
	callTool(t, cs, "tasks_delete", server.TasksDeleteInput{
		Account: "test", FolderID: folder, ID: created.ID,
	}, nil)
}

// ---- notes -------------------------------------------------------------

func findNotesFolder(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	var out server.NotesListFoldersOutput
	callTool(t, cs, "notes_list_folders",
		server.NotesListFoldersInput{Account: "test"}, &out)
	if len(out.Folders) == 0 {
		t.Skip("no notes folder (testenv routes notes via IMAP; degenerate)")
	}
	return out.Folders[0].ID
}

func TestE2E_NotesListFolders(t *testing.T) {
	cs := e2eClient(t)
	var out server.NotesListFoldersOutput
	callTool(t, cs, "notes_list_folders",
		server.NotesListFoldersInput{Account: "test"}, &out)
	// Notes are an Outlook-only concept; testenv routes them via IMAP.
	// Empty list is the expected and correct outcome.
	_ = out
}

func TestE2E_NotesList(t *testing.T) {
	cs := e2eClient(t)
	folder := findNotesFolder(t, cs)
	var out server.NotesListOutput
	callTool(t, cs, "notes_list",
		server.NotesListInput{Account: "test", FolderID: folder}, &out)
	_ = out
}

func TestE2E_NotesCreate(t *testing.T) {
	cs := e2eClient(t)
	folder := findNotesFolder(t, cs)
	var out server.IDOutput
	callTool(t, cs, "notes_create", server.NotesCreateInput{
		Account: "test", FolderID: folder,
		Subject: fmt.Sprintf("note-%d", time.Now().UnixNano()),
		Body:    "hello from e2e",
	}, &out)
	if out.ID == "" {
		t.Error("created note id empty")
	}
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "notes_delete", server.NotesDeleteInput{
			Account: "test", FolderID: folder, ID: out.ID,
		})
	})
}

func TestE2E_NotesUpdate(t *testing.T) {
	cs := e2eClient(t)
	folder := findNotesFolder(t, cs)
	var created server.IDOutput
	callTool(t, cs, "notes_create", server.NotesCreateInput{
		Account: "test", FolderID: folder, Body: "before",
	}, &created)
	t.Cleanup(func() {
		_ = mustCallToolBool(t, cs, "notes_delete", server.NotesDeleteInput{
			Account: "test", FolderID: folder, ID: created.ID,
		})
	})
	callTool(t, cs, "notes_update", server.NotesUpdateInput{
		NotesCreateInput: server.NotesCreateInput{
			Account: "test", FolderID: folder, Body: "after",
		},
		ID: created.ID,
	}, nil)
}

func TestE2E_NotesDelete(t *testing.T) {
	cs := e2eClient(t)
	folder := findNotesFolder(t, cs)
	var created server.IDOutput
	callTool(t, cs, "notes_create", server.NotesCreateInput{
		Account: "test", FolderID: folder, Body: "to delete",
	}, &created)
	callTool(t, cs, "notes_delete", server.NotesDeleteInput{
		Account: "test", FolderID: folder, ID: created.ID,
	}, nil)
}
