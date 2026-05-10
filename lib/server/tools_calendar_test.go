package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/wbxml"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// calendarFakeServer responds to FolderSync (returns one calendar folder),
// Sync (returns one event the second time it's called for that folder so
// the bootstrap dance has data on the second pass), and MeetingResponse.
type calendarFakeServer struct {
	mu        sync.Mutex
	provCalls int
	syncCalls int
}

func (f *calendarFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	switch r.URL.Query().Get("Cmd") {
	case "Settings":
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "Provision":
		f.mu.Lock()
		f.provCalls++
		call := f.provCalls
		f.mu.Unlock()
		key := "TKEY"
		if call == 2 {
			key = "FKEY"
		}
		doc := &wbxml.Document{
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
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "FolderSync":
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS")),
				wbxml.E(wbxml.PageFolderHierarchy, "Changes",
					wbxml.E(wbxml.PageFolderHierarchy, "Count", wbxml.Text("2")),
					wbxml.E(wbxml.PageFolderHierarchy, "Add",
						wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("inbox-id")),
						wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text("0")),
						wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Inbox")),
						wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text("2")),
					),
					wbxml.E(wbxml.PageFolderHierarchy, "Add",
						wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("cal-id")),
						wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text("0")),
						wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Calendar")),
						wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text("8")),
					),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "Sync":
		f.mu.Lock()
		f.syncCalls++
		call := f.syncCalls
		f.mu.Unlock()
		key := "C1"
		var commands *wbxml.Element
		if call == 2 {
			key = "C2"
			start := time.Date(2026, 5, 9, 14, 0, 0, 0, time.UTC)
			commands = wbxml.E(wbxml.PageAirSync, "Commands",
				wbxml.E(wbxml.PageAirSync, "Add",
					wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("cal-id:42")),
					wbxml.E(wbxml.PageAirSync, "ApplicationData",
						wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text("Quarterly review")),
						wbxml.E(wbxml.PageCalendar, "StartTime", wbxml.Text("2026-05-09T14:00:00.000Z")),
						wbxml.E(wbxml.PageCalendar, "EndTime", wbxml.Text("2026-05-09T15:00:00.000Z")),
						wbxml.E(wbxml.PageCalendar, "AllDayEvent", wbxml.Text("0")),
					),
				),
			)
			_ = start
		}
		coll := wbxml.E(wbxml.PageAirSync, "Collection",
			wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
			wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal-id")),
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
		)
		if commands != nil {
			coll.Children = append(coll.Children, commands)
		}
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Collections", coll),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "MeetingResponse":
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMeetingResponse, "MeetingResponse",
				wbxml.E(wbxml.PageMeetingResponse, "Result",
					wbxml.E(wbxml.PageMeetingResponse, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageMeetingResponse, "CalendarId", wbxml.Text("cal-id:42")),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	default:
		http.Error(w, "unhandled "+r.URL.Query().Get("Cmd"), 400)
	}
}

func calendarTestManager(t *testing.T, srv *httptest.Server) *Manager {
	t.Helper()
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Access:        map[string]string{"calendar": config.AccessRW},
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	store := &fakeStateProvider{}
	return NewManager(cfg, store, res, staticDeviceIDs{"alpha": "abc"})
}

func TestCalendarListFolders(t *testing.T) {
	f := &calendarFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := calendarTestManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_folders", CalendarListFoldersInput{Account: "alpha"})
	folders := out["folders"].([]any)
	if len(folders) != 1 {
		t.Fatalf("want 1 calendar folder, got %d", len(folders))
	}
	if folders[0].(map[string]any)["display_name"] != "Calendar" {
		t.Errorf("name = %v", folders[0])
	}
}

func TestCalendarListEvents(t *testing.T) {
	f := &calendarFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := calendarTestManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_list_events", CalendarListEventsInput{
		Account: "alpha", FolderID: "cal-id",
	})
	events := out["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("len(events) = %d", len(events))
	}
	first := events[0].(map[string]any)
	if first["subject"] != "Quarterly review" {
		t.Errorf("subject = %v", first["subject"])
	}
}

func TestCalendarRespondInvite_validation(t *testing.T) {
	f := &calendarFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := calendarTestManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	out := callTool(t, s, "calendar_respond_invite", CalendarRespondInviteInput{
		Account: "alpha", FolderID: "inbox-id", ID: "invite", Response: "accept",
	})
	if out["calendar_id"] != "cal-id:42" {
		t.Errorf("calendar_id = %v", out["calendar_id"])
	}
}

func TestParseInviteResponse(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"accept", true},
		{"Tentative", true},
		{"DECLINE", true},
		{"1", true},
		{"yes", false},
		{"", false},
	} {
		_, err := parseInviteResponse(c.in)
		if (err == nil) != c.ok {
			t.Errorf("parseInviteResponse(%q): err=%v, want ok=%v", c.in, err, c.ok)
		}
	}
}

func TestCalendarCreate_requiresStartEnd(t *testing.T) {
	f := &calendarFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := calendarTestManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	// In-process MCP call — expect IsError because StartTime is missing.
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
		Name: "calendar_create_event",
		Arguments: CalendarCreateEventInput{
			Account: "alpha", FolderID: "cal-id", Subject: "x",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("want IsError when start/end missing")
	}
}

func TestDraftFromInput_mapsAllFields(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	in := CalendarCreateEventInput{
		Subject:    "Standup",
		Location:   "Zoom",
		Body:       "agenda",
		StartTime:  start,
		EndTime:    end,
		AllDay:     false,
		BusyStatus: 2,
		Attendees: []CalendarAttendee{
			{Name: "Alice", Email: "alice@x"},
			{Email: "bob@x"},
		},
		ReminderMin: 10,
	}
	d := draftFromInput(in)
	if d.Subject != "Standup" || d.Location != "Zoom" || d.BusyStatus != 2 {
		t.Errorf("scalar fields wrong: %+v", d)
	}
	if !d.StartTime.Equal(start) || !d.EndTime.Equal(end) {
		t.Errorf("time fields wrong: %+v", d)
	}
	if d.Reminder != 10 {
		t.Errorf("reminder = %d, want 10", d.Reminder)
	}
	if len(d.Attendees) != 2 || d.Attendees[0].Name != "Alice" || d.Attendees[1].Email != "bob@x" {
		t.Errorf("attendees = %+v", d.Attendees)
	}
}

// calendarCUDFakeServer extends calendarFakeServer with full Sync
// CRUD support: bootstrap (key=0 → "C1") plus Add/Change/Delete
// command echoing back the right Responses shape.
type calendarCUDFakeServer struct {
	mu        sync.Mutex
	provCalls int
	syncCalls int
}

func (f *calendarCUDFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	cmd := r.URL.Query().Get("Cmd")
	switch cmd {
	case "Settings":
		b, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			),
		}, wbxml.DefaultRegistry())
		w.Write(b)
	case "Provision":
		f.mu.Lock()
		f.provCalls++
		call := f.provCalls
		f.mu.Unlock()
		key := "TKEY"
		if call%2 == 0 {
			key = "FKEY"
		}
		b, _ := wbxml.Marshal(&wbxml.Document{
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
		w.Write(b)
	case "Sync":
		f.mu.Lock()
		f.syncCalls++
		f.mu.Unlock()
		body, _ := readReqBody(r)
		doc, err := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
		if err != nil || doc.Root == nil {
			http.Error(w, "bad sync", 400)
			return
		}
		var clientID string
		if cmds := doc.Root.Find("Commands"); cmds != nil {
			if add := cmds.Find("Add"); add != nil {
				if cid := add.Find("ClientId"); cid != nil {
					clientID = cid.TextContent()
				}
			}
		}
		coll := wbxml.E(wbxml.PageAirSync, "Collection",
			wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("C+1")),
			wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal-id")),
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
		)
		if clientID != "" {
			coll.Children = append(coll.Children,
				wbxml.E(wbxml.PageAirSync, "Responses",
					wbxml.E(wbxml.PageAirSync, "Add",
						wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(clientID)),
						wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("cal-id:99")),
						wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					),
				),
			)
		}
		b, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Collections", coll),
			),
		}, wbxml.DefaultRegistry())
		w.Write(b)
	default:
		http.Error(w, "unhandled "+cmd, 400)
	}
}

func readReqBody(r *http.Request) ([]byte, error) {
	const max = 1 << 20
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < max {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}

func TestCalendarCreate_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&calendarCUDFakeServer{}).handle))
	defer srv.Close()
	cfg := &config.Config{Accounts: []config.Account{{
		Name: "alpha", ServerURL: srv.URL, Username: "u", ASVersion: "14.1",
		DefaultAccess: config.AccessRW,
		Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
	}}}
	m := NewManager(cfg, &fakeStateProvider{},
		&fakeResolver{pw: map[string]string{"alpha": "p"}},
		staticDeviceIDs{"alpha": "abc"})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	out := callTool(t, s, "calendar_create_event", CalendarCreateEventInput{
		Account: "alpha", FolderID: "cal-id",
		Subject: "Plan", StartTime: start, EndTime: start.Add(time.Hour),
	})
	if out["id"] != "cal-id:99" {
		t.Errorf("id = %v, want cal-id:99", out["id"])
	}
}

func TestCalendarUpdate_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&calendarCUDFakeServer{}).handle))
	defer srv.Close()
	cfg := &config.Config{Accounts: []config.Account{{
		Name: "alpha", ServerURL: srv.URL, Username: "u", ASVersion: "14.1",
		DefaultAccess: config.AccessRW,
		Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
	}}}
	m := NewManager(cfg, &fakeStateProvider{},
		&fakeResolver{pw: map[string]string{"alpha": "p"}},
		staticDeviceIDs{"alpha": "abc"})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	callTool(t, s, "calendar_update_event", CalendarUpdateEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
		Subject: "Plan (updated)", StartTime: start, EndTime: start.Add(time.Hour),
	})
}

func TestCalendarDelete_success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&calendarCUDFakeServer{}).handle))
	defer srv.Close()
	cfg := &config.Config{Accounts: []config.Account{{
		Name: "alpha", ServerURL: srv.URL, Username: "u", ASVersion: "14.1",
		DefaultAccess: config.AccessRW,
		Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
	}}}
	m := NewManager(cfg, &fakeStateProvider{},
		&fakeResolver{pw: map[string]string{"alpha": "p"}},
		staticDeviceIDs{"alpha": "abc"})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerCalendarTools(s, m.cfg, m)

	callTool(t, s, "calendar_delete_event", CalendarDeleteEventInput{
		Account: "alpha", FolderID: "cal-id", ID: "cal-id:42",
	})
}

func TestEventRowFrom_organizerAndAttendees(t *testing.T) {
	row := eventRowFrom(eas.EventItem{
		ServerID:       "id",
		Subject:        "S",
		OrganizerName:  "Alice",
		OrganizerEmail: "alice@x",
		Attendees: []eas.EventAttendee{
			{Name: "Bob", Email: "bob@x"},
			{Email: "carol@x"},
		},
	})
	if row.Organizer != "Alice <alice@x>" {
		t.Errorf("Organizer = %q", row.Organizer)
	}
	if len(row.Attendees) != 2 {
		t.Fatalf("len(Attendees) = %d", len(row.Attendees))
	}
	if !strings.Contains(row.Attendees[0], "Bob") || !strings.Contains(row.Attendees[1], "carol@x") {
		t.Errorf("attendees = %v", row.Attendees)
	}
}
