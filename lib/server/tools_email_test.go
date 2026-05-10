package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/wbxml"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// emailFakeServer handles Provision (two-phase), FolderSync (one folder
// per type), Sync (returns one email), and ItemOperations Fetch (returns
// the same email with full body).
type emailFakeServer struct {
	mu        sync.Mutex
	provCalls int
	syncCalls int
}

func (f *emailFakeServer) handle(w http.ResponseWriter, r *http.Request) {
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
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS-1")),
				wbxml.E(wbxml.PageFolderHierarchy, "Changes",
					wbxml.E(wbxml.PageFolderHierarchy, "Count", wbxml.Text("3")),
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
					wbxml.E(wbxml.PageFolderHierarchy, "Add",
						wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("project-id")),
						wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text("inbox-id")),
						wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Project")),
						wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text("12")),
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
		key := "S1"
		var cmds *wbxml.Element
		if call == 2 {
			key = "S2"
			cmds = wbxml.E(wbxml.PageAirSync, "Commands",
				wbxml.E(wbxml.PageAirSync, "Add",
					wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("inbox-id:42")),
					wbxml.E(wbxml.PageAirSync, "ApplicationData",
						wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("Hi")),
						wbxml.E(wbxml.PageEmail, "From", wbxml.Text("alice@x")),
						wbxml.E(wbxml.PageEmail, "To", wbxml.Text("henry@x")),
						wbxml.E(wbxml.PageEmail, "DateReceived", wbxml.Text("2024-01-15T12:00:00.000Z")),
						wbxml.E(wbxml.PageEmail, "Read", wbxml.Text("0")),
						wbxml.E(wbxml.PageAirSyncBase, "Body",
							wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
							wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("preview body")),
						),
					),
				),
			)
		}
		coll := wbxml.E(wbxml.PageAirSync, "Collection",
			wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
			wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox-id")),
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
		)
		if cmds != nil {
			coll.Children = append(coll.Children, cmds)
		}
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Collections", coll),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)

	case "ItemOperations":
		mime := []byte("From: alice@x\r\nTo: henry@x\r\nSubject: Hi\r\n\r\nFull message body")
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageItemOperations, "Response",
					wbxml.E(wbxml.PageItemOperations, "Fetch",
						wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("inbox-id:42")),
						wbxml.E(wbxml.PageItemOperations, "Properties",
							wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("Hi")),
							wbxml.E(wbxml.PageAirSyncBase, "Body",
								wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("4")),
								wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Opaque(mime)),
							),
						),
					),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)

	default:
		http.Error(w, "unhandled "+r.URL.Query().Get("Cmd"), 400)
	}
}

func newEmailTestManager(t *testing.T, srv *httptest.Server) *Manager {
	t.Helper()
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	store := &fakeStateProvider{}
	return NewManager(cfg, store, res, staticDeviceIDs{"alpha": "abc123"})
}

// callTool invokes a registered tool by name, returning the JSON object
// from its TextContent.
func callTool(t *testing.T, srv *mcp.Server, name string, args any) map[string]any {
	t.Helper()
	// Connect a client transport in-process.
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type: %T", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, tc.Text)
	}
	return out
}

func TestEmailListFolders_filtersToMail(t *testing.T) {
	f := &emailFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := newEmailTestManager(t, srv)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	out := callTool(t, s, "email_list_folders", EmailListFoldersInput{Account: "alpha"})
	folders, ok := out["folders"].([]any)
	if !ok {
		t.Fatalf("folders missing or wrong type: %T", out["folders"])
	}
	// Calendar (type 8) should be filtered out; Inbox + Project remain.
	if len(folders) != 2 {
		t.Errorf("len(folders) = %d (got %v)", len(folders), folders)
	}
	names := []string{}
	for _, f := range folders {
		names = append(names, f.(map[string]any)["display_name"].(string))
	}
	for _, want := range []string{"Inbox", "Project"} {
		if !slices.Contains(names, want) {
			t.Errorf("missing %q; got %v", want, names)
		}
	}
}

func TestEmailList_returnsItem(t *testing.T) {
	f := &emailFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := newEmailTestManager(t, srv)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	out := callTool(t, s, "email_list", EmailListInput{
		Account:  "alpha",
		FolderID: "inbox-id",
	})
	items, ok := out["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v", out["items"])
	}
	first := items[0].(map[string]any)
	if first["subject"] != "Hi" {
		t.Errorf("subject: %v", first["subject"])
	}
	if first["from"] != "alice@x" {
		t.Errorf("from: %v", first["from"])
	}
	if cur, _ := out["sync_cursor"].(string); cur != "S2" {
		t.Errorf("sync_cursor: %v", out["sync_cursor"])
	}
}

func TestEmailGet_returnsMIME(t *testing.T) {
	f := &emailFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := newEmailTestManager(t, srv)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	out := callTool(t, s, "email_get", EmailGetInput{
		Account:  "alpha",
		FolderID: "inbox-id",
		ID:       "inbox-id:42",
	})
	if out["body_type"] != "mime" {
		t.Errorf("body_type: %v", out["body_type"])
	}
	mime, _ := out["body_mime"].(string)
	if !strings.Contains(mime, "Full message body") {
		t.Errorf("body_mime missing marker:\n%s", mime)
	}
}

func TestParseDateWindow(t *testing.T) {
	cases := map[string]int{
		"":     int(4), // FilterTwoWeek
		"2w":   int(4),
		"none": 0,
		"1d":   1,
		"3d":   2,
		"1w":   3,
		"1m":   5,
		"3m":   6,
		"6m":   7,
		"junk": int(4),
	}
	for in, want := range cases {
		if int(parseDateWindow(in)) != want {
			t.Errorf("parseDateWindow(%q) = %d, want %d", in, parseDateWindow(in), want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		{"", "mime"}, {"mime", "mime"}, {"plain", "plain"},
		{"html", "html"}, {"junk", "mime"},
	} {
		_, name := parseFormat(c.in)
		if name != c.want {
			t.Errorf("parseFormat(%q) = %q, want %q", c.in, name, c.want)
		}
	}
}
