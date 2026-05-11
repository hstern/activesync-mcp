// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// folderSyncFolders is the canned FolderSync result reused across email
// tests: an Inbox (Type 2), a Calendar (Type 8, filtered out by
// email_list_folders), and a user mail folder (Type 12).
var folderSyncFolders = &eas.FolderSyncResult{
	SyncKey: "FS-1",
	Added: []eas.Folder{
		{ServerID: "inbox-id", ParentID: "0", DisplayName: "Inbox", Type: eas.FolderTypeInbox},
		{ServerID: "cal-id", ParentID: "0", DisplayName: "Calendar", Type: eas.FolderTypeCalendar},
		{ServerID: "project-id", ParentID: "inbox-id", DisplayName: "Project", Type: eas.FolderTypeUserMail},
	},
}

func TestEmailListFolders_filtersToMail(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) { return folderSyncFolders, nil },
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	out := callTool(t, s, "email_list_folders", EmailListFoldersInput{Account: "alpha"})
	folders, ok := out["folders"].([]any)
	if !ok {
		t.Fatalf("folders missing or wrong type: %T", out["folders"])
	}
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

// memFolderCache is a tiny FolderCacheProvider for tests that want to
// verify the cache path. Keeps everything in a per-account map; no
// persistence.
type memFolderCache struct {
	byAcct map[string]map[string]eas.Folder
}

func (m *memFolderCache) FolderCache(account string) FolderCache {
	if m.byAcct == nil {
		m.byAcct = map[string]map[string]eas.Folder{}
	}
	if m.byAcct[account] == nil {
		m.byAcct[account] = map[string]eas.Folder{}
	}
	return &memFolderCacheView{store: m.byAcct[account]}
}

type memFolderCacheView struct{ store map[string]eas.Folder }

func (v *memFolderCacheView) All() ([]eas.Folder, error) {
	out := make([]eas.Folder, 0, len(v.store))
	for _, f := range v.store {
		out = append(out, f)
	}
	return out, nil
}

func (v *memFolderCacheView) Apply(fs *eas.FolderSyncResult) error {
	if fs == nil {
		return nil
	}
	for _, f := range fs.Added {
		v.store[f.ServerID] = f
	}
	for _, f := range fs.Updated {
		v.store[f.ServerID] = f
	}
	for _, id := range fs.Deleted {
		delete(v.store, id)
	}
	return nil
}

func (v *memFolderCacheView) Clear() error {
	clear(v.store)
	return nil
}

// TestEmailListFolders_secondCallStillReturnsCached pins the bug fix:
// FolderSync is incremental, so the second call returns an empty
// delta. With a wired FolderCache, email_list_folders must still
// surface the cumulative list — not {"folders": null}.
func TestEmailListFolders_secondCallStillReturnsCached(t *testing.T) {
	calls := 0
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
				calls++
				if calls == 1 {
					// First call: server returns the whole hierarchy.
					return folderSyncFolders, nil
				}
				// Subsequent calls: empty delta (nothing changed).
				return &eas.FolderSyncResult{SyncKey: "FS-2"}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	m.SetFolderCache(&memFolderCache{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	// First call: populates the cache.
	first := callTool(t, s, "email_list_folders", EmailListFoldersInput{Account: "alpha"})
	if got, _ := first["folders"].([]any); len(got) != 2 {
		t.Fatalf("first call: want 2 folders, got %v", first["folders"])
	}

	// Second call: empty delta. Without the cache fix this returned
	// {"folders": null}; with the cache it returns the cumulative list.
	second := callTool(t, s, "email_list_folders", EmailListFoldersInput{Account: "alpha"})
	got, ok := second["folders"].([]any)
	if !ok {
		t.Fatalf("regression: second call returned non-list (folders=%v)", second["folders"])
	}
	if len(got) != 2 {
		t.Errorf("second call: want 2 folders from cache, got %d (%v)", len(got), got)
	}
}

// TestEmailList_emptyCursorResetsBeforeSync pins the cursor-leakage
// fix: with no Cursor passed, the handler must reset the per-folder
// SyncKey to "0" before SyncEmail so the server returns the most
// recent batch instead of resuming a prior session's pagination.
func TestEmailList_emptyCursorResetsBeforeSync(t *testing.T) {
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				return &eas.EmailSyncResult{SyncKey: "S1"}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	// Pre-set the per-folder cursor so we can detect the reset.
	if err := m.store.AccountState("alpha").SetSyncKey(t.Context(), "inbox-id", "K42"); err != nil {
		t.Fatal(err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	callTool(t, s, "email_list", EmailListInput{Account: "alpha", FolderID: "inbox-id"})

	got, _ := m.store.AccountState("alpha").SyncKey(t.Context(), "inbox-id")
	// SyncEmail in production also writes back its own new key after
	// the call (S1 here, set by the mock-implied-write — actually mock
	// doesn't write, so we just assert the reset happened *before* the
	// Sync call by checking it is no longer K42).
	if got == "K42" {
		t.Error("cursor not reset before SyncEmail (still K42)")
	}
}

func TestEmailList_passedCursorIsHonored(t *testing.T) {
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				return &eas.EmailSyncResult{SyncKey: "S2"}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	callTool(t, s, "email_list", EmailListInput{
		Account: "alpha", FolderID: "inbox-id", Cursor: "K42",
	})

	got, _ := m.store.AccountState("alpha").SyncKey(t.Context(), "inbox-id")
	// We can't observe the exact value SyncEmail saw without instrumenting
	// the mock to capture pre-call state, so we just assert the user-supplied
	// cursor wasn't quietly dropped (would now be "0" if the empty-path
	// branch fired by mistake).
	if got == "0" {
		t.Error("cursor reset to 0 even though caller supplied K42")
	}
}

// TestSyncFolderList_recoversFromStatus9 pins the recovery path
// for "FolderSync: status 9 (OutOfSpace)" — what some servers
// (notably Z-Push) return when the SyncKey or server-side
// per-device state is corrupt and only a fresh-start sync will
// work. The eas client library auto-retries status 3
// (InvalidSyncKey); status 9 needs the same handling from us.
func TestSyncFolderList_recoversFromStatus9(t *testing.T) {
	calls := 0
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
				calls++
				if calls == 1 {
					return nil, &eas.StatusError{Command: "FolderSync", Code: 9}
				}
				// After our reset+retry, the server replies with the
				// full hierarchy as if from key=0.
				return folderSyncFolders, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	mem := &memFolderCache{}
	m.SetFolderCache(mem)
	// Pre-poison the cache with stale folders that no longer exist
	// on the server. Recovery must drop these.
	mem.FolderCache("alpha").Apply(&eas.FolderSyncResult{
		Added: []eas.Folder{
			{ServerID: "ghost-1", DisplayName: "Stale Folder", Type: eas.FolderTypeUserMail},
			{ServerID: "ghost-2", DisplayName: "Another Stale", Type: eas.FolderTypeUserMail},
		},
	})

	got, err := m.SyncFolderList(t.Context(), "alpha")
	if err != nil {
		t.Fatalf("SyncFolderList after status-9: %v", err)
	}
	if calls != 2 {
		t.Errorf("FolderSync called %d times, want 2 (status-9 then retry)", calls)
	}
	for _, f := range got {
		if strings.HasPrefix(f.ServerID, "ghost-") {
			t.Errorf("stale cache entry %q survived recovery: %+v", f.ServerID, got)
		}
	}
}

// TestSyncFolderList_status9RetryFailureSurfaces asserts that a
// SECOND status-9 (or any other error) from the retry call
// propagates to the caller — we don't loop indefinitely.
func TestSyncFolderList_status9RetryFailureSurfaces(t *testing.T) {
	calls := 0
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
				calls++
				return nil, &eas.StatusError{Command: "FolderSync", Code: 9}
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	m.SetFolderCache(&memFolderCache{})

	_, err := m.SyncFolderList(t.Context(), "alpha")
	if err == nil {
		t.Fatal("want error after retry also fails")
	}
	if calls != 2 {
		t.Errorf("FolderSync called %d times, want 2 (initial + one retry, no more)", calls)
	}
	if !strings.Contains(err.Error(), "FolderSync") {
		t.Errorf("err = %v, want one wrapped under 'FolderSync'", err)
	}
}

// TestEmailListFolders_appliesDelete confirms the cache path drops
// folders the server reports as deleted.
func TestEmailListFolders_appliesDelete(t *testing.T) {
	calls := 0
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
				calls++
				if calls == 1 {
					return folderSyncFolders, nil
				}
				// Second call: server says the project folder was removed.
				return &eas.FolderSyncResult{
					SyncKey: "FS-2",
					Deleted: []string{"project-id"},
				}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	m.SetFolderCache(&memFolderCache{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	callTool(t, s, "email_list_folders", EmailListFoldersInput{Account: "alpha"})
	second := callTool(t, s, "email_list_folders", EmailListFoldersInput{Account: "alpha"})
	got := second["folders"].([]any)
	if len(got) != 1 {
		t.Errorf("after delete delta: want 1 folder, got %d (%v)", len(got), got)
	}
}

func TestEmailList_returnsItem(t *testing.T) {
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(_ context.Context, fid string, _ eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				if fid != "inbox-id" {
					t.Errorf("folder = %q", fid)
				}
				return &eas.EmailSyncResult{
					SyncKey: "S2",
					Added: []eas.EmailItem{
						{
							ServerID: "inbox-id:42",
							Subject:  "Hi",
							From:     "alice@x",
							To:       "henry@x",
							Body:     "preview body",
						},
					},
				}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})

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
	mime := []byte("From: alice@x\r\nTo: henry@x\r\nSubject: Hi\r\n\r\nFull message body")
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			FetchEmailFunc: func(_ context.Context, fid, sid string, _ eas.FetchEmailOptions) (*eas.EmailItem, error) {
				if fid != "inbox-id" || sid != "inbox-id:42" {
					t.Errorf("got fid=%q sid=%q", fid, sid)
				}
				return &eas.EmailItem{
					ServerID: sid,
					Subject:  "Hi",
					BodyType: eas.BodyTypeMIME,
					BodyMIME: mime,
				}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})

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
	if got, _ := out["body_mime"].(string); got == "" || !contains(got, "Full message body") {
		t.Errorf("body_mime missing marker: %q", got)
	}
}

func TestEmailSearch_returnsHits(t *testing.T) {
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SearchEmailFunc: func(_ context.Context, q string, _ eas.EmailSearchOptions) (*eas.EmailSearchResult, error) {
				if q != "Hi" {
					t.Errorf("query = %q", q)
				}
				return &eas.EmailSearchResult{
					Total: 1,
					Range: "0-0",
					Items: []eas.EmailItem{
						{ServerID: "inbox-id:42", Subject: "Hi", From: "alice@x"},
					},
				}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)

	out := callTool(t, s, "email_search", EmailSearchInput{
		Account: "alpha", Query: "Hi", Limit: 5,
	})
	if int(out["total"].(float64)) != 1 {
		t.Errorf("total = %v, want 1", out["total"])
	}
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["subject"] != "Hi" || first["id"] != "inbox-id:42" {
		t.Errorf("first = %v", first)
	}
}

// TestParseDateWindow and TestParseFormat exercise package-private
// helpers that aren't tied to the EAS layer.
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

// contains is a tiny strings.Contains alias so the rewritten tests
// don't carry a strings import they otherwise wouldn't need.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Response-size cap tests. The MCP SDK enforces a 1 MiB ceiling on
// tool results; our handlers stay under it via:
//   - email_get: server-side body truncation default + post-marshal
//     belt-and-suspenders trim of BodyMIME/Body.
//   - email_list: WindowSize × BodyPreview product is capped before
//     the EAS request.
//   - email_search: Limit is capped to a value that keeps the
//     marshaled response under the budget.

func TestShrinkEmailGetOutput_underBudgetUnchanged(t *testing.T) {
	out := &EmailGetOutput{
		ID: "x", Subject: "S", From: "a@b",
		BodyMIME: strings.Repeat("a", 1024),
	}
	original := out.BodyMIME
	shrinkEmailGetOutput(out)
	if out.BodyMIME != original {
		t.Error("payload under budget should not be touched")
	}
	if out.BodyTruncated {
		t.Error("BodyTruncated set when no truncation happened")
	}
}

func TestShrinkEmailGetOutput_overBudgetTrimsBodyMIME(t *testing.T) {
	// 2 MiB of binary-ish content: well over the cap.
	huge := strings.Repeat("Z", 2_000_000)
	out := &EmailGetOutput{
		ID: "x", Subject: "S",
		BodyMIME: huge,
	}
	shrinkEmailGetOutput(out)
	if marshaledSize(out) > maxResponseBytes {
		t.Errorf("after shrink, size = %d > cap %d", marshaledSize(out), maxResponseBytes)
	}
	if !out.BodyTruncated {
		t.Error("BodyTruncated should be set after shrinking")
	}
	if !strings.Contains(out.BodyMIME, "truncated") && out.BodyMIME != "" {
		t.Errorf("expected truncation marker in BodyMIME, got prefix: %q", out.BodyMIME[:min(80, len(out.BodyMIME))])
	}
}

func TestShrinkEmailGetOutput_overBudgetWithBodyOnly(t *testing.T) {
	// Same scenario but the bulk lives in Body (parsed plain) not
	// BodyMIME — verifies the trim handles either field.
	huge := strings.Repeat("Y", 2_000_000)
	out := &EmailGetOutput{
		ID: "x", Subject: "S",
		BodyType: "plain",
		Body:     huge,
	}
	shrinkEmailGetOutput(out)
	if marshaledSize(out) > maxResponseBytes {
		t.Errorf("after shrink, size = %d > cap", marshaledSize(out))
	}
	if !out.BodyTruncated {
		t.Error("BodyTruncated should be set")
	}
}

// ptr wraps a value as a pointer. Convenience for *int / *bool fields
// in test inputs where the schema needs to distinguish absent from zero.
func ptr[T any](v T) *T { return &v }

// listAndDecode runs email_list with the given input through a fake
// SyncEmail that returns one item with the supplied body. Returns the
// first item's body_preview from the response and a snapshot of opts
// the handler sent to EAS.
func listAndDecode(t *testing.T, in EmailListInput, body string) (string, eas.EmailSyncOptions) {
	t.Helper()
	var seen eas.EmailSyncOptions
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(_ context.Context, _ string, opts eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				seen = opts
				return &eas.EmailSyncResult{
					SyncKey: "S",
					Added:   []eas.EmailItem{{ServerID: "1", Subject: "S", Body: body}},
				}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	out := callTool(t, s, "email_list", in)
	items, ok := out["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("response missing items[0]: %+v", out)
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("items[0] not an object: %T", items[0])
	}
	bp, _ := first["body_preview"].(string) // absent → "" (omitempty)
	return bp, seen
}

func TestEmailList_bodyPreviewBytesZeroOmits(t *testing.T) {
	// Marketing-email scenario: server returned 30 KiB of HTML in
	// body_preview ignoring our TruncationSize hint. Caller asked for
	// 0 to opt out — response must contain no body bytes regardless
	// of what the server sent.
	bp, seen := listAndDecode(t, EmailListInput{
		Account: "alpha", FolderID: "i", BodyPreview: ptr(0),
	}, strings.Repeat("X", 30_000))
	if bp != "" {
		t.Errorf("body_preview len = %d, want empty when body_preview_bytes=0", len(bp))
	}
	// On the wire we still send a small TruncationSize to be polite to
	// servers that actually honor it. Don't insist on the exact value
	// — just that it's small.
	if seen.BodyTruncationSize > 1024 {
		t.Errorf("on-wire BodyTruncationSize = %d, want <=1024 when caller opted out", seen.BodyTruncationSize)
	}
}

func TestEmailList_bodyPreviewBytesTrimsOverlongResponse(t *testing.T) {
	// Caller asked for 256 bytes; server (Z-Push BackendIMAP on
	// HTML-only marketing items) returned 30 KiB anyway. The handler
	// must trim before responding.
	bp, _ := listAndDecode(t, EmailListInput{
		Account: "alpha", FolderID: "i", BodyPreview: ptr(256),
	}, strings.Repeat("Y", 30_000))
	if len(bp) > 256 {
		t.Errorf("body_preview len = %d, want <= 256 (post-trim should enforce budget)", len(bp))
	}
	if bp == "" {
		t.Errorf("body_preview empty; expected truncated content")
	}
}

func TestEmailList_bodyPreviewBytesAbsentUsesDefault(t *testing.T) {
	// Field absent in the request → handler must fall back to 1024,
	// not omit. This is the "noop call" behavior most callers see.
	bp, seen := listAndDecode(t, EmailListInput{
		Account: "alpha", FolderID: "i",
	}, strings.Repeat("Z", 5000))
	if len(bp) != 1024 {
		t.Errorf("body_preview len = %d, want 1024 (default)", len(bp))
	}
	if seen.BodyTruncationSize != 1024 {
		t.Errorf("on-wire TruncationSize = %d, want 1024", seen.BodyTruncationSize)
	}
}

func TestTrimBodyPreview_doesNotProduceInvalidUTF8(t *testing.T) {
	// "é" is two bytes in UTF-8. Trimming a string of "é"s at an odd
	// budget that lands mid-rune must produce a valid (shorter) UTF-8
	// string — JSON encoding fails otherwise.
	in := strings.Repeat("é", 10) // 20 bytes
	out := trimBodyPreview(in, 5)
	if !utf8ValidShim(out) {
		t.Errorf("trimBodyPreview produced invalid UTF-8: %q", out)
	}
	if len(out) > 5 {
		t.Errorf("budget exceeded: len=%d", len(out))
	}
}

// utf8ValidShim is a tiny wrapper kept here to avoid importing
// unicode/utf8 in two places with the same name.
func utf8ValidShim(s string) bool {
	for _, r := range s {
		if r == 0xFFFD && len(s) > 0 {
			return false
		}
	}
	return true
}

func TestEmailList_capsWindowSize(t *testing.T) {
	// Caller asks for 1000 items × 4 KiB previews = 4 MiB. Handler
	// must shrink WindowSize so the EAS request stays under the
	// payload budget.
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(_ context.Context, _ string, opts eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				if opts.WindowSize > 700_000/4096+1 {
					t.Errorf("WindowSize=%d not capped (would request ~%d KiB of previews)",
						opts.WindowSize, opts.WindowSize*opts.BodyTruncationSize/1024)
				}
				return &eas.EmailSyncResult{SyncKey: "S"}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	callTool(t, s, "email_list", EmailListInput{
		Account: "alpha", FolderID: "i",
		WindowSize: 1000, BodyPreview: ptr(4096),
	})
}

// searchAndDecode runs email_search through a fake SearchEmail that
// returns one hit with the supplied body. Returns the first hit's
// body_preview from the response and a snapshot of opts the handler
// sent to EAS.
func searchAndDecode(t *testing.T, in EmailSearchInput, body string) (string, eas.EmailSearchOptions) {
	t.Helper()
	var seen eas.EmailSearchOptions
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SearchEmailFunc: func(_ context.Context, _ string, opts eas.EmailSearchOptions) (*eas.EmailSearchResult, error) {
				seen = opts
				return &eas.EmailSearchResult{
					Total: 1, Range: "0-0",
					Items: []eas.EmailItem{{ServerID: "1", Subject: "S", Body: body}},
				}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	out := callTool(t, s, "email_search", in)
	items, _ := out["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no items in response: %+v", out)
	}
	first := items[0].(map[string]any)
	bp, _ := first["body_preview"].(string)
	return bp, seen
}

func TestEmailSearch_bodyPreviewBytesZeroOmits(t *testing.T) {
	// Marketing-mail folder scenario from the issue: server returned
	// 30 KiB of HTML in body_preview ignoring TruncationSize. Caller
	// asked for 0 — response must omit the body bytes.
	bp, seen := searchAndDecode(t, EmailSearchInput{
		Account: "alpha", Query: "x", BodyPreview: ptr(0),
	}, strings.Repeat("X", 30_000))
	if bp != "" {
		t.Errorf("body_preview len = %d, want empty when body_preview_bytes=0", len(bp))
	}
	if seen.BodyPreviewBytes > 1024 {
		t.Errorf("on-wire BodyPreviewBytes = %d, want <=1024 when caller opted out", seen.BodyPreviewBytes)
	}
}

func TestEmailSearch_bodyPreviewBytesTrimsOverlongResponse(t *testing.T) {
	// Caller asked for 256, server returned 30 KiB of HTML. Post-trim
	// must enforce the budget on the response.
	bp, _ := searchAndDecode(t, EmailSearchInput{
		Account: "alpha", Query: "x", BodyPreview: ptr(256),
	}, strings.Repeat("Y", 30_000))
	if len(bp) > 256 {
		t.Errorf("body_preview len = %d, want <= 256", len(bp))
	}
	if bp == "" {
		t.Errorf("body_preview empty; expected truncated content")
	}
}

func TestEmailSearch_bodyPreviewBytesAbsentUsesDefault(t *testing.T) {
	// Field absent → handler uses 256 (the lib's own default surfaced
	// at the MCP layer for callers' benefit).
	bp, seen := searchAndDecode(t, EmailSearchInput{
		Account: "alpha", Query: "x",
	}, strings.Repeat("Z", 5000))
	if len(bp) != 256 {
		t.Errorf("body_preview len = %d, want 256 (default)", len(bp))
	}
	if seen.BodyPreviewBytes != 256 {
		t.Errorf("on-wire BodyPreviewBytes = %d, want 256", seen.BodyPreviewBytes)
	}
}

func TestEmailSearch_capsLimit(t *testing.T) {
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SearchEmailFunc: func(_ context.Context, _ string, opts eas.EmailSearchOptions) (*eas.EmailSearchResult, error) {
				// Range like "0-N" — verify N+1 (= effective limit)
				// doesn't exceed the cap.
				var start, end int
				if _, err := fmt.Sscanf(opts.Range, "%d-%d", &start, &end); err != nil {
					t.Fatalf("Range = %q (parse: %v)", opts.Range, err)
				}
				if end-start+1 > 700 {
					t.Errorf("limit=%d not capped (Range=%q)", end-start+1, opts.Range)
				}
				return &eas.EmailSearchResult{Range: opts.Range}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	callTool(t, s, "email_search", EmailSearchInput{
		Account: "alpha", Query: "x", Limit: 10_000,
	})
}

func TestEmailGet_defaultsBodyTruncation(t *testing.T) {
	// MaxBytes=0 should be replaced with the server-side default so
	// the EAS request asks for a bounded body, not the full thing.
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			FetchEmailFunc: func(_ context.Context, _, _ string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
				if opts.BodyTruncationSize == 0 {
					t.Errorf("BodyTruncationSize=0; should default to %d", defaultBodyBytes)
				}
				if opts.BodyTruncationSize > defaultBodyBytes {
					t.Errorf("BodyTruncationSize=%d > default cap %d", opts.BodyTruncationSize, defaultBodyBytes)
				}
				return &eas.EmailItem{ServerID: "x", Subject: "S"}, nil
			},
		},
	}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	callTool(t, s, "email_get", EmailGetInput{
		Account: "alpha", FolderID: "i", ID: "x",
	})
}

// Error-wrap tests for the read-side handlers.

func TestEmailListFolders_wrapsError(t *testing.T) {
	mock := &easmock.Client{FolderClient: easmock.FolderClient{
		FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	res := callToolErr(t, s, "email_list_folders", EmailListFoldersInput{Account: "alpha"})
	if !contains(errText(t, res), "FolderSync") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestEmailList_wrapsError(t *testing.T) {
	mock := &easmock.Client{EmailClient: easmock.EmailClient{
		SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	res := callToolErr(t, s, "email_list", EmailListInput{Account: "alpha", FolderID: "i"})
	if !contains(errText(t, res), "Sync") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestEmailGet_wrapsError(t *testing.T) {
	mock := &easmock.Client{EmailClient: easmock.EmailClient{
		FetchEmailFunc: func(context.Context, string, string, eas.FetchEmailOptions) (*eas.EmailItem, error) {
			return nil, errors.New("boom")
		},
	}}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	res := callToolErr(t, s, "email_get", EmailGetInput{Account: "alpha", FolderID: "i", ID: "x"})
	if !contains(errText(t, res), "FetchEmail") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestEmailSearch_wrapsError(t *testing.T) {
	mock := &easmock.Client{EmailClient: easmock.EmailClient{
		SearchEmailFunc: func(context.Context, string, eas.EmailSearchOptions) (*eas.EmailSearchResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := newMockManager(t, mock, mockManagerOpts{})
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailReadTools(s, m.cfg, m)
	res := callToolErr(t, s, "email_search", EmailSearchInput{Account: "alpha", Query: "x"})
	if !contains(errText(t, res), "Search") {
		t.Errorf("err = %q", errText(t, res))
	}
}
