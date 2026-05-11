package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerEmailReadTools wires the email read tools into s. It reads cfg
// to scope each tool's `account` parameter enum to the accounts that
// permit at least the corresponding access level.
//
// Read tools (list_folders, list, get) are registered if any account
// exists. Per-tool account enums list every configured account because
// reads are always permitted; write-side enums get narrowed in later
// phases.
func registerEmailReadTools(s *mcp.Server, cfg *config.Config, m *Manager) {
	allAccounts := cfg.AccountNames()
	if len(allAccounts) == 0 {
		return
	}
	registerEmailListFolders(s, m, allAccounts)
	registerEmailList(s, m, allAccounts)
	registerEmailGet(s, m, allAccounts)
	registerEmailSearch(s, m, allAccounts)
}

// --- email_list_folders ----------------------------------------------------

// EmailListFoldersInput is the schema for email_list_folders.
type EmailListFoldersInput struct {
	Account string `json:"account" jsonschema:"the configured account name to query"`
}

// FolderRow is one entry in the email_list_folders output.
type FolderRow struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	TypeCode    int    `json:"type_code"`
	Mailbox     bool   `json:"is_mailbox"`
}

// EmailListFoldersOutput wraps the folder rows.
type EmailListFoldersOutput struct {
	Folders []FolderRow `json:"folders"`
}

func registerEmailListFolders(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "email_list_folders",
		Description: "List the email-bearing folders for an account. " +
			"Returns the user's mail hierarchy (Inbox, Sent, Drafts, custom folders). " +
			"Use the returned id values as folder_id in email_list and email_get.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailListFoldersInput) (*mcp.CallToolResult, EmailListFoldersOutput, error) {
		folders, err := m.SyncFolderList(ctx, in.Account)
		if err != nil {
			return nil, EmailListFoldersOutput{}, err
		}
		out := EmailListFoldersOutput{}
		for _, f := range folders {
			if !isMailFolder(f.Type) {
				continue
			}
			out.Folders = append(out.Folders, FolderRow{
				ID:          f.ServerID,
				ParentID:    f.ParentID,
				DisplayName: f.DisplayName,
				Type:        f.Type.String(),
				TypeCode:    int(f.Type),
				Mailbox:     true,
			})
		}
		return jsonResult(out)
	})
}

func isMailFolder(t eas.FolderType) bool {
	switch t {
	case eas.FolderTypeInbox, eas.FolderTypeDrafts, eas.FolderTypeDeletedItems,
		eas.FolderTypeSentItems, eas.FolderTypeOutbox, eas.FolderTypeUserMail,
		eas.FolderTypeUserGeneric:
		return true
	}
	return false
}

// --- email_list ------------------------------------------------------------

// EmailListInput is the schema for email_list.
//
// BodyPreview is a pointer so we can distinguish "field absent → use
// default 1024" from "field explicitly 0 → omit body_preview entirely".
// If both collapsed to int(0) the server-side default would always
// fire and "set 0 to omit" — a documented affordance — would silently
// not work.
type EmailListInput struct {
	Account     string `json:"account" jsonschema:"the configured account name"`
	FolderID    string `json:"folder_id" jsonschema:"the EAS server-assigned folder identifier (from email_list_folders)"`
	WindowSize  int    `json:"window_size,omitempty" jsonschema:"max items per response (default 50, server caps usually allow up to 512)"`
	DateWindow  string `json:"date_window,omitempty" jsonschema:"limit by recency: one of none, 1d, 3d, 1w, 2w, 1m, 3m, 6m (default 2w)"`
	BodyPreview *int   `json:"body_preview_bytes,omitempty" jsonschema:"include up to N bytes of plain-text body preview per item (default 1024, set 0 to omit body_preview entirely — useful for inboxes with marketing HTML where the server returns the whole document instead of honoring TruncationSize)"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"pagination cursor; omit for the most recent batch, pass back the sync_cursor from a prior response to fetch the next batch"`
}

// EmailRow is one item in the email_list output.
type EmailRow struct {
	ID             string    `json:"id"`
	Subject        string    `json:"subject"`
	From           string    `json:"from"`
	To             string    `json:"to,omitempty"`
	Cc             string    `json:"cc,omitempty"`
	DateReceived   time.Time `json:"date_received,omitzero"`
	Read           bool      `json:"read"`
	Flagged        bool      `json:"flagged,omitempty"`
	Importance     int       `json:"importance,omitempty"`
	HasAttachments bool      `json:"has_attachments,omitempty"`
	BodyPreview    string    `json:"body_preview,omitempty"`
}

// EmailListOutput wraps the result rows.
type EmailListOutput struct {
	Items         []EmailRow `json:"items"`
	MoreAvailable bool       `json:"more_available"`
	SyncCursor    string     `json:"sync_cursor"`
}

func registerEmailList(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "email_list",
		Description: "List recent emails in a folder. Returns metadata and an optional " +
			"plain-text body preview. Pagination is implicit: call again to fetch the next batch when more_available is true.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailListInput) (*mcp.CallToolResult, EmailListOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailListOutput{}, err
		}
		// Pin the cursor before SyncEmail so an empty Cursor returns
		// the top of the inbox (snapshot) rather than picking up
		// wherever a prior session left off.
		if err := m.PrepareListCursor(ctx, in.Account, in.FolderID, in.Cursor); err != nil {
			return nil, EmailListOutput{}, fmt.Errorf("PrepareListCursor: %w", err)
		}
		// Resolve the requested preview budget:
		//   nil      → default 1024 bytes
		//   *p == 0  → omit body_preview from the response (post-trim)
		//   *p > 0   → cap body_preview at p bytes (post-trim)
		// Always send a positive TruncationSize on the wire so the server
		// has an upper bound to honor (when it does); the post-trim is
		// what *actually* enforces the budget on the response we hand
		// back, since some Z-Push backends ignore TruncationSize for
		// HTML-only items and return the whole document.
		previewBudget := 1024
		if in.BodyPreview != nil {
			previewBudget = *in.BodyPreview
		}
		if previewBudget < 0 {
			previewBudget = 0
		}
		opts := eas.EmailSyncOptions{
			WindowSize: in.WindowSize,
			BodyType:   eas.BodyTypePlain,
			DateFilter: parseDateWindow(in.DateWindow),
		}
		// Even when omitting the preview from the response, ask the
		// server for a small body — the lib promotes BodyType=None to
		// Plain, so there's no way to skip the BodyPreference entirely.
		// 1 byte is the smallest legal hint and minimizes wire size on
		// servers that *do* honor TruncationSize.
		if previewBudget == 0 {
			opts.BodyTruncationSize = 1
		} else {
			opts.BodyTruncationSize = previewBudget
		}
		// Cap WindowSize × budget at ~700 KiB so a caller asking
		// for 512 items with 4 KiB previews each can't blow past the
		// MCP result limit. We prefer trimming WindowSize because
		// shorter previews are usually less useful to a model than
		// fewer-but-richer items. When the preview is omitted, only
		// the metadata budget caps WindowSize.
		if opts.WindowSize == 0 {
			opts.WindowSize = 50
		}
		if previewBudget > 0 && opts.WindowSize*previewBudget > 700_000 {
			opts.WindowSize = 700_000 / previewBudget
			if opts.WindowSize < 1 {
				opts.WindowSize = 1
			}
		}
		res, err := c.SyncEmail(ctx, in.FolderID, opts)
		if err != nil {
			return nil, EmailListOutput{}, fmt.Errorf("SyncEmail: %w", err)
		}
		out := EmailListOutput{
			MoreAvailable: res.MoreAvailable,
			SyncCursor:    res.SyncKey,
		}
		for _, it := range res.Added {
			out.Items = append(out.Items, emailRowFromItem(it))
		}
		// Surface in-folder Changes (e.g. read flags flipped) as well so
		// the LLM gets a complete view of what's new since the last sync.
		for _, it := range res.Changed {
			out.Items = append(out.Items, emailRowFromItem(it))
		}
		// Post-trim: enforce previewBudget on the response regardless
		// of what the server actually sent. This is what makes the
		// "set 0 to omit" affordance work, and what protects callers
		// from servers that ignore TruncationSize for HTML bodies and
		// return the entire marketing-email document.
		for i := range out.Items {
			out.Items[i].BodyPreview = trimBodyPreview(out.Items[i].BodyPreview, previewBudget)
		}
		return jsonResult(out)
	})
}

// trimBodyPreview enforces the caller's body_preview_bytes budget on a
// single item's preview field. budget == 0 means "omit"; otherwise the
// field is truncated to at most budget bytes, preserving valid UTF-8 by
// trimming on rune boundaries (so we don't slice mid-rune and produce
// garbage on the wire).
func trimBodyPreview(s string, budget int) string {
	if budget == 0 || s == "" {
		return ""
	}
	if len(s) <= budget {
		return s
	}
	out := s[:budget]
	// Walk back to a rune boundary so we don't return half-decoded UTF-8.
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out
}

func emailRowFromItem(it eas.EmailItem) EmailRow {
	return EmailRow{
		ID:             it.ServerID,
		Subject:        it.Subject,
		From:           it.From,
		To:             it.To,
		Cc:             it.Cc,
		DateReceived:   it.DateReceived,
		Read:           it.Read,
		Flagged:        it.Flagged(),
		Importance:     it.Importance,
		HasAttachments: it.HasAttachments,
		BodyPreview:    it.Body,
	}
}

func parseDateWindow(s string) eas.FilterType {
	switch s {
	case "", "2w":
		return eas.FilterTwoWeek
	case "none":
		return eas.FilterNone
	case "1d":
		return eas.FilterOneDay
	case "3d":
		return eas.FilterThreeDay
	case "1w":
		return eas.FilterOneWeek
	case "1m":
		return eas.FilterOneMonth
	case "3m":
		return eas.FilterThreeMonth
	case "6m":
		return eas.FilterSixMonth
	}
	return eas.FilterTwoWeek
}

// --- email_get -------------------------------------------------------------

// EmailGetInput is the schema for email_get.
type EmailGetInput struct {
	Account  string `json:"account" jsonschema:"the configured account name"`
	FolderID string `json:"folder_id" jsonschema:"the folder containing the email"`
	ID       string `json:"id" jsonschema:"the email's server id (from email_list)"`
	Format   string `json:"format,omitempty" jsonschema:"plain | html | mime (default mime)"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"truncate body to this many bytes; 0 = full body"`
}

// EmailGetOutput is the full email payload returned by email_get.
type EmailGetOutput struct {
	ID             string    `json:"id"`
	Subject        string    `json:"subject"`
	From           string    `json:"from"`
	To             string    `json:"to,omitempty"`
	Cc             string    `json:"cc,omitempty"`
	Bcc            string    `json:"bcc,omitempty"`
	ReplyTo        string    `json:"reply_to,omitempty"`
	DateReceived   time.Time `json:"date_received,omitzero"`
	Read           bool      `json:"read"`
	Flagged        bool      `json:"flagged,omitempty"`
	Importance     int       `json:"importance,omitempty"`
	HasAttachments bool      `json:"has_attachments,omitempty"`
	BodyType       string    `json:"body_type"`
	BodyTruncated  bool      `json:"body_truncated,omitempty"`
	Body           string    `json:"body,omitempty"`
	BodyMIME       string    `json:"body_mime,omitempty"`
}

func registerEmailGet(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "email_get",
		Description: "Fetch the full content of a single email by id. " +
			"Format defaults to MIME (raw RFC 5322 source); pass plain or html for a parsed body.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailGetInput) (*mcp.CallToolResult, EmailGetOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailGetOutput{}, err
		}
		bt, btName := parseFormat(in.Format)
		// Apply a default body cap when the caller didn't specify
		// one, so a single message with attachments doesn't blow
		// past the MCP 1 MiB result limit.
		max := in.MaxBytes
		if max <= 0 || max > defaultBodyBytes {
			max = defaultBodyBytes
		}
		item, err := c.FetchEmail(ctx, in.FolderID, in.ID, eas.FetchEmailOptions{
			BodyType:           bt,
			BodyTruncationSize: max,
		})
		if err != nil {
			return nil, EmailGetOutput{}, fmt.Errorf("FetchEmail: %w", err)
		}
		out := EmailGetOutput{
			ID:             item.ServerID,
			Subject:        item.Subject,
			From:           item.From,
			To:             item.To,
			Cc:             item.Cc,
			Bcc:            item.Bcc,
			ReplyTo:        item.ReplyTo,
			DateReceived:   item.DateReceived,
			Read:           item.Read,
			Flagged:        item.Flagged(),
			Importance:     item.Importance,
			HasAttachments: item.HasAttachments,
			BodyType:       btName,
			BodyTruncated:  item.BodyTruncated,
			Body:           item.Body,
		}
		if len(item.BodyMIME) > 0 {
			out.BodyMIME = string(item.BodyMIME)
		}
		// Belt-and-suspenders: even with a server-side cap, JSON
		// escaping (especially for binary in 8bit MIME parts) can
		// inflate the payload past the budget. Trim the bulkiest
		// field client-side and flag it.
		shrinkEmailGetOutput(&out)
		return jsonResult(out)
	})
}

// shrinkEmailGetOutput trims body fields in place when the marshaled
// payload would exceed maxResponseBytes. We trim BodyMIME first
// (raw + base64-attachment-heavy), then Body, marking BodyTruncated.
// The truncation is destructive and rounds down — callers that need
// the full content should re-call with a smaller MaxBytes or pull
// attachments via a future email_get_attachment tool.
func shrinkEmailGetOutput(out *EmailGetOutput) {
	for {
		if marshaledSize(out) <= maxResponseBytes {
			return
		}
		switch {
		case len(out.BodyMIME) > 4096:
			out.BodyMIME = truncateString(out.BodyMIME, len(out.BodyMIME)/2)
			out.BodyTruncated = true
		case len(out.Body) > 4096:
			out.Body = truncateString(out.Body, len(out.Body)/2)
			out.BodyTruncated = true
		case len(out.BodyMIME) > 0:
			out.BodyMIME = ""
			out.BodyTruncated = true
		case len(out.Body) > 0:
			out.Body = ""
			out.BodyTruncated = true
		default:
			// Nothing left to trim. The caller's metadata alone
			// exceeds the budget — should never happen in practice.
			return
		}
	}
}

func marshaledSize[T any](v T) int {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return 0
	}
	return len(b)
}

func truncateString(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated by activesync-mcp to fit MCP result size limit]"
}

func parseFormat(s string) (eas.BodyType, string) {
	switch s {
	case "plain":
		return eas.BodyTypePlain, "plain"
	case "html":
		return eas.BodyTypeHTML, "html"
	case "", "mime":
		return eas.BodyTypeMIME, "mime"
	}
	return eas.BodyTypeMIME, "mime"
}

// --- email_search ----------------------------------------------------------

// EmailSearchInput is the schema for email_search.
type EmailSearchInput struct {
	Account       string `json:"account" jsonschema:"the configured account name"`
	Query         string `json:"query" jsonschema:"free-text query (server-side full-text search)"`
	FolderID      string `json:"folder_id,omitempty" jsonschema:"restrict to a single folder; empty searches all folders"`
	DeepTraversal bool   `json:"deep_traversal,omitempty" jsonschema:"include subfolders when folder_id is set"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max hits to return (default 50)"`
	Offset        int    `json:"offset,omitempty" jsonschema:"skip this many initial hits (default 0)"`
}

// EmailSearchOutput wraps the matched rows + the server's range/total.
type EmailSearchOutput struct {
	Items []EmailRow `json:"items"`
	Range string     `json:"range"`
	Total int        `json:"total"`
}

func registerEmailSearch(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "email_search",
		Description: "Server-side full-text search of mail. " +
			"Returns matched messages with metadata + a short preview.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailSearchInput) (*mcp.CallToolResult, EmailSearchOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailSearchOutput{}, err
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		// Search returns body previews bounded by the EAS server's
		// default truncation; with hundreds of hits this can blow
		// past the MCP result limit. Cap so the request can't ask
		// for more than ~700 items at once. (Each item is metadata
		// + ~256B body preview by default = ~1 KiB; 700 items =
		// ~700 KiB, leaving headroom for envelope.)
		if limit > 700 {
			limit = 700
		}
		offset := max(in.Offset, 0)
		end := offset + limit - 1
		rangeStr := fmt.Sprintf("%d-%d", offset, end)
		res, err := c.SearchEmail(ctx, in.Query, eas.EmailSearchOptions{
			FolderID:      in.FolderID,
			DeepTraversal: in.DeepTraversal,
			Range:         rangeStr,
		})
		if err != nil {
			return nil, EmailSearchOutput{}, fmt.Errorf("SearchEmail: %w", err)
		}
		out := EmailSearchOutput{Range: res.Range, Total: res.Total}
		for _, it := range res.Items {
			out.Items = append(out.Items, emailRowFromItem(it))
		}
		return jsonResult(out)
	})
}

// --- helpers ---------------------------------------------------------------

// scopeEnum appends an "(allowed: a, b, c)" hint to the tool description
// listing the accounts that are valid for this tool. The MCP SDK doesn't
// expose a clean post-hoc way to narrow an inferred schema's enum, so we
// signal the constraint in prose; the handler still enforces correctness
// via Manager.Client (unknown account → error).
//
// A future refactor can build the schema with jsonschema-go and set
// InputSchema explicitly to encode the enum at the protocol level.
func scopeEnum(t *mcp.Tool, _ string, allowed []string) {
	if len(allowed) == 0 {
		return
	}
	hint := "(allowed accounts: "
	for i, a := range allowed {
		if i > 0 {
			hint += ", "
		}
		hint += a
	}
	hint += ")"
	if t.Description != "" {
		t.Description += " " + hint
	} else {
		t.Description = hint
	}
}

// maxResponseBytes is the soft cap on a single tool result. The MCP
// SDK enforces a hard 1 MiB ceiling; we stay under it with headroom
// for envelope + JSON encoding overhead so handler-side caps can use
// the budget meaningfully.
const maxResponseBytes = 900_000

// defaultBodyBytes is the server-side body truncation we ask EAS for
// when the caller didn't specify one. Empirically: a typical inbox
// message with no attachments is < 50 KiB; 700 KiB leaves room for
// inline images and quoted threads while staying well under
// maxResponseBytes after JSON encoding overhead.
const defaultBodyBytes = 700_000

func jsonResult[T any](v T) (*mcp.CallToolResult, T, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		var zero T
		return nil, zero, fmt.Errorf("marshal: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, v, nil
}
