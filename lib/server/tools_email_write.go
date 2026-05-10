package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerEmailWriteTools wires the email write tools (send, reply, forward,
// move, set_flags, delete). A tool is only registered if at least one
// configured account permits writes for the email class; the tool's
// account enum is then narrowed to those accounts (signaled in prose,
// enforced via Manager.CheckClass).
func registerEmailWriteTools(s *mcp.Server, cfg *config.Config, m *Manager) {
	writers := cfg.WritableAccounts(config.ClassEmail)
	if len(writers) == 0 {
		return
	}
	registerEmailSend(s, m, writers)
	registerEmailReply(s, m, writers)
	registerEmailForward(s, m, writers)
	registerEmailMove(s, m, writers)
	registerEmailSetFlags(s, m, writers)
	registerEmailDelete(s, m, writers)
}

// --- email_send ------------------------------------------------------------

// EmailAddress is a typed input for tool inputs. The model can pass either
// a plain address ("a@b") or a display-formatted string ("Alice <a@b>").
type EmailAddress struct {
	Address string `json:"address" jsonschema:"the email address (and optional display name in RFC 5322 form)"`
}

// EmailSendInput is the schema for email_send.
type EmailSendInput struct {
	Account    string         `json:"account" jsonschema:"the configured account name"`
	From       string         `json:"from,omitempty" jsonschema:"sender header; defaults to the account's username"`
	To         []EmailAddress `json:"to" jsonschema:"primary recipients"`
	Cc         []EmailAddress `json:"cc,omitempty"`
	Bcc        []EmailAddress `json:"bcc,omitempty"`
	Subject    string         `json:"subject"`
	BodyText   string         `json:"body_text,omitempty" jsonschema:"plain-text body (UTF-8)"`
	BodyHTML   string         `json:"body_html,omitempty" jsonschema:"HTML body (will be sent as a multipart/alternative if both text and html are provided)"`
	InReplyTo  string         `json:"in_reply_to,omitempty" jsonschema:"Message-ID of the message being replied to (sets In-Reply-To and References headers)"`
	SaveInSent *bool          `json:"save_in_sent,omitempty" jsonschema:"whether the server stores a copy in Sent Items (default true)"`
}

// EmailSendOutput reports the outcome of email_send.
type EmailSendOutput struct {
	Status string `json:"status"`
}

func registerEmailSend(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "email_send",
		Description: "Compose and send a new email.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailSendInput) (*mcp.CallToolResult, EmailSendOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassEmail, true); err != nil {
			return nil, EmailSendOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailSendOutput{}, err
		}
		from := in.From
		if from == "" {
			from = m.cfg.FindAccount(in.Account).Username
		}
		mime, err := buildMIME(messageFields{
			From:      from,
			To:        addrs(in.To),
			Cc:        addrs(in.Cc),
			Bcc:       addrs(in.Bcc),
			Subject:   in.Subject,
			InReplyTo: in.InReplyTo,
			Text:      in.BodyText,
			HTML:      in.BodyHTML,
		})
		if err != nil {
			return nil, EmailSendOutput{}, fmt.Errorf("compose: %w", err)
		}
		opts := eas.SendMailOptions{MIME: mime}
		if in.SaveInSent != nil && !*in.SaveInSent {
			opts.SkipSaveInSent = true
		}
		if err := c.SendMail(ctx, opts); err != nil {
			return nil, EmailSendOutput{}, fmt.Errorf("SendMail: %w", err)
		}
		return jsonResult(EmailSendOutput{Status: "sent"})
	})
}

// --- email_reply -----------------------------------------------------------

// EmailReplyInput is the schema for email_reply.
type EmailReplyInput struct {
	Account    string `json:"account" jsonschema:"the configured account name"`
	FolderID   string `json:"folder_id" jsonschema:"folder containing the original message"`
	ID         string `json:"id" jsonschema:"server id of the original message"`
	BodyText   string `json:"body_text,omitempty"`
	BodyHTML   string `json:"body_html,omitempty"`
	ReplyAll   bool   `json:"reply_all,omitempty" jsonschema:"reply to everyone in To/Cc, not just the sender"`
	SaveInSent *bool  `json:"save_in_sent,omitempty"`
}

func registerEmailReply(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "email_reply",
		Description: "Reply to an email by id. The server merges this reply with the " +
			"original message; the client only supplies the new body text. " +
			"reply_all CCs everyone in To/Cc.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailReplyInput) (*mcp.CallToolResult, EmailSendOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassEmail, true); err != nil {
			return nil, EmailSendOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailSendOutput{}, err
		}
		// EAS SmartReply: server fills in headers and body of the original.
		// We only send the new content as a minimal MIME part. reply_all is
		// the server's responsibility — we set a header to indicate
		// preference; SOGo and Z-Push respect it.
		mime, err := buildReplyMIME(in.BodyText, in.BodyHTML, in.ReplyAll)
		if err != nil {
			return nil, EmailSendOutput{}, err
		}
		opts := eas.ReplyForwardOptions{
			SendMailOptions: eas.SendMailOptions{MIME: mime},
			FolderID:        in.FolderID,
			ServerID:        in.ID,
		}
		if in.SaveInSent != nil && !*in.SaveInSent {
			opts.SkipSaveInSent = true
		}
		if err := c.SmartReply(ctx, opts); err != nil {
			return nil, EmailSendOutput{}, fmt.Errorf("SmartReply: %w", err)
		}
		return jsonResult(EmailSendOutput{Status: "sent"})
	})
}

// --- email_forward ---------------------------------------------------------

// EmailForwardInput is the schema for email_forward.
type EmailForwardInput struct {
	Account    string         `json:"account" jsonschema:"the configured account name"`
	FolderID   string         `json:"folder_id" jsonschema:"folder containing the original message"`
	ID         string         `json:"id" jsonschema:"server id of the original message"`
	To         []EmailAddress `json:"to" jsonschema:"recipients of the forward"`
	Cc         []EmailAddress `json:"cc,omitempty"`
	BodyText   string         `json:"body_text,omitempty" jsonschema:"intro text the user wrote above the forwarded body"`
	BodyHTML   string         `json:"body_html,omitempty"`
	SaveInSent *bool          `json:"save_in_sent,omitempty"`
}

func registerEmailForward(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "email_forward",
		Description: "Forward an email to new recipients. The server attaches the original message body.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailForwardInput) (*mcp.CallToolResult, EmailSendOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassEmail, true); err != nil {
			return nil, EmailSendOutput{}, err
		}
		if len(in.To) == 0 {
			return nil, EmailSendOutput{}, errors.New("at least one recipient required")
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailSendOutput{}, err
		}
		mime, err := buildMIME(messageFields{
			To:      addrs(in.To),
			Cc:      addrs(in.Cc),
			Subject: "", // server fills in "Fwd: …" from the original
			Text:    in.BodyText,
			HTML:    in.BodyHTML,
		})
		if err != nil {
			return nil, EmailSendOutput{}, err
		}
		opts := eas.ReplyForwardOptions{
			SendMailOptions: eas.SendMailOptions{MIME: mime},
			FolderID:        in.FolderID,
			ServerID:        in.ID,
		}
		if in.SaveInSent != nil && !*in.SaveInSent {
			opts.SkipSaveInSent = true
		}
		if err := c.SmartForward(ctx, opts); err != nil {
			return nil, EmailSendOutput{}, fmt.Errorf("SmartForward: %w", err)
		}
		return jsonResult(EmailSendOutput{Status: "sent"})
	})
}

// --- email_move ------------------------------------------------------------

// EmailMoveInput is the schema for email_move.
type EmailMoveInput struct {
	Account    string   `json:"account" jsonschema:"the configured account name"`
	FromFolder string   `json:"from_folder" jsonschema:"source folder id"`
	ToFolder   string   `json:"to_folder" jsonschema:"destination folder id"`
	IDs        []string `json:"ids" jsonschema:"one or more message ids to move"`
}

// EmailMoveOutput reports per-id move outcomes.
type EmailMoveOutput struct {
	Results []EmailMoveResult `json:"results"`
}

// EmailMoveResult is one row in EmailMoveOutput.
type EmailMoveResult struct {
	SrcID   string `json:"src_id"`
	NewID   string `json:"new_id,omitempty"`
	Status  int    `json:"status"`
	Success bool   `json:"success"`
}

func registerEmailMove(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name:        "email_move",
		Description: "Move one or more emails between folders. Returns a per-id result list with the new server-assigned id in the destination folder.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailMoveInput) (*mcp.CallToolResult, EmailMoveOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassEmail, true); err != nil {
			return nil, EmailMoveOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailMoveOutput{}, err
		}
		results, err := c.MoveItems(ctx, in.FromFolder, in.ToFolder, in.IDs)
		if err != nil {
			return nil, EmailMoveOutput{}, fmt.Errorf("MoveItems: %w", err)
		}
		out := EmailMoveOutput{}
		for _, r := range results {
			out.Results = append(out.Results, EmailMoveResult{
				SrcID:   r.SrcServerID,
				NewID:   r.DstServerID,
				Status:  r.Status,
				Success: r.Status == 3, // EAS Move "success" code per MS-ASMOV §2.2.1
			})
		}
		return jsonResult(out)
	})
}

// --- email_set_flags -------------------------------------------------------

// EmailSetFlagsInput is the schema for email_set_flags.
type EmailSetFlagsInput struct {
	Account  string `json:"account" jsonschema:"the configured account name"`
	FolderID string `json:"folder_id" jsonschema:"folder containing the message"`
	ID       string `json:"id" jsonschema:"message id"`
	Read     *bool  `json:"read,omitempty" jsonschema:"set read state; omit to leave unchanged"`
	Flagged  *bool  `json:"flagged,omitempty" jsonschema:"set flagged state; omit to leave unchanged"`
}

// EmailSetFlagsOutput reports the outcome.
type EmailSetFlagsOutput struct {
	Status int `json:"status"`
}

func registerEmailSetFlags(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "email_set_flags",
		Description: "Update the read or flagged state of a message. " +
			"Pass only the flags you want to change; omitted flags are left alone.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailSetFlagsInput) (*mcp.CallToolResult, EmailSetFlagsOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassEmail, true); err != nil {
			return nil, EmailSetFlagsOutput{}, err
		}
		if in.Read == nil && in.Flagged == nil {
			return nil, EmailSetFlagsOutput{}, errors.New("at least one of read or flagged must be set")
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailSetFlagsOutput{}, err
		}
		results, err := c.ApplyEmailChanges(ctx, in.FolderID, []eas.EmailChange{{
			ServerID: in.ID,
			Read:     in.Read,
			Flagged:  in.Flagged,
		}})
		if err != nil {
			return nil, EmailSetFlagsOutput{}, fmt.Errorf("ApplyEmailChanges: %w", err)
		}
		status := 1 // assume OK if server returned no per-item status
		if len(results) > 0 {
			status = results[0].Status
		}
		return jsonResult(EmailSetFlagsOutput{Status: status})
	})
}

// --- email_delete ----------------------------------------------------------

// EmailDeleteInput is the schema for email_delete.
type EmailDeleteInput struct {
	Account  string `json:"account" jsonschema:"the configured account name"`
	FolderID string `json:"folder_id" jsonschema:"folder containing the message"`
	ID       string `json:"id" jsonschema:"message id"`
}

// EmailDeleteOutput reports the outcome.
type EmailDeleteOutput struct {
	Status int `json:"status"`
}

func registerEmailDelete(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "email_delete",
		Description: "Delete an email. By default the server moves it to Deleted Items " +
			"(DeletesAsMoves is set); permanent removal requires a second delete from there.",
	}
	scopeEnum(tool, "account", accounts)

	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in EmailDeleteInput) (*mcp.CallToolResult, EmailDeleteOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassEmail, true); err != nil {
			return nil, EmailDeleteOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, EmailDeleteOutput{}, err
		}
		results, err := c.ApplyEmailChanges(ctx, in.FolderID, []eas.EmailChange{{
			ServerID: in.ID,
			Delete:   true,
		}})
		if err != nil {
			return nil, EmailDeleteOutput{}, fmt.Errorf("ApplyEmailChanges: %w", err)
		}
		status := 1
		if len(results) > 0 {
			status = results[0].Status
		}
		return jsonResult(EmailDeleteOutput{Status: status})
	})
}

// --- MIME compose helpers --------------------------------------------------

func addrs(in []EmailAddress) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if s := strings.TrimSpace(a.Address); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// messageFields is the input to buildMIME. All fields optional (per use
// case — buildMIME validates what it needs).
type messageFields struct {
	From      string
	To        []string
	Cc        []string
	Bcc       []string
	Subject   string
	InReplyTo string
	Text      string
	HTML      string
}

// buildMIME constructs an RFC 5322 message from the given fields. If both
// Text and HTML are present, the body is multipart/alternative. If only
// one is present, the body is a single text part.
func buildMIME(f messageFields) ([]byte, error) {
	if len(f.To) == 0 {
		return nil, errors.New("at least one To: address required")
	}
	if f.Text == "" && f.HTML == "" {
		return nil, errors.New("at least one of body_text or body_html required")
	}
	var sb strings.Builder
	if f.From != "" {
		sb.WriteString("From: ")
		sb.WriteString(f.From)
		sb.WriteString("\r\n")
	}
	sb.WriteString("To: ")
	sb.WriteString(strings.Join(f.To, ", "))
	sb.WriteString("\r\n")
	if len(f.Cc) > 0 {
		sb.WriteString("Cc: ")
		sb.WriteString(strings.Join(f.Cc, ", "))
		sb.WriteString("\r\n")
	}
	if len(f.Bcc) > 0 {
		sb.WriteString("Bcc: ")
		sb.WriteString(strings.Join(f.Bcc, ", "))
		sb.WriteString("\r\n")
	}
	if f.Subject != "" {
		sb.WriteString("Subject: ")
		sb.WriteString(f.Subject)
		sb.WriteString("\r\n")
	}
	if f.InReplyTo != "" {
		sb.WriteString("In-Reply-To: ")
		sb.WriteString(f.InReplyTo)
		sb.WriteString("\r\n")
		sb.WriteString("References: ")
		sb.WriteString(f.InReplyTo)
		sb.WriteString("\r\n")
	}
	sb.WriteString("Date: ")
	sb.WriteString(time.Now().UTC().Format(time.RFC1123Z))
	sb.WriteString("\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	if f.Text != "" && f.HTML != "" {
		boundary := "asmcp-" + randHex(12)
		sb.WriteString(`Content-Type: multipart/alternative; boundary="` + boundary + "\"\r\n\r\n")
		sb.WriteString("--" + boundary + "\r\n")
		sb.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(f.Text)
		if !strings.HasSuffix(f.Text, "\r\n") {
			sb.WriteString("\r\n")
		}
		sb.WriteString("--" + boundary + "\r\n")
		sb.WriteString("Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(f.HTML)
		if !strings.HasSuffix(f.HTML, "\r\n") {
			sb.WriteString("\r\n")
		}
		sb.WriteString("--" + boundary + "--\r\n")
	} else if f.HTML != "" {
		sb.WriteString("Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(f.HTML)
	} else {
		sb.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		sb.WriteString(f.Text)
	}
	return []byte(sb.String()), nil
}

// buildReplyMIME constructs a minimal MIME message for SmartReply /
// SmartForward. Headers are minimal: the server fills in From/To/Subject/
// References. We send the body and (when reply_all is set) an
// X-MS-Exchange-Inbox-Reply-All header that some servers honor.
func buildReplyMIME(text, html string, replyAll bool) ([]byte, error) {
	if text == "" && html == "" {
		return nil, errors.New("at least one of body_text or body_html required")
	}
	var sb strings.Builder
	sb.WriteString("MIME-Version: 1.0\r\n")
	if replyAll {
		// Hint that this is a reply-all; servers vary on whether they
		// honor this. Z-Push does; SOGo ignores.
		sb.WriteString("X-MS-Exchange-Inbox-Reply-All: 1\r\n")
	}
	if text != "" && html != "" {
		boundary := "asmcp-r-" + randHex(10)
		sb.WriteString(`Content-Type: multipart/alternative; boundary="` + boundary + "\"\r\n\r\n")
		sb.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
		sb.WriteString(text)
		if !strings.HasSuffix(text, "\r\n") {
			sb.WriteString("\r\n")
		}
		sb.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n")
		sb.WriteString(html)
		if !strings.HasSuffix(html, "\r\n") {
			sb.WriteString("\r\n")
		}
		sb.WriteString("--" + boundary + "--\r\n")
	} else if html != "" {
		sb.WriteString("Content-Type: text/html; charset=utf-8\r\n\r\n")
		sb.WriteString(html)
	} else {
		sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		sb.WriteString(text)
	}
	return []byte(sb.String()), nil
}

// randHex returns n hex chars from crypto/rand. Used for MIME boundaries.
func randHex(n int) string {
	const hexc = "0123456789abcdef"
	out := make([]byte, n)
	buf := make([]byte, (n+1)/2)
	_, _ = rand.Read(buf)
	for i := range n {
		if i%2 == 0 {
			out[i] = hexc[(buf[i/2]>>4)&0x0F]
		} else {
			out[i] = hexc[buf[i/2]&0x0F]
		}
	}
	return string(out)
}
