package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// nowFunc is the clock used by availability/time computations. Tests
// may override it to make assertions deterministic.
var nowFunc = func() time.Time { return time.Now().UTC() }

const availabilityWindow = 24 * time.Hour

// parseTimeRFC3339 returns the parsed time or zero on error/empty.
func parseTimeRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// registerExtraTools wires the additional commands added in the
// "complete protocol" phase: folder CRUD, item-count, recipient
// resolution, OOF management, folder empty.
//
// ValidateCert, RightsManagement, DocumentLibrary, and Find stay
// library-only — they're either niche or mostly redundant with the
// existing tool surface, and not generally useful for an LLM.
func registerExtraTools(s *mcp.Server, cfg *config.Config, m *Manager) {
	all := cfg.AccountNames()
	if len(all) == 0 {
		return
	}
	registerItemCount(s, m, all)
	registerResolveRecipients(s, m, all)
	registerOofGet(s, m, all)

	// Folder mutations + OOF set + folder_empty are write-shaped.
	// We don't have a "folder_management" config class — using the
	// most-restrictive class (email) here is a reasonable proxy:
	// folders normally hold mail. Document this in the tool descriptions.
	writers := uniqueWritableAccountsAcrossClasses(cfg)
	if len(writers) > 0 {
		registerFolderCreate(s, m, writers)
		registerFolderRename(s, m, writers)
		registerFolderDelete(s, m, writers)
		registerFolderEmpty(s, m, writers)
		registerOofSet(s, m, writers)
	}
}

// uniqueWritableAccountsAcrossClasses returns accounts that allow writes
// to at least one class. Used to scope folder-management tools.
func uniqueWritableAccountsAcrossClasses(cfg *config.Config) []string {
	seen := make(map[string]struct{})
	for _, c := range config.AllClasses() {
		for _, name := range cfg.WritableAccounts(c) {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for _, a := range cfg.Accounts {
		if _, ok := seen[a.Name]; ok {
			out = append(out, a.Name)
		}
	}
	return out
}

// --- item_count ------------------------------------------------------------

// ItemCountInput is the schema for item_count.
type ItemCountInput struct {
	Account   string   `json:"account"`
	FolderIDs []string `json:"folder_ids" jsonschema:"one or more folder ids to estimate"`
}

// ItemCountRow is one estimate row.
type ItemCountRow struct {
	FolderID string `json:"folder_id"`
	Class    string `json:"class,omitempty"`
	Estimate int    `json:"estimate"`
	Status   int    `json:"status"`
}

// ItemCountOutput wraps the rows.
type ItemCountOutput struct {
	Rows []ItemCountRow `json:"rows"`
}

func registerItemCount(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "item_count",
		Description: "Get the number of items pending sync per folder " +
			"(via EAS GetItemEstimate). Useful for unread/backlog counts " +
			"without doing a full Sync.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in ItemCountInput) (*mcp.CallToolResult, ItemCountOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, ItemCountOutput{}, err
		}
		ests, err := c.GetItemEstimate(ctx, in.FolderIDs)
		if err != nil {
			return nil, ItemCountOutput{}, fmt.Errorf("GetItemEstimate: %w", err)
		}
		out := ItemCountOutput{}
		for _, e := range ests {
			out.Rows = append(out.Rows, ItemCountRow{
				FolderID: e.CollectionID, Class: e.Class,
				Estimate: e.Estimate, Status: e.Status,
			})
		}
		return jsonResult(out)
	})
}

// --- resolve_recipients ----------------------------------------------------

// ResolveRecipientsInput is the schema for resolve_recipients.
type ResolveRecipientsInput struct {
	Account                string   `json:"account"`
	Recipients             []string `json:"recipients" jsonschema:"email addresses or display-name strings to resolve"`
	IncludeAvailability    bool     `json:"include_availability,omitempty" jsonschema:"fetch free/busy data for the next 24 hours"`
	MaxAmbiguousRecipients int      `json:"max_ambiguous,omitempty" jsonschema:"cap on candidates per ambiguous name (default 10)"`
}

// RecipientRow is one resolved recipient row.
type RecipientRow struct {
	Input   string                 `json:"input"`
	Status  int                    `json:"status"`
	Matches []ResolvedRecipientRow `json:"matches,omitempty"`
}

// ResolvedRecipientRow is one match within a recipient resolution.
type ResolvedRecipientRow struct {
	DisplayName    string `json:"display_name,omitempty"`
	EmailAddress   string `json:"email_address,omitempty"`
	Type           int    `json:"type,omitempty"`
	MergedFreeBusy string `json:"merged_free_busy,omitempty"`
}

// ResolveRecipientsOutput wraps the rows.
type ResolveRecipientsOutput struct {
	Rows []RecipientRow `json:"rows"`
}

func registerResolveRecipients(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "resolve_recipients",
		Description: "Resolve email addresses or display-name strings against the " +
			"corporate directory and the user's contacts. Optionally returns " +
			"free/busy availability for the next 24 hours.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in ResolveRecipientsInput) (*mcp.CallToolResult, ResolveRecipientsOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, ResolveRecipientsOutput{}, err
		}
		opts := eas.ResolveOptions{}
		if in.MaxAmbiguousRecipients > 0 {
			opts.MaxAmbiguousRecipients = in.MaxAmbiguousRecipients
		} else {
			opts.MaxAmbiguousRecipients = 10
		}
		if in.IncludeAvailability {
			opts.AvailabilityStart = nowFunc()
			opts.AvailabilityEnd = nowFunc().Add(availabilityWindow)
		}
		results, err := c.ResolveRecipients(ctx, in.Recipients, opts)
		if err != nil {
			return nil, ResolveRecipientsOutput{}, fmt.Errorf("ResolveRecipients: %w", err)
		}
		out := ResolveRecipientsOutput{}
		for _, r := range results {
			row := RecipientRow{Input: r.To, Status: r.Status}
			for _, m := range r.Recipients {
				row.Matches = append(row.Matches, ResolvedRecipientRow{
					DisplayName:    m.DisplayName,
					EmailAddress:   m.EmailAddress,
					Type:           m.Type,
					MergedFreeBusy: m.MergedFreeBusy,
				})
			}
			out.Rows = append(out.Rows, row)
		}
		return jsonResult(out)
	})
}

// --- folder_create / rename / delete / empty -------------------------------

// FolderCreateInput is the schema for folder_create.
type FolderCreateInput struct {
	Account     string `json:"account"`
	ParentID    string `json:"parent_id" jsonschema:"parent folder id (use \"0\" for top-level)"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type,omitempty" jsonschema:"folder class: email, calendar, contacts, tasks, notes (default email)"`
}

// FolderCreateOutput reports the new folder id.
type FolderCreateOutput struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
}

func registerFolderCreate(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "folder_create",
		Description: "Create a new folder. Allowed when the account permits writes to " +
			"at least one class.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in FolderCreateInput) (*mcp.CallToolResult, FolderCreateOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, FolderCreateOutput{}, err
		}
		ft := folderTypeForClass(in.Type)
		res, err := c.FolderCreate(ctx, in.ParentID, in.DisplayName, ft)
		if err != nil {
			return nil, FolderCreateOutput{}, fmt.Errorf("FolderCreate: %w", err)
		}
		return jsonResult(FolderCreateOutput{ID: res.ServerID, Status: res.Status})
	})
}

// FolderRenameInput is the schema for folder_rename.
type FolderRenameInput struct {
	Account        string `json:"account"`
	ID             string `json:"id"`
	NewDisplayName string `json:"new_display_name"`
	NewParentID    string `json:"new_parent_id,omitempty" jsonschema:"set to move the folder; empty leaves the parent unchanged"`
}

// FolderActionOutput is the simple {status: int} response.
type FolderActionOutput struct {
	Status string `json:"status"`
}

func registerFolderRename(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "folder_rename",
		Description: "Rename a folder and/or move it under a new parent.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in FolderRenameInput) (*mcp.CallToolResult, FolderActionOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, FolderActionOutput{}, err
		}
		if err := c.FolderUpdate(ctx, in.ID, in.NewParentID, in.NewDisplayName); err != nil {
			return nil, FolderActionOutput{}, fmt.Errorf("FolderUpdate: %w", err)
		}
		return jsonResult(FolderActionOutput{Status: "ok"})
	})
}

// FolderDeleteInput is the schema for folder_delete.
type FolderDeleteInput struct {
	Account string `json:"account"`
	ID      string `json:"id"`
}

func registerFolderDelete(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "folder_delete",
		Description: "Delete a folder and all its contents. Server typically moves to Deleted Items if recoverable.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in FolderDeleteInput) (*mcp.CallToolResult, FolderActionOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, FolderActionOutput{}, err
		}
		if err := c.FolderDelete(ctx, in.ID); err != nil {
			return nil, FolderActionOutput{}, fmt.Errorf("FolderDelete: %w", err)
		}
		return jsonResult(FolderActionOutput{Status: "deleted"})
	})
}

// FolderEmptyInput is the schema for folder_empty.
type FolderEmptyInput struct {
	Account          string `json:"account"`
	FolderID         string `json:"folder_id"`
	DeleteSubfolders bool   `json:"delete_subfolders,omitempty"`
}

func registerFolderEmpty(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "folder_empty",
		Description: "Remove every item in a folder (and optionally its subfolders) " +
			"without deleting the folder itself. Common use: empty Trash.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in FolderEmptyInput) (*mcp.CallToolResult, FolderActionOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, FolderActionOutput{}, err
		}
		if err := c.EmptyFolderContents(ctx, in.FolderID, in.DeleteSubfolders); err != nil {
			return nil, FolderActionOutput{}, fmt.Errorf("EmptyFolderContents: %w", err)
		}
		return jsonResult(FolderActionOutput{Status: "emptied"})
	})
}

// folderTypeForClass converts the user-facing class label to an EAS
// folder type appropriate for FolderCreate.
func folderTypeForClass(class string) eas.FolderType {
	switch class {
	case "calendar":
		return eas.FolderTypeUserCalendar
	case "contacts":
		return eas.FolderTypeUserContacts
	case "tasks":
		return eas.FolderTypeUserTasks
	case "notes":
		return eas.FolderTypeUserNotes
	case "", "email", "mail":
		return eas.FolderTypeUserMail
	}
	return eas.FolderTypeUserGeneric
}

// --- oof_get / oof_set -----------------------------------------------------

// OofGetInput is the schema for oof_get.
type OofGetInput struct {
	Account string `json:"account"`
}

// OofMessageRow is one variant of the OOF reply (internal/external-known/external-unknown).
type OofMessageRow struct {
	Enabled      bool   `json:"enabled"`
	ReplyMessage string `json:"reply_message,omitempty"`
	BodyType     string `json:"body_type,omitempty"`
}

// OofGetOutput is the OOF configuration.
type OofGetOutput struct {
	State                string        `json:"state" jsonschema:"disabled, global, or time_based"`
	StartTime            string        `json:"start_time,omitempty"`
	EndTime              string        `json:"end_time,omitempty"`
	InternalReply        OofMessageRow `json:"internal_reply"`
	ExternalKnownReply   OofMessageRow `json:"external_known_reply"`
	ExternalUnknownReply OofMessageRow `json:"external_unknown_reply"`
}

func registerOofGet(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{Name: "oof_get", Description: "Read the current Out-of-Office configuration."}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in OofGetInput) (*mcp.CallToolResult, OofGetOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, OofGetOutput{}, err
		}
		cfg, err := c.GetOof(ctx)
		if err != nil {
			return nil, OofGetOutput{}, fmt.Errorf("GetOof: %w", err)
		}
		out := OofGetOutput{
			State:                oofStateLabel(cfg.State),
			InternalReply:        oofMessageRow(cfg.InternalReply),
			ExternalKnownReply:   oofMessageRow(cfg.ExternalKnownReply),
			ExternalUnknownReply: oofMessageRow(cfg.ExternalUnknownReply),
		}
		if !cfg.StartTime.IsZero() {
			out.StartTime = cfg.StartTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		if !cfg.EndTime.IsZero() {
			out.EndTime = cfg.EndTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		return jsonResult(out)
	})
}

// OofSetInput is the schema for oof_set.
type OofSetInput struct {
	Account              string `json:"account"`
	State                string `json:"state" jsonschema:"disabled, global, or time_based"`
	StartTime            string `json:"start_time,omitempty" jsonschema:"RFC3339; required when state=time_based"`
	EndTime              string `json:"end_time,omitempty"`
	InternalReply        string `json:"internal_reply,omitempty" jsonschema:"reply text to internal recipients"`
	ExternalKnownReply   string `json:"external_known_reply,omitempty"`
	ExternalUnknownReply string `json:"external_unknown_reply,omitempty"`
	BodyFormat           string `json:"body_format,omitempty" jsonschema:"plain (default) or html"`
}

func registerOofSet(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "oof_set",
		Description: "Set the user's Out-of-Office configuration.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in OofSetInput) (*mcp.CallToolResult, FolderActionOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, FolderActionOutput{}, err
		}
		state, err := parseOofState(in.State)
		if err != nil {
			return nil, FolderActionOutput{}, err
		}
		cfg := eas.OofConfig{State: state}
		if state == eas.OofTimeBased {
			cfg.StartTime = parseTimeRFC3339(in.StartTime)
			cfg.EndTime = parseTimeRFC3339(in.EndTime)
			if cfg.StartTime.IsZero() || cfg.EndTime.IsZero() {
				return nil, FolderActionOutput{}, errors.New("time_based OOF requires start_time and end_time as RFC3339")
			}
		}
		bt := eas.BodyTypePlain
		if in.BodyFormat == "html" {
			bt = eas.BodyTypeHTML
		}
		mkMsg := func(text string) eas.OofMessage {
			return eas.OofMessage{Enabled: text != "", ReplyMessage: text, BodyType: bt}
		}
		cfg.InternalReply = mkMsg(in.InternalReply)
		cfg.ExternalKnownReply = mkMsg(in.ExternalKnownReply)
		cfg.ExternalUnknownReply = mkMsg(in.ExternalUnknownReply)
		if err := c.SetOof(ctx, cfg); err != nil {
			return nil, FolderActionOutput{}, fmt.Errorf("SetOof: %w", err)
		}
		return jsonResult(FolderActionOutput{Status: "ok"})
	})
}

func oofStateLabel(s eas.OofState) string {
	switch s {
	case eas.OofGlobal:
		return "global"
	case eas.OofTimeBased:
		return "time_based"
	default:
		return "disabled"
	}
}

func parseOofState(s string) (eas.OofState, error) {
	switch s {
	case "disabled", "":
		return eas.OofDisabled, nil
	case "global":
		return eas.OofGlobal, nil
	case "time_based":
		return eas.OofTimeBased, nil
	}
	return 0, fmt.Errorf("invalid OOF state %q (want disabled|global|time_based)", s)
}

func oofMessageRow(msg eas.OofMessage) OofMessageRow {
	bt := "plain"
	if msg.BodyType == eas.BodyTypeHTML {
		bt = "html"
	}
	return OofMessageRow{Enabled: msg.Enabled, ReplyMessage: msg.ReplyMessage, BodyType: bt}
}
