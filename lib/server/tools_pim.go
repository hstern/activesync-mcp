// Package server: contact / task / note / GAL MCP tools.
//
// These three classes share enough structure (Sync + per-class CRUD) that
// their tools live in one file rather than three to keep the surface
// scannable. GAL is appended here because it's the directory-flavored
// counterpart.
package server

import (
	"context"
	"fmt"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPIMTools wires Contacts/Tasks/Notes/GAL.
func registerPIMTools(s *mcp.Server, cfg *config.Config, m *Manager) {
	all := cfg.AccountNames()
	if len(all) == 0 {
		return
	}
	registerContactsTools(s, cfg, m, all)
	registerTasksTools(s, cfg, m, all)
	registerNotesTools(s, cfg, m, all)
	registerGALSearch(s, m, all)
}

// --- Contacts --------------------------------------------------------------

// ContactsListFoldersInput is the schema for contacts_list_folders.
type ContactsListFoldersInput struct {
	Account string `json:"account"`
}

// ContactsFolderRow is one entry in contacts_list_folders.
type ContactsFolderRow struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	DisplayName string `json:"display_name"`
}

// ContactsListFoldersOutput wraps the rows.
type ContactsListFoldersOutput struct {
	Folders []ContactsFolderRow `json:"folders"`
}

// ContactsListInput is the schema for contacts_list.
type ContactsListInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
}

// ContactRow is one entry in contacts_list / contacts_get response.
type ContactRow struct {
	ID            string `json:"id"`
	FirstName     string `json:"first_name,omitempty"`
	LastName      string `json:"last_name,omitempty"`
	CompanyName   string `json:"company_name,omitempty"`
	JobTitle      string `json:"job_title,omitempty"`
	Email1        string `json:"email1,omitempty"`
	Email2        string `json:"email2,omitempty"`
	HomePhone     string `json:"home_phone,omitempty"`
	BusinessPhone string `json:"business_phone,omitempty"`
	MobilePhone   string `json:"mobile_phone,omitempty"`
}

// ContactsListOutput wraps the rows.
type ContactsListOutput struct {
	Contacts      []ContactRow `json:"contacts"`
	MoreAvailable bool         `json:"more_available"`
	SyncCursor    string       `json:"sync_cursor"`
}

// ContactsCreateInput is the schema for contacts_create.
type ContactsCreateInput struct {
	Account       string `json:"account"`
	FolderID      string `json:"folder_id"`
	FirstName     string `json:"first_name,omitempty"`
	LastName      string `json:"last_name,omitempty"`
	CompanyName   string `json:"company_name,omitempty"`
	JobTitle      string `json:"job_title,omitempty"`
	Email1        string `json:"email1,omitempty"`
	Email2        string `json:"email2,omitempty"`
	HomePhone     string `json:"home_phone,omitempty"`
	BusinessPhone string `json:"business_phone,omitempty"`
	MobilePhone   string `json:"mobile_phone,omitempty"`
}

// ContactsUpdateInput is the schema for contacts_update; same as create + ID.
type ContactsUpdateInput struct {
	ContactsCreateInput
	ID string `json:"id"`
}

// ContactsDeleteInput is the schema for contacts_delete.
type ContactsDeleteInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
	ID       string `json:"id"`
}

// IDOutput is the simple {id: string} response shape used by create/update/delete.
type IDOutput struct {
	ID string `json:"id"`
}

func registerContactsTools(s *mcp.Server, cfg *config.Config, m *Manager, accounts []string) {
	folderTool := &mcp.Tool{Name: "contacts_list_folders", Description: "List contact folders for an account."}
	scopeEnum(folderTool, "account", accounts)
	mcp.AddTool(s, folderTool, func(ctx context.Context, _ *mcp.CallToolRequest, in ContactsListFoldersInput) (*mcp.CallToolResult, ContactsListFoldersOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, ContactsListFoldersOutput{}, err
		}
		fs, err := c.FolderSync(ctx)
		if err != nil {
			return nil, ContactsListFoldersOutput{}, fmt.Errorf("FolderSync: %w", err)
		}
		out := ContactsListFoldersOutput{}
		for _, f := range fs.Added {
			if f.Type != eas.FolderTypeContacts && f.Type != eas.FolderTypeUserContacts {
				continue
			}
			out.Folders = append(out.Folders, ContactsFolderRow{ID: f.ServerID, ParentID: f.ParentID, DisplayName: f.DisplayName})
		}
		return jsonResult(out)
	})

	listTool := &mcp.Tool{Name: "contacts_list", Description: "List contacts in a folder."}
	scopeEnum(listTool, "account", accounts)
	mcp.AddTool(s, listTool, func(ctx context.Context, _ *mcp.CallToolRequest, in ContactsListInput) (*mcp.CallToolResult, ContactsListOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, ContactsListOutput{}, err
		}
		res, err := c.SyncContacts(ctx, in.FolderID)
		if err != nil {
			return nil, ContactsListOutput{}, fmt.Errorf("SyncContacts: %w", err)
		}
		out := ContactsListOutput{MoreAvailable: res.MoreAvailable, SyncCursor: res.SyncKey}
		for _, ct := range res.Added {
			out.Contacts = append(out.Contacts, contactRowFrom(ct))
		}
		for _, ct := range res.Changed {
			out.Contacts = append(out.Contacts, contactRowFrom(ct))
		}
		return jsonResult(out)
	})

	writers := cfg.WritableAccounts(config.ClassContacts)
	if len(writers) == 0 {
		return
	}
	createTool := &mcp.Tool{Name: "contacts_create", Description: "Create a new contact."}
	scopeEnum(createTool, "account", writers)
	mcp.AddTool(s, createTool, func(ctx context.Context, _ *mcp.CallToolRequest, in ContactsCreateInput) (*mcp.CallToolResult, IDOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassContacts, true); err != nil {
			return nil, IDOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, IDOutput{}, err
		}
		id, err := c.CreateContact(ctx, in.FolderID, draftFromContactInput(in))
		if err != nil {
			return nil, IDOutput{}, fmt.Errorf("CreateContact: %w", err)
		}
		return jsonResult(IDOutput{ID: id})
	})

	updateTool := &mcp.Tool{Name: "contacts_update", Description: "Update an existing contact."}
	scopeEnum(updateTool, "account", writers)
	mcp.AddTool(s, updateTool, func(ctx context.Context, _ *mcp.CallToolRequest, in ContactsUpdateInput) (*mcp.CallToolResult, IDOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassContacts, true); err != nil {
			return nil, IDOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, IDOutput{}, err
		}
		if err := c.UpdateContact(ctx, in.FolderID, in.ID, draftFromContactInput(in.ContactsCreateInput)); err != nil {
			return nil, IDOutput{}, fmt.Errorf("UpdateContact: %w", err)
		}
		return jsonResult(IDOutput{ID: in.ID})
	})

	deleteTool := &mcp.Tool{Name: "contacts_delete", Description: "Delete a contact."}
	scopeEnum(deleteTool, "account", writers)
	mcp.AddTool(s, deleteTool, func(ctx context.Context, _ *mcp.CallToolRequest, in ContactsDeleteInput) (*mcp.CallToolResult, IDOutput, error) {
		if err := m.CheckClass(in.Account, config.ClassContacts, true); err != nil {
			return nil, IDOutput{}, err
		}
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, IDOutput{}, err
		}
		if err := c.DeleteContact(ctx, in.FolderID, in.ID); err != nil {
			return nil, IDOutput{}, fmt.Errorf("DeleteContact: %w", err)
		}
		return jsonResult(IDOutput{ID: in.ID})
	})
}

func contactRowFrom(c eas.ContactItem) ContactRow {
	return ContactRow{
		ID: c.ServerID, FirstName: c.FirstName, LastName: c.LastName,
		CompanyName: c.CompanyName, JobTitle: c.JobTitle,
		Email1: c.Email1Address, Email2: c.Email2Address,
		HomePhone: c.HomePhone, BusinessPhone: c.BusinessPhone, MobilePhone: c.MobilePhone,
	}
}

func draftFromContactInput(in ContactsCreateInput) eas.ContactDraft {
	return eas.ContactDraft{
		FirstName: in.FirstName, LastName: in.LastName,
		CompanyName: in.CompanyName, JobTitle: in.JobTitle,
		Email1Address: in.Email1, Email2Address: in.Email2,
		HomePhone: in.HomePhone, BusinessPhone: in.BusinessPhone, MobilePhone: in.MobilePhone,
	}
}

// --- Tasks -----------------------------------------------------------------

// TasksListFoldersInput is the schema for tasks_list_folders.
type TasksListFoldersInput struct {
	Account string `json:"account"`
}

// TasksFolderRow is one row in tasks_list_folders.
type TasksFolderRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// TasksListFoldersOutput wraps the rows.
type TasksListFoldersOutput struct {
	Folders []TasksFolderRow `json:"folders"`
}

// TasksListInput is the schema for tasks_list.
type TasksListInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
}

// TaskRow is one task in tasks_list.
type TaskRow struct {
	ID         string    `json:"id"`
	Subject    string    `json:"subject,omitempty"`
	Complete   bool      `json:"complete"`
	DueDate    time.Time `json:"due_date,omitzero"`
	Importance int       `json:"importance,omitempty"`
}

// TasksListOutput wraps the rows.
type TasksListOutput struct {
	Tasks         []TaskRow `json:"tasks"`
	MoreAvailable bool      `json:"more_available"`
	SyncCursor    string    `json:"sync_cursor"`
}

// TasksCreateInput is the schema for tasks_create.
type TasksCreateInput struct {
	Account    string    `json:"account"`
	FolderID   string    `json:"folder_id"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body,omitempty"`
	DueDate    time.Time `json:"due_date,omitzero"`
	StartDate  time.Time `json:"start_date,omitzero"`
	Importance int       `json:"importance,omitempty"`
}

// TasksUpdateInput is the schema for tasks_update; same as create + ID.
type TasksUpdateInput struct {
	TasksCreateInput
	ID string `json:"id"`
}

// TasksCompleteInput is the schema for tasks_complete.
type TasksCompleteInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
	ID       string `json:"id"`
}

// TasksDeleteInput is the schema for tasks_delete.
type TasksDeleteInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
	ID       string `json:"id"`
}

func registerTasksTools(s *mcp.Server, cfg *config.Config, m *Manager, accounts []string) {
	folderTool := &mcp.Tool{Name: "tasks_list_folders", Description: "List task folders for an account."}
	scopeEnum(folderTool, "account", accounts)
	mcp.AddTool(s, folderTool, func(ctx context.Context, _ *mcp.CallToolRequest, in TasksListFoldersInput) (*mcp.CallToolResult, TasksListFoldersOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, TasksListFoldersOutput{}, err
		}
		fs, err := c.FolderSync(ctx)
		if err != nil {
			return nil, TasksListFoldersOutput{}, fmt.Errorf("FolderSync: %w", err)
		}
		out := TasksListFoldersOutput{}
		for _, f := range fs.Added {
			if f.Type != eas.FolderTypeTasks && f.Type != eas.FolderTypeUserTasks {
				continue
			}
			out.Folders = append(out.Folders, TasksFolderRow{ID: f.ServerID, DisplayName: f.DisplayName})
		}
		return jsonResult(out)
	})

	listTool := &mcp.Tool{Name: "tasks_list", Description: "List tasks in a folder."}
	scopeEnum(listTool, "account", accounts)
	mcp.AddTool(s, listTool, func(ctx context.Context, _ *mcp.CallToolRequest, in TasksListInput) (*mcp.CallToolResult, TasksListOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, TasksListOutput{}, err
		}
		res, err := c.SyncTasks(ctx, in.FolderID)
		if err != nil {
			return nil, TasksListOutput{}, fmt.Errorf("SyncTasks: %w", err)
		}
		out := TasksListOutput{MoreAvailable: res.MoreAvailable, SyncCursor: res.SyncKey}
		for _, tk := range res.Added {
			out.Tasks = append(out.Tasks, taskRowFrom(tk))
		}
		for _, tk := range res.Changed {
			out.Tasks = append(out.Tasks, taskRowFrom(tk))
		}
		return jsonResult(out)
	})

	writers := cfg.WritableAccounts(config.ClassTasks)
	if len(writers) == 0 {
		return
	}
	mcp.AddTool(s, scopedTool("tasks_create", "Create a new task.", writers),
		func(ctx context.Context, _ *mcp.CallToolRequest, in TasksCreateInput) (*mcp.CallToolResult, IDOutput, error) {
			if err := m.CheckClass(in.Account, config.ClassTasks, true); err != nil {
				return nil, IDOutput{}, err
			}
			c, err := m.Client(ctx, in.Account)
			if err != nil {
				return nil, IDOutput{}, err
			}
			id, err := c.CreateTask(ctx, in.FolderID, draftFromTaskInput(in))
			if err != nil {
				return nil, IDOutput{}, fmt.Errorf("CreateTask: %w", err)
			}
			return jsonResult(IDOutput{ID: id})
		})
	mcp.AddTool(s, scopedTool("tasks_update", "Update an existing task.", writers),
		func(ctx context.Context, _ *mcp.CallToolRequest, in TasksUpdateInput) (*mcp.CallToolResult, IDOutput, error) {
			if err := m.CheckClass(in.Account, config.ClassTasks, true); err != nil {
				return nil, IDOutput{}, err
			}
			c, err := m.Client(ctx, in.Account)
			if err != nil {
				return nil, IDOutput{}, err
			}
			if err := c.UpdateTask(ctx, in.FolderID, in.ID, draftFromTaskInput(in.TasksCreateInput)); err != nil {
				return nil, IDOutput{}, fmt.Errorf("UpdateTask: %w", err)
			}
			return jsonResult(IDOutput{ID: in.ID})
		})
	mcp.AddTool(s, scopedTool("tasks_complete", "Mark a task complete (sets DateCompleted to now).", writers),
		func(ctx context.Context, _ *mcp.CallToolRequest, in TasksCompleteInput) (*mcp.CallToolResult, IDOutput, error) {
			if err := m.CheckClass(in.Account, config.ClassTasks, true); err != nil {
				return nil, IDOutput{}, err
			}
			c, err := m.Client(ctx, in.Account)
			if err != nil {
				return nil, IDOutput{}, err
			}
			if err := c.CompleteTask(ctx, in.FolderID, in.ID); err != nil {
				return nil, IDOutput{}, fmt.Errorf("CompleteTask: %w", err)
			}
			return jsonResult(IDOutput{ID: in.ID})
		})
	mcp.AddTool(s, scopedTool("tasks_delete", "Delete a task.", writers),
		func(ctx context.Context, _ *mcp.CallToolRequest, in TasksDeleteInput) (*mcp.CallToolResult, IDOutput, error) {
			if err := m.CheckClass(in.Account, config.ClassTasks, true); err != nil {
				return nil, IDOutput{}, err
			}
			c, err := m.Client(ctx, in.Account)
			if err != nil {
				return nil, IDOutput{}, err
			}
			if err := c.DeleteTask(ctx, in.FolderID, in.ID); err != nil {
				return nil, IDOutput{}, fmt.Errorf("DeleteTask: %w", err)
			}
			return jsonResult(IDOutput{ID: in.ID})
		})
}

func taskRowFrom(t eas.TaskItem) TaskRow {
	return TaskRow{ID: t.ServerID, Subject: t.Subject, Complete: t.Complete, DueDate: t.DueDate, Importance: t.Importance}
}

func draftFromTaskInput(in TasksCreateInput) eas.TaskDraft {
	return eas.TaskDraft{
		Subject: in.Subject, Body: in.Body,
		StartDate: in.StartDate, DueDate: in.DueDate,
		Importance: in.Importance,
	}
}

// --- Notes -----------------------------------------------------------------

// NotesListFoldersInput is the schema for notes_list_folders.
type NotesListFoldersInput struct {
	Account string `json:"account"`
}

// NotesFolderRow is one row in notes_list_folders.
type NotesFolderRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// NotesListFoldersOutput wraps the rows.
type NotesListFoldersOutput struct {
	Folders []NotesFolderRow `json:"folders"`
}

// NotesListInput is the schema for notes_list.
type NotesListInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
}

// NoteRow is one note in the response.
type NoteRow struct {
	ID           string    `json:"id"`
	Subject      string    `json:"subject,omitempty"`
	Body         string    `json:"body,omitempty"`
	LastModified time.Time `json:"last_modified,omitzero"`
	Categories   []string  `json:"categories,omitempty"`
}

// NotesListOutput wraps the rows.
type NotesListOutput struct {
	Notes         []NoteRow `json:"notes"`
	MoreAvailable bool      `json:"more_available"`
	SyncCursor    string    `json:"sync_cursor"`
}

// NotesCreateInput is the schema for notes_create.
type NotesCreateInput struct {
	Account    string   `json:"account"`
	FolderID   string   `json:"folder_id"`
	Subject    string   `json:"subject,omitempty"`
	Body       string   `json:"body"`
	BodyFormat string   `json:"body_format,omitempty" jsonschema:"plain (default) or html"`
	Categories []string `json:"categories,omitempty"`
}

// NotesUpdateInput is the schema for notes_update.
type NotesUpdateInput struct {
	NotesCreateInput
	ID string `json:"id"`
}

// NotesDeleteInput is the schema for notes_delete.
type NotesDeleteInput struct {
	Account  string `json:"account"`
	FolderID string `json:"folder_id"`
	ID       string `json:"id"`
}

func registerNotesTools(s *mcp.Server, cfg *config.Config, m *Manager, accounts []string) {
	folderTool := &mcp.Tool{Name: "notes_list_folders", Description: "List note folders for an account."}
	scopeEnum(folderTool, "account", accounts)
	mcp.AddTool(s, folderTool, func(ctx context.Context, _ *mcp.CallToolRequest, in NotesListFoldersInput) (*mcp.CallToolResult, NotesListFoldersOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, NotesListFoldersOutput{}, err
		}
		fs, err := c.FolderSync(ctx)
		if err != nil {
			return nil, NotesListFoldersOutput{}, fmt.Errorf("FolderSync: %w", err)
		}
		out := NotesListFoldersOutput{}
		for _, f := range fs.Added {
			if f.Type != eas.FolderTypeNotes && f.Type != eas.FolderTypeUserNotes {
				continue
			}
			out.Folders = append(out.Folders, NotesFolderRow{ID: f.ServerID, DisplayName: f.DisplayName})
		}
		return jsonResult(out)
	})

	listTool := &mcp.Tool{Name: "notes_list", Description: "List notes in a folder."}
	scopeEnum(listTool, "account", accounts)
	mcp.AddTool(s, listTool, func(ctx context.Context, _ *mcp.CallToolRequest, in NotesListInput) (*mcp.CallToolResult, NotesListOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, NotesListOutput{}, err
		}
		res, err := c.SyncNotes(ctx, in.FolderID)
		if err != nil {
			return nil, NotesListOutput{}, fmt.Errorf("SyncNotes: %w", err)
		}
		out := NotesListOutput{MoreAvailable: res.MoreAvailable, SyncCursor: res.SyncKey}
		for _, nt := range res.Added {
			out.Notes = append(out.Notes, noteRowFrom(nt))
		}
		for _, nt := range res.Changed {
			out.Notes = append(out.Notes, noteRowFrom(nt))
		}
		return jsonResult(out)
	})

	writers := cfg.WritableAccounts(config.ClassNotes)
	if len(writers) == 0 {
		return
	}
	mcp.AddTool(s, scopedTool("notes_create", "Create a new note.", writers),
		func(ctx context.Context, _ *mcp.CallToolRequest, in NotesCreateInput) (*mcp.CallToolResult, IDOutput, error) {
			if err := m.CheckClass(in.Account, config.ClassNotes, true); err != nil {
				return nil, IDOutput{}, err
			}
			c, err := m.Client(ctx, in.Account)
			if err != nil {
				return nil, IDOutput{}, err
			}
			id, err := c.CreateNote(ctx, in.FolderID, draftFromNoteInput(in))
			if err != nil {
				return nil, IDOutput{}, fmt.Errorf("CreateNote: %w", err)
			}
			return jsonResult(IDOutput{ID: id})
		})
	mcp.AddTool(s, scopedTool("notes_update", "Update an existing note.", writers),
		func(ctx context.Context, _ *mcp.CallToolRequest, in NotesUpdateInput) (*mcp.CallToolResult, IDOutput, error) {
			if err := m.CheckClass(in.Account, config.ClassNotes, true); err != nil {
				return nil, IDOutput{}, err
			}
			c, err := m.Client(ctx, in.Account)
			if err != nil {
				return nil, IDOutput{}, err
			}
			if err := c.UpdateNote(ctx, in.FolderID, in.ID, draftFromNoteInput(in.NotesCreateInput)); err != nil {
				return nil, IDOutput{}, fmt.Errorf("UpdateNote: %w", err)
			}
			return jsonResult(IDOutput{ID: in.ID})
		})
	mcp.AddTool(s, scopedTool("notes_delete", "Delete a note.", writers),
		func(ctx context.Context, _ *mcp.CallToolRequest, in NotesDeleteInput) (*mcp.CallToolResult, IDOutput, error) {
			if err := m.CheckClass(in.Account, config.ClassNotes, true); err != nil {
				return nil, IDOutput{}, err
			}
			c, err := m.Client(ctx, in.Account)
			if err != nil {
				return nil, IDOutput{}, err
			}
			if err := c.DeleteNote(ctx, in.FolderID, in.ID); err != nil {
				return nil, IDOutput{}, fmt.Errorf("DeleteNote: %w", err)
			}
			return jsonResult(IDOutput{ID: in.ID})
		})
}

func noteRowFrom(n eas.NoteItem) NoteRow {
	return NoteRow{
		ID: n.ServerID, Subject: n.Subject, Body: n.Body,
		LastModified: n.LastModifiedDate, Categories: n.Categories,
	}
}

func draftFromNoteInput(in NotesCreateInput) eas.NoteDraft {
	d := eas.NoteDraft{Subject: in.Subject, Body: in.Body, Categories: in.Categories}
	switch in.BodyFormat {
	case "html":
		d.BodyType = eas.BodyTypeHTML
	case "plain", "":
		d.BodyType = eas.BodyTypePlain
	}
	return d
}

// --- GAL search ------------------------------------------------------------

// GALSearchInput is the schema for gal_search.
type GALSearchInput struct {
	Account string `json:"account"`
	Query   string `json:"query" jsonschema:"directory query (free text)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max entries (default 25)"`
}

// GALEntryRow is one entry in gal_search output.
type GALEntryRow struct {
	DisplayName  string `json:"display_name,omitempty"`
	EmailAddress string `json:"email_address,omitempty"`
	Title        string `json:"title,omitempty"`
	Office       string `json:"office,omitempty"`
	Company      string `json:"company,omitempty"`
	Phone        string `json:"phone,omitempty"`
	MobilePhone  string `json:"mobile_phone,omitempty"`
}

// GALSearchOutput wraps the entries.
type GALSearchOutput struct {
	Entries []GALEntryRow `json:"entries"`
	Total   int           `json:"total"`
}

func registerGALSearch(s *mcp.Server, m *Manager, accounts []string) {
	tool := &mcp.Tool{
		Name: "gal_search",
		Description: "Search the corporate directory (Global Address List). " +
			"Useful for looking up someone's email by partial name or alias.",
	}
	scopeEnum(tool, "account", accounts)
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in GALSearchInput) (*mcp.CallToolResult, GALSearchOutput, error) {
		c, err := m.Client(ctx, in.Account)
		if err != nil {
			return nil, GALSearchOutput{}, err
		}
		res, err := c.GALSearch(ctx, in.Query, in.Limit)
		if err != nil {
			return nil, GALSearchOutput{}, fmt.Errorf("GALSearch: %w", err)
		}
		out := GALSearchOutput{Total: res.Total}
		for _, e := range res.Entries {
			out.Entries = append(out.Entries, GALEntryRow{
				DisplayName: e.DisplayName, EmailAddress: e.EmailAddress,
				Title: e.Title, Office: e.Office, Company: e.Company,
				Phone: e.Phone, MobilePhone: e.MobilePhone,
			})
		}
		return jsonResult(out)
	})
}

// --- helper ----------------------------------------------------------------

// scopedTool builds a Tool with name/description and applies scopeEnum.
// Reduces boilerplate across the many CRUD registrations above.
func scopedTool(name, desc string, accounts []string) *mcp.Tool {
	t := &mcp.Tool{Name: name, Description: desc}
	scopeEnum(t, "account", accounts)
	return t
}
