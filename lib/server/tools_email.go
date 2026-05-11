package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

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
		// Wire-size budget separate from response budget:
		//   - When omitting the preview, ask the server for a 1-byte
		//     body (smallest legal hint; the lib promotes BodyType=None
		//     to Plain so we can't skip BodyPreference entirely).
		//   - Otherwise ask for substantially more than previewBudget.
		//     Marketing emails carry 2-5 KiB of <head>/<style>/<meta>
		//     boilerplate before any visible content; if the server
		//     truncates *raw bytes* at our budget, all the user gets
		//     after htmlToPlain stripping is empty. Ask for previewBudget*8
		//     with a 16 KiB floor and 64 KiB cap so HTML stripping has
		//     enough raw markup to extract previewBudget bytes of text.
		opts.BodyTruncationSize = wireBodyTruncation(previewBudget)
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
			row := emailRowFromItem(it)
			row.BodyPreview = previewFromItem(it, previewBudget)
			out.Items = append(out.Items, row)
		}
		// Surface in-folder Changes (e.g. read flags flipped) as well so
		// the LLM gets a complete view of what's new since the last sync.
		for _, it := range res.Changed {
			row := emailRowFromItem(it)
			row.BodyPreview = previewFromItem(it, previewBudget)
			out.Items = append(out.Items, row)
		}
		return jsonResult(out)
	})
}

// wireBodyTruncation chooses the on-wire BodyTruncationSize /
// BodyPreviewBytes hint to send to the EAS server, given the user-
// facing previewBudget (the body_preview_bytes parameter). It exists
// because:
//   - We strip HTML to plain text *after* the response arrives, so the
//     server-side bytes need to cover <head>/<style>/<meta> overhead
//     before the first byte of visible content.
//   - Z-Push BackendIMAP (with the BodyPreference TruncationSize patch
//     deployed) honors the hint exactly, so a tiny budget would cut
//     the body inside the <head> block and leave nothing for the
//     stripper to extract.
//
// The MCP 1 MiB limit applies to the *response* we hand back to the
// client, not the EAS wire between us and the mail server. We strip
// HTML and trim to previewBudget before responding, so it's safe (and
// necessary) to ask the EAS server for substantially more than the
// user's budget — otherwise marketing-mail head/style preamble can
// fill the entire returned slice and the stripper has no body
// content to extract. Just ask for 1 MiB unconditionally; servers
// only send what the message actually contains.
//
// budget == 0 maps to 1 (the smallest legal hint; the lib forces a
// BodyPreference element on the wire).
func wireBodyTruncation(previewBudget int) int {
	if previewBudget == 0 {
		return 1
	}
	return 1024 * 1024
}

// previewFromItem renders an item's body as a plain-text preview
// bounded by budget. HTML bodies are stripped to plain text first
// (the server may have ignored our request for Type=Plain when the
// message has no text/plain MIME alternative), so the bytes we ship
// carry actual content rather than markup. Returns "" when budget==0
// — that's the documented "omit body_preview" affordance.
func previewFromItem(it eas.EmailItem, budget int) string {
	if budget == 0 {
		return ""
	}
	body := it.Body
	if it.BodyType == eas.BodyTypeHTML {
		body = htmlToPlain(body)
	}
	return trimBodyPreview(body, budget)
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

// htmlToPlain renders an HTML document fragment to plain text suitable
// for body_preview: tags stripped, entities decoded, runs of whitespace
// collapsed to a single space, paragraph-level boundaries (`<br>`,
// closing block-level tags) preserved as line breaks. Content of
// `<head>`, `<style>`, and `<script>` is dropped — those carry styling
// and scripting, not user-visible text.
//
// The point is "lossy ASCII for triage", not document fidelity. A
// 200-byte preview should read like prose so a model can decide
// whether the message is worth a full email_get round-trip.
func htmlToPlain(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	skip := 0 // depth inside <head>/<style>/<script>
	lastWasSpace := true
	addText := func(t string) {
		for _, r := range t {
			if unicode.IsSpace(r) {
				if !lastWasSpace {
					b.WriteByte(' ')
					lastWasSpace = true
				}
				continue
			}
			b.WriteRune(r)
			lastWasSpace = false
		}
	}
	addNewline := func() {
		if b.Len() == 0 || lastWasSpace {
			return
		}
		b.WriteByte('\n')
		lastWasSpace = true
	}
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(b.String())
		case html.TextToken:
			if skip == 0 {
				addText(string(z.Text()))
			}
		case html.StartTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "head", "style", "script":
				skip++
			case "body":
				// HTML5 implicit <head> close. Truncated mail often
				// gets cut before the explicit </head>; without this,
				// skip stays > 0 and we never emit the body content.
				skip = 0
			case "br", "hr":
				// HTML5 void elements; tokenizer emits them as
				// StartTag (no end tag exists in the source).
				addNewline()
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "head", "style", "script":
				if skip > 0 {
					skip--
				}
			case "p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote":
				addNewline()
			}
		case html.SelfClosingTagToken:
			name, _ := z.TagName()
			if string(name) == "br" {
				addNewline()
			}
		}
	}
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
//
// BodyPreview is a pointer (same rationale as EmailListInput.BodyPreview):
// distinguishes "field absent → use default" from "explicit 0 → omit
// body_preview entirely". Without that distinction the existing default
// fires for callers who explicitly opted out, and Z-Push BackendIMAP
// returns the full HTML body (often 30-500 KiB per marketing-email hit)
// regardless of TruncationSize, blowing the 1 MiB MCP result cap on
// search hits as small as limit=2.
type EmailSearchInput struct {
	Account       string `json:"account" jsonschema:"the configured account name"`
	Query         string `json:"query" jsonschema:"free-text query (server-side full-text search)"`
	FolderID      string `json:"folder_id,omitempty" jsonschema:"restrict to a single folder; empty searches all folders"`
	DeepTraversal bool   `json:"deep_traversal,omitempty" jsonschema:"include subfolders when folder_id is set"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max hits to return (default 50)"`
	Offset        int    `json:"offset,omitempty" jsonschema:"skip this many initial hits (default 0). NOTE: many EAS servers silently ignore this and always return the first window — see the tool description for the recommended workaround."`
	BodyPreview   *int   `json:"body_preview_bytes,omitempty" jsonschema:"include up to N bytes of plain-text body preview per hit (default 256, set 0 to omit body_preview entirely — useful for HTML-heavy folders like marketing-mail inboxes where the server returns the full document instead of honoring TruncationSize)"`
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
			"Returns matched messages with metadata + a short preview. " +
			"\n\nKNOWN LIMITATIONS (server-side, EAS protocol): " +
			"(1) Result ordering is server-determined and is typically " +
			"date-ascending (oldest first). MS-ASCMD §2.2.3.151 does not " +
			"define a Sort element for Search, so there is no protocol " +
			"knob to flip this. " +
			"(2) The `offset` parameter is sent on the wire (and the server " +
			"echoes it back in `range`) but many EAS servers silently ignore " +
			"it and always return the first window of results. If items at " +
			"`offset > 0` look identical to `offset == 0`, the server is " +
			"ignoring the parameter — there is no client-side fix. " +
			"\nWORKAROUND for finding recent matches: use `email_list` with " +
			"a `date_window` against the target folder (e.g. Inbox) and " +
			"paginate via `cursor`. This loses full-text filtering but " +
			"orders newest-first and respects pagination on every server.",
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
		// Resolve the requested preview budget the same way email_list
		// does: nil → 256 (the lib's own default), explicit 0 → omit,
		// explicit N → cap. The on-wire BodyPreviewBytes is set to 1
		// when omitting (smallest legal hint) so we don't pay for
		// bytes we're going to discard. Post-trim is what actually
		// enforces the budget — Z-Push BackendIMAP returns full HTML
		// regardless of TruncationSize for HTML-only hits.
		previewBudget := 256
		if in.BodyPreview != nil {
			previewBudget = *in.BodyPreview
		}
		if previewBudget < 0 {
			previewBudget = 0
		}
		opts := eas.EmailSearchOptions{
			FolderID:      in.FolderID,
			DeepTraversal: in.DeepTraversal,
			Range:         rangeStr,
		}
		opts.BodyPreviewBytes = wireBodyTruncation(previewBudget)
		res, err := c.SearchEmail(ctx, in.Query, opts)
		if err != nil {
			return nil, EmailSearchOutput{}, fmt.Errorf("SearchEmail: %w", err)
		}
		out := EmailSearchOutput{Range: res.Range, Total: res.Total}
		for _, it := range res.Items {
			row := emailRowFromItem(it)
			row.BodyPreview = previewFromItem(it, previewBudget)
			out.Items = append(out.Items, row)
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
