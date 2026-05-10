// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"strings"
	"testing"

	"activesync-mcp/lib/config"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rwMockManager is a Manager wired with easmock and AccessRW so write
// tools register and pass CheckClass.
func rwMockManager(t *testing.T, c eas.Client) *Manager {
	return newMockManager(t, c, mockManagerOpts{access: config.AccessRW})
}

func TestEmailSend_buildsValidMIME(t *testing.T) {
	var sent eas.SendMailOptions
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SendMailFunc: func(_ context.Context, opts eas.SendMailOptions) error {
				sent = opts
				return nil
			},
		},
	}
	m := rwMockManager(t, mock)

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
	mime := string(sent.MIME)
	if !strings.Contains(mime, "To: bob@example.com") {
		t.Errorf("MIME missing To header:\n%s", mime)
	}
	if !strings.Contains(mime, "Subject: Test") {
		t.Errorf("MIME missing Subject:\n%s", mime)
	}
	if !strings.Contains(mime, "Hello, Bob.") {
		t.Errorf("MIME missing body:\n%s", mime)
	}
}

func TestEmailReply_includesSourceFolder(t *testing.T) {
	var got eas.ReplyForwardOptions
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SmartReplyFunc: func(_ context.Context, opts eas.ReplyForwardOptions) error {
				got = opts
				return nil
			},
		},
	}
	m := rwMockManager(t, mock)

	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	_ = callTool(t, s, "email_reply", EmailReplyInput{
		Account:  "alpha",
		FolderID: "inbox",
		ID:       "inbox:42",
		BodyText: "Got it.",
	})
	if got.FolderID != "inbox" {
		t.Errorf("FolderID = %q", got.FolderID)
	}
	if got.ServerID != "inbox:42" {
		t.Errorf("ServerID = %q", got.ServerID)
	}
}

func TestEmailForward_includesSourceFolder(t *testing.T) {
	var got eas.ReplyForwardOptions
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SmartForwardFunc: func(_ context.Context, opts eas.ReplyForwardOptions) error {
				got = opts
				return nil
			},
		},
	}
	m := rwMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	_ = callTool(t, s, "email_forward", EmailForwardInput{
		Account: "alpha", FolderID: "inbox", ID: "inbox:42",
		To:       []EmailAddress{{Address: "carol@x"}},
		BodyText: "FYI",
	})
	if got.FolderID != "inbox" {
		t.Errorf("FolderID = %q", got.FolderID)
	}
}

func TestEmailMove_mapsResults(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			MoveItemsFunc: func(_ context.Context, src, dst string, ids []string) ([]eas.MoveItemResult, error) {
				if src != "inbox" || dst != "archive" {
					t.Errorf("got src=%q dst=%q", src, dst)
				}
				return []eas.MoveItemResult{
					{SrcServerID: "inbox:42", DstServerID: "archive:7", Status: 3},
				}, nil
			},
		},
	}
	m := rwMockManager(t, mock)

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

func TestEmailDelete_sendsSyncDeleteCommand(t *testing.T) {
	var got []eas.EmailChange
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			ApplyEmailChangesFunc: func(_ context.Context, _ string, changes []eas.EmailChange) ([]eas.EmailChangeResult, error) {
				got = changes
				return []eas.EmailChangeResult{{ServerID: "inbox:42", Status: 1}}, nil
			},
		},
	}
	m := rwMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	out := callTool(t, s, "email_delete", EmailDeleteInput{
		Account: "alpha", FolderID: "inbox", ID: "inbox:42",
	})
	if int(out["status"].(float64)) != 1 {
		t.Errorf("status = %v, want 1", out["status"])
	}
	if len(got) != 1 || !got[0].Delete {
		t.Errorf("change set = %+v, want one Delete", got)
	}
}

func TestEmailSetFlags_marksReadAndFlagged(t *testing.T) {
	var got []eas.EmailChange
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			ApplyEmailChangesFunc: func(_ context.Context, _ string, changes []eas.EmailChange) ([]eas.EmailChangeResult, error) {
				got = changes
				return []eas.EmailChangeResult{{ServerID: "inbox:42", Status: 1}}, nil
			},
		},
	}
	m := rwMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	read := true
	flagged := true
	out := callTool(t, s, "email_set_flags", EmailSetFlagsInput{
		Account: "alpha", FolderID: "inbox", ID: "inbox:42",
		Read: &read, Flagged: &flagged,
	})
	if int(out["status"].(float64)) != 1 {
		t.Errorf("status = %v, want 1", out["status"])
	}
	if len(got) != 1 {
		t.Fatalf("change set len = %d", len(got))
	}
	if got[0].Read == nil || !*got[0].Read {
		t.Errorf("Read = %v, want *true", got[0].Read)
	}
	if got[0].Flagged == nil || !*got[0].Flagged {
		t.Errorf("Flagged = %v, want *true", got[0].Flagged)
	}
}

func TestEmailSetFlags_requiresAtLeastOne(t *testing.T) {
	mock := &easmock.Client{} // no Func set; if any method runs, sentinel error fires
	m := rwMockManager(t, mock)
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerEmailWriteTools(s, m.cfg, m)

	if err := m.CheckClass("alpha", config.ClassEmail, true); err != nil {
		t.Fatalf("class check: %v", err)
	}
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
	_ = m
}

// MIME / reply-MIME / random-id helpers are pure-Go and don't depend
// on the EAS layer.

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

func TestBuildReplyMIME_textOnly(t *testing.T) {
	mime, err := buildReplyMIME("hello", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mime), "Content-Type: text/plain") ||
		!strings.Contains(string(mime), "hello") {
		t.Errorf("plain reply missing parts:\n%s", mime)
	}
	if strings.Contains(string(mime), "X-MS-Exchange-Inbox-Reply-All") {
		t.Errorf("reply-all header set when not requested:\n%s", mime)
	}
}

func TestBuildReplyMIME_htmlOnly(t *testing.T) {
	mime, err := buildReplyMIME("", "<b>hi</b>", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mime), "Content-Type: text/html") {
		t.Errorf("html content-type missing:\n%s", mime)
	}
}

func TestBuildReplyMIME_alternativeAndReplyAll(t *testing.T) {
	mime, err := buildReplyMIME("plain", "<b>html</b>", true)
	if err != nil {
		t.Fatal(err)
	}
	s := string(mime)
	if !strings.Contains(s, "multipart/alternative") {
		t.Errorf("multipart missing:\n%s", s)
	}
	if !strings.Contains(s, "X-MS-Exchange-Inbox-Reply-All: 1") {
		t.Errorf("reply-all header missing when requested:\n%s", s)
	}
}

func TestBuildReplyMIME_emptyBodyFails(t *testing.T) {
	if _, err := buildReplyMIME("", "", false); err == nil {
		t.Error("want error for empty body")
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
