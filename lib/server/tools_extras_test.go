package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/wbxml"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// extrasFakeServer handles Provision + Settings + the new commands
// (GetItemEstimate, ResolveRecipients, FolderCreate, FolderUpdate,
// FolderDelete, EmptyFolderContents, OOF Settings).
type extrasFakeServer struct {
	mu        sync.Mutex
	provCalls int
}

func (f *extrasFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	switch r.URL.Query().Get("Cmd") {
	case "Settings":
		// Used by both DeviceInformation set and OOF get/set; respond
		// with a dummy OOF document so oof_get has something to parse.
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Oof",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageSettings, "Get",
						wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text("0")),
					),
				),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "Provision":
		f.mu.Lock()
		f.provCalls++
		call := f.provCalls
		f.mu.Unlock()
		key := "TKEY"
		if call == 2 {
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
	case "GetItemEstimate":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageGetItemEstimate, "GetItemEstimate",
				wbxml.E(wbxml.PageGetItemEstimate, "Response",
					wbxml.E(wbxml.PageGetItemEstimate, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageGetItemEstimate, "Collection",
						wbxml.E(wbxml.PageGetItemEstimate, "CollectionId", wbxml.Text("inbox")),
						wbxml.E(wbxml.PageGetItemEstimate, "Class", wbxml.Text("Email")),
						wbxml.E(wbxml.PageGetItemEstimate, "Estimate", wbxml.Text("17")),
					),
				),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "ResolveRecipients":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients",
				wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageResolveRecipients, "Response",
					wbxml.E(wbxml.PageResolveRecipients, "To", wbxml.Text("alice")),
					wbxml.E(wbxml.PageResolveRecipients, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageResolveRecipients, "Recipient",
						wbxml.E(wbxml.PageResolveRecipients, "Type", wbxml.Text("1")),
						wbxml.E(wbxml.PageResolveRecipients, "DisplayName", wbxml.Text("Alice E.")),
						wbxml.E(wbxml.PageResolveRecipients, "EmailAddress", wbxml.Text("alice@x")),
					),
				),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "FolderCreate":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderCreate",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS+1")),
				wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("new-folder")),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "FolderUpdate":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderUpdate",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS+2")),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "FolderDelete":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderDelete",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS+3")),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	case "ItemOperations":
		body, _ := wbxml.Marshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
			),
		}, wbxml.DefaultRegistry())
		w.Write(body)
	default:
		http.Error(w, "unhandled "+r.URL.Query().Get("Cmd"), 400)
	}
}

func extrasManager(t *testing.T, srv *httptest.Server) *Manager {
	t.Helper()
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRW,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	store := &fakeStateProvider{}
	return NewManager(cfg, store, res, staticDeviceIDs{"alpha": "dev"})
}

func TestItemCount(t *testing.T) {
	f := &extrasFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := extrasManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "item_count", ItemCountInput{
		Account: "alpha", FolderIDs: []string{"inbox"},
	})
	rows := out["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["estimate"].(float64) != 17 {
		t.Errorf("rows = %v", rows)
	}
}

func TestResolveRecipientsTool(t *testing.T) {
	f := &extrasFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := extrasManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "resolve_recipients", ResolveRecipientsInput{
		Account: "alpha", Recipients: []string{"alice"},
	})
	rows := out["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	matches := rows[0].(map[string]any)["matches"].([]any)
	if matches[0].(map[string]any)["email_address"] != "alice@x" {
		t.Errorf("match = %v", matches[0])
	}
}

func TestFolderCreateTool(t *testing.T) {
	f := &extrasFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := extrasManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "folder_create", FolderCreateInput{
		Account: "alpha", ParentID: "0", DisplayName: "Projects", Type: "email",
	})
	if out["id"] != "new-folder" {
		t.Errorf("id = %v", out["id"])
	}
}

func TestFolderEmptyTool(t *testing.T) {
	f := &extrasFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := extrasManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "folder_empty", FolderEmptyInput{
		Account: "alpha", FolderID: "trash",
	})
	if out["status"] != "emptied" {
		t.Errorf("status = %v", out["status"])
	}
}

func TestOofGetTool(t *testing.T) {
	f := &extrasFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := extrasManager(t, srv)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "oof_get", OofGetInput{Account: "alpha"})
	if out["state"] != "disabled" {
		t.Errorf("state = %v", out["state"])
	}
}

func TestParseOofState(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		err  bool
	}{
		{"", 0, false},
		{"disabled", 0, false},
		{"global", 1, false},
		{"time_based", 2, false},
		{"junk", 0, true},
	} {
		got, err := parseOofState(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseOofState(%q): err = %v, want err=%v", c.in, err, c.err)
		}
		if !c.err && int(got) != c.want {
			t.Errorf("parseOofState(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFolderTypeForClass(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{"email", 12}, {"", 12}, {"calendar", 13}, {"contacts", 14},
		{"tasks", 15}, {"notes", 17}, {"unknown", 1},
	} {
		got := folderTypeForClass(c.in)
		if int(got) != c.want {
			t.Errorf("folderTypeForClass(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestUniqueWritableAccountsAcrossClasses(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{
			{Name: "rw", DefaultAccess: config.AccessRW},
			{Name: "ro", DefaultAccess: config.AccessRO},
			{Name: "mixed", DefaultAccess: config.AccessRO,
				Access: map[string]string{"calendar": config.AccessRW}},
		},
	}
	got := uniqueWritableAccountsAcrossClasses(cfg)
	if len(got) != 2 || got[0] != "rw" || got[1] != "mixed" {
		t.Errorf("got %v", got)
	}
}
