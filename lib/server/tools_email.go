package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailListFoldersOutput{}, err
		}
		fs, err := c.FolderSync(ctx)
		if err != nil {
			return nil, EmailListFoldersOutput{}, fmt.Errorf("FolderSync: %w", err)
		}
		out := EmailListFoldersOutput{}
		for _, f := range fs.Added {
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
type EmailListInput struct {
	Account     string `json:"account" jsonschema:"the configured account name"`
	FolderID    string `json:"folder_id" jsonschema:"the EAS server-assigned folder identifier (from email_list_folders)"`
	WindowSize  int    `json:"window_size,omitempty" jsonschema:"max items per response (default 50, server caps usually allow up to 512)"`
	DateWindow  string `json:"date_window,omitempty" jsonschema:"limit by recency: one of none, 1d, 3d, 1w, 2w, 1m, 3m, 6m (default 2w)"`
	BodyPreview int    `json:"body_preview_bytes,omitempty" jsonschema:"include up to N bytes of plain-text body preview per item (default 1024, set 0 to omit)"`
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
		opts := eas.EmailSyncOptions{
			WindowSize:         in.WindowSize,
			BodyType:           eas.BodyTypePlain,
			BodyTruncationSize: in.BodyPreview,
			DateFilter:         parseDateWindow(in.DateWindow),
		}
		if in.BodyPreview == 0 {
			opts.BodyTruncationSize = 1024
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
		return jsonResult(out)
	})
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
		item, err := c.FetchEmail(ctx, in.FolderID, in.ID, eas.FetchEmailOptions{
			BodyType:           bt,
			BodyTruncationSize: in.MaxBytes,
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
		return jsonResult(out)
	})
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
