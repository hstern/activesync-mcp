// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/wbxml"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pimFakeServer responds to the EAS commands that the contacts /
// tasks / notes / GAL tools issue: Settings, Provision, FolderSync
// (returns one folder of each PIM class), Sync (returns one item per
// class, dispatched by CollectionId), and Search (returns one GAL
// entry).
type pimFakeServer struct {
	mu        sync.Mutex
	provCalls int
}

func (f *pimFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	switch r.URL.Query().Get("Cmd") {
	case "Settings":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "Provision":
		f.mu.Lock()
		f.provCalls++
		call := f.provCalls
		f.mu.Unlock()
		key := "TKEY"
		if call%2 == 0 {
			key = "FKEY"
		}
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageProvision, "Policies",
					wbxml.E(wbxml.PageProvision, "Policy",
						wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
						wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text(key)),
					),
				),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "FolderSync":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS")),
				wbxml.E(wbxml.PageFolderHierarchy, "Changes",
					wbxml.E(wbxml.PageFolderHierarchy, "Count", wbxml.Text("3")),
					pimFolder("contacts-id", "Contacts", 9),
					pimFolder("tasks-id", "Tasks", 7),
					pimFolder("notes-id", "Notes", 10),
				),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "Sync":
		f.handleSync(w, r)
	case "Search":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageSearch, "Search",
				wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSearch, "Response",
					wbxml.E(wbxml.PageSearch, "Store",
						wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageSearch, "Total", wbxml.Text("1")),
						wbxml.E(wbxml.PageSearch, "Range", wbxml.Text("0-0")),
						wbxml.E(wbxml.PageSearch, "Result",
							wbxml.E(wbxml.PageSearch, "Properties",
								wbxml.E(wbxml.PageGAL, "DisplayName", wbxml.Text("Alice E.")),
								wbxml.E(wbxml.PageGAL, "EmailAddress", wbxml.Text("alice@x")),
								wbxml.E(wbxml.PageGAL, "Title", wbxml.Text("Engineer")),
								wbxml.E(wbxml.PageGAL, "Office", wbxml.Text("HQ")),
								wbxml.E(wbxml.PageGAL, "Company", wbxml.Text("Acme")),
								wbxml.E(wbxml.PageGAL, "Phone", wbxml.Text("555-1212")),
								wbxml.E(wbxml.PageGAL, "MobilePhone", wbxml.Text("555-3434")),
							),
						),
					),
				),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	default:
		http.Error(w, "unhandled "+r.URL.Query().Get("Cmd"), 400)
	}
}

func pimFolder(id, name string, ftype int) *wbxml.Element {
	return wbxml.E(wbxml.PageFolderHierarchy, "Add",
		wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text(id)),
		wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text("0")),
		wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text(name)),
		wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text(strconv.Itoa(ftype))),
	)
}

// handleSync inspects the request body to figure out which collection
// the client is syncing, and returns a class-appropriate response.
func (f *pimFakeServer) handleSync(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	doc, err := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
	if err != nil || doc.Root == nil {
		http.Error(w, "bad sync body", 400)
		return
	}
	collID := ""
	if c := doc.Root.Find("CollectionId"); c != nil {
		collID = c.TextContent()
	}
	var add *wbxml.Element
	switch collID {
	case "contacts-id":
		add = wbxml.E(wbxml.PageAirSync, "Add",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("contacts-id:1")),
			wbxml.E(wbxml.PageAirSync, "ApplicationData",
				wbxml.E(wbxml.PageContacts, "FirstName", wbxml.Text("Alice")),
				wbxml.E(wbxml.PageContacts, "LastName", wbxml.Text("Example")),
				wbxml.E(wbxml.PageContacts, "JobTitle", wbxml.Text("Engineer")),
				wbxml.E(wbxml.PageContacts, "Email1Address", wbxml.Text("alice@x")),
				wbxml.E(wbxml.PageContacts, "MobilePhoneNumber", wbxml.Text("555-3434")),
			),
		)
	case "tasks-id":
		add = wbxml.E(wbxml.PageAirSync, "Add",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("tasks-id:1")),
			wbxml.E(wbxml.PageAirSync, "ApplicationData",
				wbxml.E(wbxml.PageTasks, "Subject", wbxml.Text("Write report")),
				wbxml.E(wbxml.PageTasks, "Importance", wbxml.Text("2")),
				wbxml.E(wbxml.PageTasks, "Complete", wbxml.Text("0")),
				wbxml.E(wbxml.PageTasks, "DueDate", wbxml.Text("2026-06-01T17:00:00.000Z")),
			),
		)
	case "notes-id":
		add = wbxml.E(wbxml.PageAirSync, "Add",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("notes-id:1")),
			wbxml.E(wbxml.PageAirSync, "ApplicationData",
				wbxml.E(wbxml.PageNotes, "Subject", wbxml.Text("groceries")),
				wbxml.E(wbxml.PageNotes, "LastModifiedDate", wbxml.Text("2026-05-09T12:00:00.000Z")),
				wbxml.E(wbxml.PageNotes, "Categories",
					wbxml.E(wbxml.PageNotes, "Category", wbxml.Text("home")),
				),
			),
		)
	default:
		// Unknown collection — return Status=1 with no commands.
	}

	coll := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("S1")),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(collID)),
		wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
	)
	if add != nil {
		coll.Children = append(coll.Children,
			wbxml.E(wbxml.PageAirSync, "Commands", add))
	}
	body, _ = wbxml.Marshal(&wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", coll),
		),
	}, wbxml.DefaultRegistry())
	w.Write(body)
}

func pimManager(t *testing.T, srv *httptest.Server) *Manager {
	t.Helper()
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Access: map[string]string{
				"contacts": config.AccessRW,
				"tasks":    config.AccessRW,
				"notes":    config.AccessRW,
			},
			Secret: config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	return NewManager(cfg, &fakeStateProvider{}, res, staticDeviceIDs{"alpha": "dev"})
}

func TestPIM_ContactsListFolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&pimFakeServer{}).handle))
	defer srv.Close()
	m := pimManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "contacts_list_folders",
		ContactsListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["display_name"] != "Contacts" {
		t.Errorf("folders = %v", folders)
	}
}

func TestPIM_TasksListFolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&pimFakeServer{}).handle))
	defer srv.Close()
	m := pimManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_list_folders",
		TasksListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["display_name"] != "Tasks" {
		t.Errorf("folders = %v", folders)
	}
}

func TestPIM_NotesListFolders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&pimFakeServer{}).handle))
	defer srv.Close()
	m := pimManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "notes_list_folders",
		NotesListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 || folders[0].(map[string]any)["display_name"] != "Notes" {
		t.Errorf("folders = %v", folders)
	}
}

func TestPIM_ContactsList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&pimFakeServer{}).handle))
	defer srv.Close()
	m := pimManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "contacts_list",
		ContactsListInput{Account: "alpha", FolderID: "contacts-id"})
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
	srv := httptest.NewServer(http.HandlerFunc((&pimFakeServer{}).handle))
	defer srv.Close()
	m := pimManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "tasks_list",
		TasksListInput{Account: "alpha", FolderID: "tasks-id"})
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
	srv := httptest.NewServer(http.HandlerFunc((&pimFakeServer{}).handle))
	defer srv.Close()
	m := pimManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "notes_list",
		NotesListInput{Account: "alpha", FolderID: "notes-id"})
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
	srv := httptest.NewServer(http.HandlerFunc((&pimFakeServer{}).handle))
	defer srv.Close()
	m := pimManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerPIMTools(s, m.cfg, m)

	out := callTool(t, s, "gal_search",
		GALSearchInput{Account: "alpha", Query: "alice"})
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

func TestPIM_RegisterPIMTools_skipsOnEmptyAccounts(t *testing.T) {
	cfg := &config.Config{}
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	// Should be a no-op — no accounts means nothing to register.
	registerPIMTools(s, cfg, nil)
}

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
	// scopeEnum appends the allowed-accounts hint to the description.
	if !strings.Contains(tool.Description, "alpha") || !strings.Contains(tool.Description, "beta") {
		t.Errorf("Description should mention both accounts, got: %q", tool.Description)
	}
}
