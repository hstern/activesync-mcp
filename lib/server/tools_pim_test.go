// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"activesync-mcp/lib/config"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pimFolders is the canned FolderSync output reused by PIM list tests:
// one folder of each PIM class.
var pimFolders = &eas.FolderSyncResult{
	SyncKey: "FS",
	Added: []eas.Folder{
		{ServerID: "contacts-id", DisplayName: "Contacts", Type: eas.FolderTypeContacts},
		{ServerID: "tasks-id", DisplayName: "Tasks", Type: eas.FolderTypeTasks},
		{ServerID: "notes-id", DisplayName: "Notes", Type: eas.FolderTypeNotes},
	},
}

// pimMockManager wires a Manager with per-class RW so all PIM classes
// (contacts, tasks, notes) accept writes.
func pimMockManager(t *testing.T, c eas.Client) *Manager {
	return newMockManager(t, c, mockManagerOpts{
		access: config.AccessRO,
		classes: map[string]string{
			"contacts": config.AccessRW,
			"tasks":    config.AccessRW,
			"notes":    config.AccessRW,
		},
	})
}

func TestPIM_ContactsListFolders(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) { return pimFolders, nil },
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "contacts_list_folders", ContactsListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["display_name"] != "Contacts" {
		t.Errorf("folders = %v", folders)
	}
}

func TestPIM_TasksListFolders(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) { return pimFolders, nil },
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_list_folders", TasksListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["display_name"] != "Tasks" {
		t.Errorf("folders = %v", folders)
	}
}

func TestPIM_NotesListFolders(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) { return pimFolders, nil },
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "notes_list_folders", NotesListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["display_name"] != "Notes" {
		t.Errorf("folders = %v", folders)
	}
}

func TestPIM_ContactsList(t *testing.T) {
	mock := &easmock.Client{
		ContactsClient: easmock.ContactsClient{
			SyncContactsFunc: func(_ context.Context, fid string) (*eas.ContactsSyncResult, error) {
				return &eas.ContactsSyncResult{
					SyncKey: "S1",
					Added: []eas.ContactItem{{
						ServerID:      "contacts-id:1",
						FirstName:     "Alice",
						LastName:      "Example",
						JobTitle:      "Engineer",
						Email1Address: "alice@x",
						MobilePhone:   "555-3434",
					}},
				}, nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "contacts_list", ContactsListInput{Account: "alpha", FolderID: "contacts-id"})
	rows := out["contacts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("len(contacts) = %d", len(rows))
	}
	r := rows[0].(map[string]any)
	if r["first_name"] != "Alice" || r["email1"] != "alice@x" {
		t.Errorf("row = %v", r)
	}
}

func TestPIM_TasksList(t *testing.T) {
	due := time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC)
	mock := &easmock.Client{
		TasksClient: easmock.TasksClient{
			SyncTasksFunc: func(_ context.Context, fid string) (*eas.TasksSyncResult, error) {
				return &eas.TasksSyncResult{
					SyncKey: "S1",
					Added: []eas.TaskItem{{
						ServerID:   "tasks-id:1",
						Subject:    "Write report",
						Importance: 2,
						Complete:   false,
						DueDate:    due,
					}},
				}, nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_list", TasksListInput{Account: "alpha", FolderID: "tasks-id"})
	rows := out["tasks"].([]any)
	if len(rows) != 1 {
		t.Fatalf("len(tasks) = %d", len(rows))
	}
	r := rows[0].(map[string]any)
	if r["subject"] != "Write report" || r["complete"] != false {
		t.Errorf("row = %v", r)
	}
}

func TestPIM_NotesList(t *testing.T) {
	mod := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	mock := &easmock.Client{
		NotesClient: easmock.NotesClient{
			SyncNotesFunc: func(_ context.Context, fid string) (*eas.NotesSyncResult, error) {
				return &eas.NotesSyncResult{
					SyncKey: "S1",
					Added: []eas.NoteItem{{
						ServerID:         "notes-id:1",
						Subject:          "groceries",
						LastModifiedDate: mod,
						Categories:       []string{"home"},
					}},
				}, nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "notes_list", NotesListInput{Account: "alpha", FolderID: "notes-id"})
	rows := out["notes"].([]any)
	if len(rows) != 1 {
		t.Fatalf("len(notes) = %d", len(rows))
	}
	r := rows[0].(map[string]any)
	if r["subject"] != "groceries" {
		t.Errorf("row = %v", r)
	}
	cats := r["categories"].([]any)
	if len(cats) != 1 || cats[0] != "home" {
		t.Errorf("categories = %v", cats)
	}
}

func TestPIM_GALSearch(t *testing.T) {
	mock := &easmock.Client{
		SearchClient: easmock.SearchClient{
			GALSearchFunc: func(_ context.Context, q string, _ int) (*eas.GALSearchResult, error) {
				return &eas.GALSearchResult{
					Total: 1,
					Range: "0-0",
					Entries: []eas.GALEntry{{
						DisplayName:  "Alice E.",
						EmailAddress: "alice@x",
						Title:        "Engineer",
						Office:       "HQ",
						Company:      "Acme",
						Phone:        "555-1212",
						MobilePhone:  "555-3434",
					}},
				}, nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "gal_search", GALSearchInput{Account: "alpha", Query: "alice"})
	if int(out["total"].(float64)) != 1 {
		t.Errorf("total = %v", out["total"])
	}
	entries := out["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d", len(entries))
	}
	e := entries[0].(map[string]any)
	if e["display_name"] != "Alice E." || e["email_address"] != "alice@x" {
		t.Errorf("entry = %v", e)
	}
}

func TestPIM_ContactsCreate(t *testing.T) {
	mock := &easmock.Client{
		ContactsClient: easmock.ContactsClient{
			CreateContactFunc: func(_ context.Context, fid string, d eas.ContactDraft) (string, error) {
				if d.FirstName != "Alice" {
					t.Errorf("draft = %+v", d)
				}
				return "contacts-id:new", nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "contacts_create", ContactsCreateInput{
		Account: "alpha", FolderID: "contacts-id",
		FirstName: "Alice", LastName: "Example", Email1: "alice@x",
	})
	if out["id"] != "contacts-id:new" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_ContactsUpdate(t *testing.T) {
	mock := &easmock.Client{
		ContactsClient: easmock.ContactsClient{
			UpdateContactFunc: func(_ context.Context, fid, sid string, _ eas.ContactDraft) error {
				if sid != "contacts-id:1" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "contacts_update", ContactsUpdateInput{
		ID: "contacts-id:1",
		ContactsCreateInput: ContactsCreateInput{
			Account: "alpha", FolderID: "contacts-id",
			FirstName: "Alice", LastName: "Updated",
		},
	})
	if out["id"] != "contacts-id:1" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_ContactsDelete(t *testing.T) {
	mock := &easmock.Client{
		ContactsClient: easmock.ContactsClient{
			DeleteContactFunc: func(_ context.Context, fid, sid string) error {
				if sid != "contacts-id:1" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "contacts_delete", ContactsDeleteInput{
		Account: "alpha", FolderID: "contacts-id", ID: "contacts-id:1",
	})
	if out["id"] != "contacts-id:1" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_TasksCreate(t *testing.T) {
	mock := &easmock.Client{
		TasksClient: easmock.TasksClient{
			CreateTaskFunc: func(_ context.Context, fid string, _ eas.TaskDraft) (string, error) {
				return "tasks-id:new", nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_create", TasksCreateInput{
		Account: "alpha", FolderID: "tasks-id",
		Subject: "Write report",
	})
	if out["id"] != "tasks-id:new" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_TasksUpdate(t *testing.T) {
	mock := &easmock.Client{
		TasksClient: easmock.TasksClient{
			UpdateTaskFunc: func(_ context.Context, fid, sid string, _ eas.TaskDraft) error {
				if sid != "tasks-id:1" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_update", TasksUpdateInput{
		ID: "tasks-id:1",
		TasksCreateInput: TasksCreateInput{
			Account: "alpha", FolderID: "tasks-id", Subject: "Updated",
		},
	})
	if out["id"] != "tasks-id:1" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_TasksComplete(t *testing.T) {
	mock := &easmock.Client{
		TasksClient: easmock.TasksClient{
			CompleteTaskFunc: func(_ context.Context, fid, sid string) error {
				if sid != "tasks-id:1" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_complete", TasksCompleteInput{
		Account: "alpha", FolderID: "tasks-id", ID: "tasks-id:1",
	})
	if out["id"] != "tasks-id:1" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_TasksDelete(t *testing.T) {
	mock := &easmock.Client{
		TasksClient: easmock.TasksClient{
			DeleteTaskFunc: func(_ context.Context, fid, sid string) error {
				if sid != "tasks-id:1" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_delete", TasksDeleteInput{
		Account: "alpha", FolderID: "tasks-id", ID: "tasks-id:1",
	})
	if out["id"] != "tasks-id:1" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_NotesCreate(t *testing.T) {
	mock := &easmock.Client{
		NotesClient: easmock.NotesClient{
			CreateNoteFunc: func(_ context.Context, fid string, _ eas.NoteDraft) (string, error) {
				return "notes-id:new", nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "notes_create", NotesCreateInput{
		Account: "alpha", FolderID: "notes-id",
		Subject: "n", Body: "b",
	})
	if out["id"] != "notes-id:new" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_NotesUpdate(t *testing.T) {
	mock := &easmock.Client{
		NotesClient: easmock.NotesClient{
			UpdateNoteFunc: func(_ context.Context, fid, sid string, _ eas.NoteDraft) error {
				if sid != "notes-id:1" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "notes_update", NotesUpdateInput{
		ID: "notes-id:1",
		NotesCreateInput: NotesCreateInput{
			Account: "alpha", FolderID: "notes-id",
			Subject: "n", Body: "b",
		},
	})
	if out["id"] != "notes-id:1" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_NotesDelete(t *testing.T) {
	mock := &easmock.Client{
		NotesClient: easmock.NotesClient{
			DeleteNoteFunc: func(_ context.Context, fid, sid string) error {
				if sid != "notes-id:1" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "notes_delete", NotesDeleteInput{
		Account: "alpha", FolderID: "notes-id", ID: "notes-id:1",
	})
	if out["id"] != "notes-id:1" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestPIM_GALSearch_emptyQueryRejected(t *testing.T) {
	mock := &easmock.Client{} // sentinel — should not be hit
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := c.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "gal_search",
		Arguments: GALSearchInput{Account: "alpha", Query: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("want IsError for empty query")
	}
}

func TestPIM_RegisterPIMTools_skipsOnEmptyAccounts(t *testing.T) {
	cfg := &config.Config{}
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, cfg, nil)
}

// Pure-Go helpers — no EAS layer.

func TestContactRowFrom(t *testing.T) {
	got := contactRowFrom(eas.ContactItem{
		ServerID: "id-1", FirstName: "Alice", LastName: "Example",
		CompanyName: "Acme", JobTitle: "Engineer",
		Email1Address: "a@x", Email2Address: "b@x",
		HomePhone: "h", BusinessPhone: "b", MobilePhone: "m",
	})
	if got.ID != "id-1" || got.Email1 != "a@x" || got.Email2 != "b@x" {
		t.Errorf("got = %+v", got)
	}
	if got.MobilePhone != "m" || got.BusinessPhone != "b" || got.HomePhone != "h" {
		t.Errorf("phones not mapped: %+v", got)
	}
}

func TestDraftFromContactInput(t *testing.T) {
	in := ContactsCreateInput{
		FirstName: "Alice", LastName: "Example",
		CompanyName: "Acme", JobTitle: "Engineer",
		Email1: "a@x", Email2: "b@x",
		HomePhone: "h", BusinessPhone: "b", MobilePhone: "m",
	}
	d := draftFromContactInput(in)
	if d.FirstName != "Alice" || d.Email1Address != "a@x" || d.MobilePhone != "m" {
		t.Errorf("draft = %+v", d)
	}
}

func TestTaskRowFrom(t *testing.T) {
	due := time.Date(2026, 6, 1, 17, 0, 0, 0, time.UTC)
	got := taskRowFrom(eas.TaskItem{
		ServerID: "t-1", Subject: "Write report",
		Complete: false, DueDate: due, Importance: 2,
	})
	if got.ID != "t-1" || got.Subject != "Write report" || !got.DueDate.Equal(due) {
		t.Errorf("got = %+v", got)
	}
	if got.Importance != 2 || got.Complete {
		t.Errorf("flags wrong: %+v", got)
	}
}

func TestDraftFromTaskInput(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	due := start.Add(8 * time.Hour)
	in := TasksCreateInput{
		Subject: "Write report", Body: "with the analysis",
		StartDate: start, DueDate: due, Importance: 2,
	}
	d := draftFromTaskInput(in)
	if d.Subject != in.Subject || !d.DueDate.Equal(due) || d.Importance != 2 {
		t.Errorf("draft = %+v", d)
	}
}

func TestNoteRowFrom(t *testing.T) {
	mod := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	got := noteRowFrom(eas.NoteItem{
		ServerID: "n-1", Subject: "groceries", Body: "milk, eggs",
		LastModifiedDate: mod, Categories: []string{"home"},
	})
	if got.ID != "n-1" || got.Subject != "groceries" || got.Body != "milk, eggs" {
		t.Errorf("got = %+v", got)
	}
	if !got.LastModified.Equal(mod) || len(got.Categories) != 1 {
		t.Errorf("metadata wrong: %+v", got)
	}
}

func TestDraftFromNoteInput(t *testing.T) {
	for _, c := range []struct {
		format string
		want   eas.BodyType
	}{
		{"", eas.BodyTypePlain},
		{"plain", eas.BodyTypePlain},
		{"html", eas.BodyTypeHTML},
	} {
		d := draftFromNoteInput(NotesCreateInput{
			Subject: "S", Body: "B", BodyFormat: c.format,
			Categories: []string{"x"},
		})
		if d.BodyType != c.want {
			t.Errorf("format=%q: BodyType = %v, want %v", c.format, d.BodyType, c.want)
		}
		if d.Subject != "S" || d.Body != "B" || len(d.Categories) != 1 {
			t.Errorf("draft fields wrong for %q: %+v", c.format, d)
		}
	}
}

func TestScopedTool_appliesEnumHint(t *testing.T) {
	tool := scopedTool("name", "desc", []string{"alpha", "beta"})
	if tool.Name != "name" {
		t.Errorf("Name = %q", tool.Name)
	}
	if !strings.Contains(tool.Description, "alpha") || !strings.Contains(tool.Description, "beta") {
		t.Errorf("Description should mention both accounts, got: %q", tool.Description)
	}
}

// Error-wrap tests for the PIM handlers. One read + one create per
// class is enough to catch typos in the wrap message; the rest of the
// CUD handlers all share a tiny wrap site.

func TestContactsList_wrapsError(t *testing.T) {
	mock := &easmock.Client{ContactsClient: easmock.ContactsClient{
		SyncContactsFunc: func(context.Context, string) (*eas.ContactsSyncResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)
	res := callToolErr(t, s, "contacts_list", ContactsListInput{
		Account: "alpha", FolderID: "contacts-id",
	})
	if !strings.Contains(errText(t, res), "SyncContacts") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestContactsCreate_wrapsError(t *testing.T) {
	mock := &easmock.Client{ContactsClient: easmock.ContactsClient{
		CreateContactFunc: func(context.Context, string, eas.ContactDraft) (string, error) {
			return "", errors.New("boom")
		},
	}}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)
	res := callToolErr(t, s, "contacts_create", ContactsCreateInput{
		Account: "alpha", FolderID: "contacts-id", FirstName: "x",
	})
	if !strings.Contains(errText(t, res), "CreateContact") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestTasksList_wrapsError(t *testing.T) {
	mock := &easmock.Client{TasksClient: easmock.TasksClient{
		SyncTasksFunc: func(context.Context, string) (*eas.TasksSyncResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)
	res := callToolErr(t, s, "tasks_list", TasksListInput{
		Account: "alpha", FolderID: "tasks-id",
	})
	if !strings.Contains(errText(t, res), "SyncTasks") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestTasksCreate_wrapsError(t *testing.T) {
	mock := &easmock.Client{TasksClient: easmock.TasksClient{
		CreateTaskFunc: func(context.Context, string, eas.TaskDraft) (string, error) {
			return "", errors.New("boom")
		},
	}}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)
	res := callToolErr(t, s, "tasks_create", TasksCreateInput{
		Account: "alpha", FolderID: "tasks-id", Subject: "x",
	})
	if !strings.Contains(errText(t, res), "CreateTask") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestNotesList_wrapsError(t *testing.T) {
	mock := &easmock.Client{NotesClient: easmock.NotesClient{
		SyncNotesFunc: func(context.Context, string) (*eas.NotesSyncResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)
	res := callToolErr(t, s, "notes_list", NotesListInput{
		Account: "alpha", FolderID: "notes-id",
	})
	if !strings.Contains(errText(t, res), "SyncNotes") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestNotesCreate_wrapsError(t *testing.T) {
	mock := &easmock.Client{NotesClient: easmock.NotesClient{
		CreateNoteFunc: func(context.Context, string, eas.NoteDraft) (string, error) {
			return "", errors.New("boom")
		},
	}}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)
	res := callToolErr(t, s, "notes_create", NotesCreateInput{
		Account: "alpha", FolderID: "notes-id", Body: "x",
	})
	if !strings.Contains(errText(t, res), "CreateNote") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestGALSearch_wrapsError(t *testing.T) {
	mock := &easmock.Client{SearchClient: easmock.SearchClient{
		GALSearchFunc: func(context.Context, string, int) (*eas.GALSearchResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := pimMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)
	res := callToolErr(t, s, "gal_search", GALSearchInput{Account: "alpha", Query: "x"})
	if !strings.Contains(errText(t, res), "GALSearch") {
		t.Errorf("err = %q", errText(t, res))
	}
}
