// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"errors"
	"slices"
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
