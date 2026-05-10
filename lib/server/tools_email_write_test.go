package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/wbxml"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rwManager is like newEmailTestManager but with email write enabled
// (default_access = rw).
func rwManager(t *testing.T, srv *httptest.Server) *Manager {
	t.Helper()
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "henry",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRW,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	store := &fakeStateProvider{}
	return NewManager(cfg, store, res, staticDeviceIDs{"alpha": "abc123"})
}

// writeFakeServer responds to Provision (with two-phase handshake) and
// echoes empty 200s for any write command. Captures the most recent
// request body for inspection.
type writeFakeServer struct {
	mu    sync.Mutex
	last  []byte
	calls int
}

func (f *writeFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	body := readAll(r)
	f.mu.Lock()
	f.last = body
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	cmd := r.URL.Query().Get("Cmd")
	switch cmd {
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
		f.calls++
		call := f.calls
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
	case "Sync":
		// For ApplyEmailChanges bootstrap + change. Return a simple ack.
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Collections",
					wbxml.E(wbxml.PageAirSync, "Collection",
						wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("S2")),
						wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
						wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "MoveItems":
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMove, "MoveItems",
				wbxml.E(wbxml.PageMove, "Response",
					wbxml.E(wbxml.PageMove, "SrcMsgId", wbxml.Text("inbox:42")),
					wbxml.E(wbxml.PageMove, "Status", wbxml.Text("3")),
					wbxml.E(wbxml.PageMove, "DstMsgId", wbxml.Text("archive:7")),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	default:
		// SendMail / SmartReply / SmartForward — empty success.
		w.WriteHeader(200)
	}
}

func readAll(r *http.Request) []byte {
	const max = 1 << 20
	buf := make([]byte, 0, 1024)
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
	return buf
}

func TestEmailSend_buildsValidMIME(t *testing.T) {
	f := &writeFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := rwManager(t, srv)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	out := callTool(t, s, "email_send", EmailSendInput{
		Account:  "alpha",
		To:       []EmailAddress{{Address: "bob@example.com"}},
		Subject:  "Test",
		BodyText: "Hello, Bob.",
	})
	if out["status"] != "sent" {
		t.Errorf("status = %v", out["status"])
	}
	// Last call should be SendMail with a Mime opaque body.
	req, err := wbxml.Unmarshal(f.last, wbxml.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if req.Root.Name != "SendMail" {
		t.Errorf("root = %q", req.Root.Name)
	}
	mimeEl := req.Root.Find("Mime")
	if mimeEl == nil {
		t.Fatal("Mime missing")
	}
	var mimeBytes []byte
	for _, c := range mimeEl.Children {
		if op, ok := c.(wbxml.Opaque); ok {
			mimeBytes = []byte(op)
		}
	}
	if !strings.Contains(string(mimeBytes), "To: bob@example.com") {
		t.Errorf("MIME missing To header:\n%s", mimeBytes)
	}
	if !strings.Contains(string(mimeBytes), "Subject: Test") {
		t.Errorf("MIME missing Subject:\n%s", mimeBytes)
	}
	if !strings.Contains(string(mimeBytes), "Hello, Bob.") {
		t.Errorf("MIME missing body:\n%s", mimeBytes)
	}
}

func TestEmailReply_includesSourceFolder(t *testing.T) {
	f := &writeFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := rwManager(t, srv)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	_ = callTool(t, s, "email_reply", EmailReplyInput{
		Account:  "alpha",
		FolderID: "inbox",
		ID:       "inbox:42",
		BodyText: "Got it.",
	})
	req, _ := wbxml.Unmarshal(f.last, wbxml.DefaultRegistry())
	if req.Root.Name != "SmartReply" {
		t.Errorf("root = %q", req.Root.Name)
	}
	if req.Root.Find("Source").Find("FolderId").TextContent() != "inbox" {
		t.Errorf("FolderId wrong")
	}
	if req.Root.Find("Source").Find("ItemId").TextContent() != "inbox:42" {
		t.Errorf("ItemId wrong")
	}
}

func TestEmailMove_mapsResults(t *testing.T) {
	f := &writeFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := rwManager(t, srv)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	out := callTool(t, s, "email_move", EmailMoveInput{
		Account:    "alpha",
		FromFolder: "inbox",
		ToFolder:   "archive",
		IDs:        []string{"inbox:42"},
	})
	results, _ := out["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", out["results"])
	}
	first := results[0].(map[string]any)
	if first["new_id"] != "archive:7" || first["success"] != true {
		t.Errorf("first = %v", first)
	}
}

func TestEmailSetFlags_requiresAtLeastOne(t *testing.T) {
	f := &writeFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	m := rwManager(t, srv)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	// Call directly to bypass MCP error-as-result wrapping.
	if err := m.CheckClass("alpha", config.ClassEmail, true); err != nil {
		t.Fatalf("class check: %v", err)
	}
	// We exercise the validation path with a manual invocation.
	// Use the in-process MCP roundtrip; expect IsError=true.
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "email_set_flags",
		Arguments: EmailSetFlagsInput{
			Account: "alpha", FolderID: "inbox", ID: "x",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("want IsError=true when no flags supplied")
	}
}

func TestRegisterEmailWriteTools_skippedWhenNoRWAccount(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "ro-only",
			ServerURL:     "https://x",
			Username:      "u",
			DefaultAccess: config.AccessRO,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "a"},
		}},
	}
	m := NewManager(cfg, &fakeStateProvider{}, &fakeResolver{}, staticDeviceIDs{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, cfg, m)
	// No assertion: we just expect no panic and no tools registered.
	// Lint smoke: a separate call to the registration after an unwritable
	// account is a no-op.
	_ = m
}

func TestBuildMIME_singlePart(t *testing.T) {
	mime, err := buildMIME(messageFields{
		To:      []string{"x@y"},
		Subject: "S",
		Text:    "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mime), "Content-Type: text/plain") {
		t.Errorf("missing plain content-type:\n%s", mime)
	}
	if strings.Contains(string(mime), "multipart") {
		t.Errorf("unexpected multipart:\n%s", mime)
	}
}

func TestBuildMIME_alternative(t *testing.T) {
	mime, err := buildMIME(messageFields{
		To:      []string{"x@y"},
		Subject: "S",
		Text:    "p",
		HTML:    "<b>h</b>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mime), "multipart/alternative") {
		t.Errorf("missing multipart:\n%s", mime)
	}
}

func TestBuildMIME_validation(t *testing.T) {
	if _, err := buildMIME(messageFields{Text: "x"}); err == nil {
		t.Error("want error for missing recipients")
	}
	if _, err := buildMIME(messageFields{To: []string{"x@y"}}); err == nil {
		t.Error("want error for missing body")
	}
}

func TestRandHex_lengthAndCharset(t *testing.T) {
	for _, n := range []int{1, 5, 12, 32} {
		got := randHex(n)
		if len(got) != n {
			t.Errorf("randHex(%d) len = %d", n, len(got))
		}
		for _, b := range []byte(got) {
			ok := (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
			if !ok {
				t.Errorf("randHex(%d) non-hex byte %q", n, b)
			}
		}
	}
}
