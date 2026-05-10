// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"activesync-mcp/lib/config"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// extrasMockManager wires a Manager with AccessRW (extras tools span
// folder CRUD + OOF set + recipient resolution; many require write).
func extrasMockManager(t *testing.T, c eas.Client) *Manager {
	return newMockManager(t, c, mockManagerOpts{access: config.AccessRW})
}

func TestItemCount(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			GetItemEstimateFunc: func(_ context.Context, ids []string) ([]eas.ItemEstimate, error) {
				if len(ids) != 1 || ids[0] != "inbox" {
					t.Errorf("ids = %v", ids)
				}
				return []eas.ItemEstimate{{CollectionID: "inbox", Class: "Email", Estimate: 17, Status: 1}}, nil
			},
		},
	}
	m := extrasMockManager(t, mock)
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
	mock := &easmock.Client{
		SearchClient: easmock.SearchClient{
			ResolveRecipientsFunc: func(_ context.Context, recipients []string, _ eas.ResolveOptions) ([]eas.ResolveResponse, error) {
				return []eas.ResolveResponse{{
					To:     recipients[0],
					Status: 1,
					Recipients: []eas.ResolvedRecipient{
						{Type: 1, DisplayName: "Alice E.", EmailAddress: "alice@x"},
					},
				}}, nil
			},
		},
	}
	m := extrasMockManager(t, mock)
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
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderCreateFunc: func(_ context.Context, parentID, name string, ft eas.FolderType) (*eas.FolderCreateResult, error) {
				if parentID != "0" || name != "Projects" {
					t.Errorf("got parent=%q name=%q", parentID, name)
				}
				return &eas.FolderCreateResult{ServerID: "new-folder", SyncKey: "FS+1", Status: 1}, nil
			},
		},
	}
	m := extrasMockManager(t, mock)
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
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			EmptyFolderContentsFunc: func(_ context.Context, fid string, _ bool) error {
				if fid != "trash" {
					t.Errorf("folder = %q", fid)
				}
				return nil
			},
		},
	}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "folder_empty", FolderEmptyInput{
		Account: "alpha", FolderID: "trash",
	})
	if out["status"] != "emptied" {
		t.Errorf("status = %v", out["status"])
	}
}

func TestFolderRenameTool_success(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderUpdateFunc: func(_ context.Context, sid, parent, newName string) error {
				if sid != "folder-x" || newName != "renamed" {
					t.Errorf("got sid=%q name=%q", sid, newName)
				}
				return nil
			},
		},
	}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "folder_rename", FolderRenameInput{
		Account: "alpha", ID: "folder-x", NewDisplayName: "renamed",
	})
	if out["status"] != "ok" {
		t.Errorf("status = %v", out["status"])
	}
}

func TestFolderDeleteTool_success(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderDeleteFunc: func(_ context.Context, sid string) error {
				if sid != "folder-x" {
					t.Errorf("sid = %q", sid)
				}
				return nil
			},
		},
	}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "folder_delete", FolderDeleteInput{
		Account: "alpha", ID: "folder-x",
	})
	if out["status"] != "deleted" {
		t.Errorf("status = %v", out["status"])
	}
}

func TestOofGetTool(t *testing.T) {
	mock := &easmock.Client{
		SettingsClient: easmock.SettingsClient{
			GetOofFunc: func(context.Context) (*eas.OofConfig, error) {
				return &eas.OofConfig{State: eas.OofDisabled}, nil
			},
		},
	}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "oof_get", OofGetInput{Account: "alpha"})
	if out["state"] != "disabled" {
		t.Errorf("state = %v", out["state"])
	}
}

func TestOofSetTool_success(t *testing.T) {
	mock := &easmock.Client{
		SettingsClient: easmock.SettingsClient{
			SetOofFunc: func(_ context.Context, cfg eas.OofConfig) error {
				if cfg.State != eas.OofGlobal || cfg.InternalReply.ReplyMessage != "I am away" {
					t.Errorf("cfg = %+v", cfg)
				}
				return nil
			},
		},
	}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

	out := callTool(t, s, "oof_set", OofSetInput{
		Account:       "alpha",
		State:         "global",
		InternalReply: "I am away",
	})
	if out["status"] != "ok" {
		t.Errorf("status = %v", out["status"])
	}
}

func TestOofSetTool_timeBasedRequiresStartEnd(t *testing.T) {
	mock := &easmock.Client{} // sentinel — should not be hit
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)

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
		Name: "oof_set",
		Arguments: OofSetInput{
			Account: "alpha", State: "time_based", // missing start/end
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("want IsError when time_based without start/end")
	}
}

// Pure-Go helpers — no EAS layer.

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

func TestParseTimeRFC3339(t *testing.T) {
	for _, c := range []struct {
		in     string
		wantOk bool
	}{
		{"2026-05-09T12:00:00Z", true},
		{"2026-05-09T12:00:00-05:00", true},
		{"", false},
		{"not-a-time", false},
		{"2026-13-01T00:00:00Z", false},
	} {
		got := parseTimeRFC3339(c.in)
		if c.wantOk && got.IsZero() {
			t.Errorf("parseTimeRFC3339(%q): got zero, want non-zero", c.in)
		}
		if !c.wantOk && !got.IsZero() {
			t.Errorf("parseTimeRFC3339(%q): got %v, want zero", c.in, got)
		}
	}
}

func TestOofStateLabel(t *testing.T) {
	for _, c := range []struct {
		in   eas.OofState
		want string
	}{
		{eas.OofGlobal, "global"},
		{eas.OofTimeBased, "time_based"},
		{eas.OofDisabled, "disabled"},
		{eas.OofState(99), "disabled"}, // unknown maps to disabled
	} {
		if got := oofStateLabel(c.in); got != c.want {
			t.Errorf("oofStateLabel(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOofMessageRow(t *testing.T) {
	plain := oofMessageRow(eas.OofMessage{
		Enabled: true, ReplyMessage: "out", BodyType: eas.BodyTypePlain,
	})
	if plain.BodyType != "plain" || !plain.Enabled || plain.ReplyMessage != "out" {
		t.Errorf("plain = %+v", plain)
	}
	html := oofMessageRow(eas.OofMessage{
		Enabled: false, ReplyMessage: "<b>out</b>", BodyType: eas.BodyTypeHTML,
	})
	if html.BodyType != "html" || html.Enabled {
		t.Errorf("html = %+v", html)
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

// Error-wrap tests for the extras handlers.

func TestItemCount_wrapsError(t *testing.T) {
	mock := &easmock.Client{FolderClient: easmock.FolderClient{
		GetItemEstimateFunc: func(context.Context, []string) ([]eas.ItemEstimate, error) {
			return nil, errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "item_count", ItemCountInput{
		Account: "alpha", FolderIDs: []string{"i"},
	})
	if !strings.Contains(errText(t, res), "GetItemEstimate") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestResolveRecipients_wrapsError(t *testing.T) {
	mock := &easmock.Client{SearchClient: easmock.SearchClient{
		ResolveRecipientsFunc: func(context.Context, []string, eas.ResolveOptions) ([]eas.ResolveResponse, error) {
			return nil, errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "resolve_recipients", ResolveRecipientsInput{
		Account: "alpha", Recipients: []string{"x"},
	})
	if !strings.Contains(errText(t, res), "ResolveRecipients") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestFolderCreate_wrapsError(t *testing.T) {
	mock := &easmock.Client{FolderClient: easmock.FolderClient{
		FolderCreateFunc: func(context.Context, string, string, eas.FolderType) (*eas.FolderCreateResult, error) {
			return nil, errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "folder_create", FolderCreateInput{
		Account: "alpha", ParentID: "0", DisplayName: "X", Type: "email",
	})
	if !strings.Contains(errText(t, res), "FolderCreate") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestFolderRename_wrapsError(t *testing.T) {
	mock := &easmock.Client{FolderClient: easmock.FolderClient{
		FolderUpdateFunc: func(context.Context, string, string, string) error {
			return errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "folder_rename", FolderRenameInput{
		Account: "alpha", ID: "x", NewDisplayName: "y",
	})
	if !strings.Contains(errText(t, res), "FolderUpdate") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestFolderDelete_wrapsError(t *testing.T) {
	mock := &easmock.Client{FolderClient: easmock.FolderClient{
		FolderDeleteFunc: func(context.Context, string) error {
			return errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "folder_delete", FolderDeleteInput{
		Account: "alpha", ID: "x",
	})
	if !strings.Contains(errText(t, res), "FolderDelete") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestFolderEmpty_wrapsError(t *testing.T) {
	mock := &easmock.Client{FolderClient: easmock.FolderClient{
		EmptyFolderContentsFunc: func(context.Context, string, bool) error {
			return errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "folder_empty", FolderEmptyInput{
		Account: "alpha", FolderID: "x",
	})
	if !strings.Contains(errText(t, res), "EmptyFolderContents") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestOofGet_wrapsError(t *testing.T) {
	mock := &easmock.Client{SettingsClient: easmock.SettingsClient{
		GetOofFunc: func(context.Context) (*eas.OofConfig, error) {
			return nil, errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "oof_get", OofGetInput{Account: "alpha"})
	if !strings.Contains(errText(t, res), "GetOof") {
		t.Errorf("err = %q", errText(t, res))
	}
}

func TestOofSet_wrapsError(t *testing.T) {
	mock := &easmock.Client{SettingsClient: easmock.SettingsClient{
		SetOofFunc: func(context.Context, eas.OofConfig) error {
			return errors.New("boom")
		},
	}}
	m := extrasMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerExtraTools(s, m.cfg, m)
	res := callToolErr(t, s, "oof_set", OofSetInput{
		Account: "alpha", State: "global",
	})
	if !strings.Contains(errText(t, res), "SetOof") {
		t.Errorf("err = %q", errText(t, res))
	}
}
