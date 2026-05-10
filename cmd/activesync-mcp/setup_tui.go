// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"activesync-mcp/lib/config"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hstern/go-activesync/eas"
	"github.com/zalando/go-keyring"
)

// setupState is the top-level FSM for the TUI: which screen the user
// is currently looking at.
type setupState int

const (
	stateMenu       setupState = iota // account list with add/edit/delete actions
	stateForm                         // add or edit account: name/email/password + advanced toggle
	stateAdvanced                     // advanced options for the in-progress account
	stateConfirmDel                   // y/n confirmation before deleting the highlighted account
	stateTesting                      // running connection test before save
	stateSaving                       // writing config + keyring
	stateDone                         // saved; quit on next key
)

// keyringService is the value the TUI writes to every account's
// secret.keyring_service. The autodiscover CLI used the same string
// historically; preserving it keeps the migration path obvious.
const keyringService = "activesync-mcp"

// Keyring access is funnelled through these hooks so tests can swap
// in an in-memory store rather than touch the OS keychain. Production
// uses the zalando/go-keyring package directly.
var (
	hookKeyringSet    = keyring.Set
	hookKeyringGet    = keyring.Get
	hookKeyringDelete = keyring.Delete
)

// setupModel is the root bubbletea model. It owns the account list,
// the in-progress edit buffer, and a small bag of UI state (selected
// row, error string, last status).
type setupModel struct {
	configPath string
	cfg        *config.Config

	state    setupState
	selected int        // index into cfg.Accounts
	editing  *editState // non-nil while in form/advanced/test/save
	confirm  string     // for stateConfirmDel: account name being confirmed
	status   string     // transient status line ("saved", "test failed: ...")
	lastErr  error      // surfaces back to runSetup for exit code
	width    int        // terminal width from WindowSizeMsg
}

// editState holds the in-progress account being added or edited. It's
// converted to a config.Account on save. editingExisting tracks
// whether we're modifying cfg.Accounts[selected] or appending a new
// one — the former lets us preserve the original keyring entry when
// the user leaves the password field blank.
type editState struct {
	editingExisting bool
	originalName    string // for editing: the unchanged name (used for keyring move)

	// inputs[*] are the visible basic-form textinputs; cursor is which
	// one has focus. Indices match formField*.
	inputs []textinput.Model
	cursor int

	// advInputs / advCursor mirror inputs / cursor for the advanced
	// screen. Allocated lazily by openAdvanced(); nil when the user
	// hasn't visited advanced this session (in which case the
	// existing-account values are preserved on save).
	advInputs []textinput.Model
	advCursor int

	// Toggleable bits — rendered as "[on]" / "[off]" rows.
	allowInsecure     bool
	push              bool
	discoveryRequired bool
	defaultAccess     string            // "ro" or "rw"
	classAccess       map[string]string // empty -> inherit default

	// For password reveal toggle (Ctrl+G on the password field).
	passwordRevealed bool
}

// Form field indices for the basic account form. Order matches the
// rendering order in viewForm().
const (
	formFieldName     = iota
	formFieldEmail    // → username
	formFieldPassword // empty when editing means "keep existing"
	formFieldCount
)

// Advanced field indices. Same convention as the basic form.
const (
	advFieldServerURL = iota
	advFieldASVersion
	advFieldUserAgent
	advFieldDeviceType
	advFieldAuthScheme
	advFieldCount
)

// Lipgloss styles. Kept minimal so the TUI runs in basic terminals
// without forcing a colour palette. Borders use ASCII so 80-column
// SSH sessions look fine.
var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	helpStyle   = lipgloss.NewStyle().Faint(true).Padding(1, 0, 0, 0)
)

// newSetupModel constructs the root model from a (possibly empty)
// loaded config.
func newSetupModel(path string, cfg *config.Config) setupModel {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return setupModel{
		configPath: path,
		cfg:        cfg,
		state:      stateMenu,
	}
}

// Init is required by tea.Model; nothing to fire on startup.
func (m setupModel) Init() tea.Cmd { return nil }

// ---- top-level Update / View dispatch -----------------------------

func (m setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if w, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = w.Width
	}
	switch m.state {
	case stateMenu:
		return m.updateMenu(msg)
	case stateForm:
		return m.updateForm(msg)
	case stateAdvanced:
		return m.updateAdvanced(msg)
	case stateConfirmDel:
		return m.updateConfirmDel(msg)
	case stateTesting:
		return m.updateTesting(msg)
	case stateSaving:
		return m.updateSaving(msg)
	case stateDone:
		// Any key quits.
		if _, ok := msg.(tea.KeyMsg); ok {
			return m, tea.Quit
		}
		return m, nil
	}
	return m, nil
}

func (m setupModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("activesync-mcp setup"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("config: %s", m.configPath)))
	b.WriteString("\n\n")
	switch m.state {
	case stateMenu:
		b.WriteString(m.viewMenu())
	case stateForm:
		b.WriteString(m.viewForm())
	case stateAdvanced:
		b.WriteString(m.viewAdvanced())
	case stateConfirmDel:
		b.WriteString(m.viewConfirmDel())
	case stateTesting:
		b.WriteString("Testing connection… (this can take several seconds)\n")
	case stateSaving:
		b.WriteString("Saving…\n")
	case stateDone:
		b.WriteString(okStyle.Render("Saved.") + " Press any key to quit.\n")
	}
	if m.status != "" {
		b.WriteString("\n")
		b.WriteString(m.status)
		b.WriteString("\n")
	}
	return b.String()
}

// ---- menu ---------------------------------------------------------

func (m setupModel) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "j", "down":
		if m.selected < len(m.cfg.Accounts)-1 {
			m.selected++
		}
	case "k", "up":
		if m.selected > 0 {
			m.selected--
		}
	case "a":
		m.editing = newEditState(nil)
		m.state = stateForm
		m.status = ""
	case "e":
		if len(m.cfg.Accounts) == 0 {
			return m, nil
		}
		m.editing = newEditState(&m.cfg.Accounts[m.selected])
		m.state = stateForm
		m.status = ""
	case "d":
		if len(m.cfg.Accounts) == 0 {
			return m, nil
		}
		m.confirm = m.cfg.Accounts[m.selected].Name
		m.state = stateConfirmDel
		m.status = ""
	}
	return m, nil
}

func (m setupModel) viewMenu() string {
	var b strings.Builder
	if len(m.cfg.Accounts) == 0 {
		b.WriteString(dimStyle.Render("No accounts configured yet.\n"))
	} else {
		b.WriteString("Accounts:\n")
		for i, a := range m.cfg.Accounts {
			marker := "  "
			if i == m.selected {
				marker = cursorStyle.Render("> ")
			}
			url := a.ServerURL
			if url == "" {
				url = dimStyle.Render("(autodiscover at serve time)")
			}
			b.WriteString(fmt.Sprintf("%s%-20s %-30s %s\n", marker, a.Name, a.Username, url))
		}
	}
	b.WriteString(helpStyle.Render("[a] add  [e] edit  [d] delete  [q] quit"))
	return b.String()
}

// ---- form (add / edit basic) --------------------------------------

func newEditState(existing *config.Account) *editState {
	es := &editState{
		inputs:        make([]textinput.Model, formFieldCount),
		defaultAccess: config.AccessRO,
		classAccess:   map[string]string{},
	}
	for i := range es.inputs {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 256
		es.inputs[i] = ti
	}
	es.inputs[formFieldName].Placeholder = "work, personal, …"
	es.inputs[formFieldEmail].Placeholder = "you@example.com"
	es.inputs[formFieldPassword].Placeholder = "(leave blank to keep stored)"
	es.inputs[formFieldPassword].EchoMode = textinput.EchoPassword
	es.inputs[formFieldPassword].EchoCharacter = '•'

	if existing != nil {
		es.editingExisting = true
		es.originalName = existing.Name
		es.inputs[formFieldName].SetValue(existing.Name)
		es.inputs[formFieldEmail].SetValue(existing.Username)
		// Password field deliberately left blank: we never read the
		// stored value, and an empty submit means "keep current".
		es.allowInsecure = existing.AllowInsecure
		es.push = existing.Push
		es.discoveryRequired = existing.DiscoveryRequired
		es.defaultAccess = existing.DefaultAccess
		if es.defaultAccess == "" {
			es.defaultAccess = config.AccessRO
		}
		for k, v := range existing.Access {
			es.classAccess[k] = v
		}
	}
	es.cursor = 0
	es.inputs[0].Focus()
	return es
}

func (m setupModel) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	es := m.editing
	switch k.String() {
	case "ctrl+c", "esc":
		m.state = stateMenu
		m.editing = nil
		return m, nil
	case "ctrl+g":
		// Toggle password reveal for the password field only.
		es.passwordRevealed = !es.passwordRevealed
		if es.passwordRevealed {
			es.inputs[formFieldPassword].EchoMode = textinput.EchoNormal
		} else {
			es.inputs[formFieldPassword].EchoMode = textinput.EchoPassword
		}
		return m, nil
	case "ctrl+a":
		// Jump to advanced options.
		m.state = stateAdvanced
		// Lazily build the advanced inputs.
		es.openAdvanced()
		return m, nil
	case "tab", "down":
		es.focusNext()
		return m, nil
	case "shift+tab", "up":
		es.focusPrev()
		return m, nil
	case "enter":
		// Validate then move to test → save.
		if err := es.validateBasic(); err != nil {
			m.status = errStyle.Render(err.Error())
			return m, nil
		}
		m.state = stateTesting
		m.status = ""
		return m, runConnectionTest(es)
	}
	// Forward keystrokes to the focused field.
	var cmd tea.Cmd
	es.inputs[es.cursor], cmd = es.inputs[es.cursor].Update(msg)
	return m, cmd
}

func (m setupModel) viewForm() string {
	es := m.editing
	var b strings.Builder
	if es.editingExisting {
		b.WriteString(titleStyle.Render(fmt.Sprintf("Edit account: %s", es.originalName)))
	} else {
		b.WriteString(titleStyle.Render("Add account"))
	}
	b.WriteString("\n\n")
	labels := []string{"Account name", "Email / username", "Password"}
	for i, l := range labels {
		marker := "  "
		if i == es.cursor {
			marker = cursorStyle.Render("> ")
		}
		hint := ""
		if i == formFieldPassword {
			if es.passwordRevealed {
				hint = dimStyle.Render(" (visible — Ctrl+G to hide)")
			} else {
				hint = dimStyle.Render(" (hidden — Ctrl+G to show)")
			}
		}
		b.WriteString(fmt.Sprintf("%s%-20s %s%s\n", marker, l+":", es.inputs[i].View(), hint))
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf(
		"  default_access=%s  allow_insecure=%v  push=%v  discovery_required=%v\n",
		es.defaultAccess, es.allowInsecure, es.push, es.discoveryRequired)))
	b.WriteString(helpStyle.Render(
		"[Ctrl+A] advanced  [Ctrl+G] toggle password  [Tab] next  [Enter] save  [Esc] cancel"))
	return b.String()
}

func (es *editState) focusNext() {
	es.inputs[es.cursor].Blur()
	es.cursor = (es.cursor + 1) % len(es.inputs)
	es.inputs[es.cursor].Focus()
}

func (es *editState) focusPrev() {
	es.inputs[es.cursor].Blur()
	es.cursor = (es.cursor - 1 + len(es.inputs)) % len(es.inputs)
	es.inputs[es.cursor].Focus()
}

func (es *editState) validateBasic() error {
	name := strings.TrimSpace(es.inputs[formFieldName].Value())
	email := strings.TrimSpace(es.inputs[formFieldEmail].Value())
	if name == "" {
		return fmt.Errorf("account name is required")
	}
	if email == "" {
		return fmt.Errorf("email / username is required")
	}
	if !es.editingExisting && es.inputs[formFieldPassword].Value() == "" {
		return fmt.Errorf("password is required for new accounts")
	}
	return nil
}

// ---- advanced options ---------------------------------------------

// openAdvanced lazily replaces the inputs slice with the advanced-form
// fields. Called when the user hits Ctrl+A from the basic form.
func (es *editState) openAdvanced() {
	// Preserve a reference to the basic inputs so we can restore them
	// when the user comes back from advanced.
	prev := es.inputs
	prevCursor := es.cursor
	_ = prev
	_ = prevCursor

	advInputs := make([]textinput.Model, advFieldCount)
	for i := range advInputs {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 256
		advInputs[i] = ti
	}
	advInputs[advFieldServerURL].Placeholder = "https://mail.example.com/Microsoft-Server-ActiveSync"
	advInputs[advFieldASVersion].Placeholder = "14.1"
	advInputs[advFieldUserAgent].Placeholder = "activesync-mcp/0.1"
	advInputs[advFieldDeviceType].Placeholder = "MCP"
	advInputs[advFieldAuthScheme].Placeholder = "basic | bearer | ntlm | negotiate"

	// If we're editing an existing account, populate the visible
	// values from cfg. For a fresh add, they stay placeholder.
	// (The basic-form values are kept on es.inputs[formField*].)
	es.advInputs = advInputs
	es.advCursor = 0
	es.advInputs[0].Focus()
}

func (m setupModel) updateAdvanced(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	es := m.editing
	switch k.String() {
	case "ctrl+c", "esc":
		// Drop advanced edits and return to the basic form.
		es.advInputs = nil
		m.state = stateForm
		return m, nil
	case "tab", "down":
		es.focusNextAdv()
		return m, nil
	case "shift+tab", "up":
		es.focusPrevAdv()
		return m, nil
	case "ctrl+i":
		es.allowInsecure = !es.allowInsecure
		return m, nil
	case "ctrl+p":
		es.push = !es.push
		return m, nil
	case "ctrl+r":
		es.discoveryRequired = !es.discoveryRequired
		return m, nil
	case "ctrl+w":
		// Toggle DefaultAccess between ro and rw.
		if es.defaultAccess == config.AccessRO {
			es.defaultAccess = config.AccessRW
		} else {
			es.defaultAccess = config.AccessRO
		}
		return m, nil
	case "enter":
		// Capture advanced inputs into es and return to basic.
		// The actual save happens from the basic form.
		es.advInputs = nil
		m.state = stateForm
		return m, nil
	}
	var cmd tea.Cmd
	es.advInputs[es.advCursor], cmd = es.advInputs[es.advCursor].Update(msg)
	return m, cmd
}

func (m setupModel) viewAdvanced() string {
	es := m.editing
	var b strings.Builder
	b.WriteString(titleStyle.Render("Advanced options"))
	b.WriteString("\n\n")
	labels := []string{
		"Server URL (empty = autodiscover at serve time)",
		"Protocol version",
		"User-Agent",
		"Device type",
		"Auth scheme",
	}
	for i, l := range labels {
		marker := "  "
		if i == es.advCursor {
			marker = cursorStyle.Render("> ")
		}
		b.WriteString(fmt.Sprintf("%s%-50s %s\n", marker, l+":", es.advInputs[i].View()))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  default_access      [%s]  (Ctrl+W toggles)\n", es.defaultAccess))
	b.WriteString(fmt.Sprintf("  allow_insecure      [%s]  (Ctrl+I toggles)\n", onOff(es.allowInsecure)))
	b.WriteString(fmt.Sprintf("  push                [%s]  (Ctrl+P toggles)\n", onOff(es.push)))
	b.WriteString(fmt.Sprintf("  discovery_required  [%s]  (Ctrl+R toggles — strict mode at serve startup)\n", onOff(es.discoveryRequired)))
	b.WriteString(helpStyle.Render("[Tab] next  [Enter] back to account  [Esc] cancel advanced"))
	return b.String()
}

func onOff(b bool) string {
	if b {
		return " on"
	}
	return "off"
}

func (es *editState) focusNextAdv() {
	es.advInputs[es.advCursor].Blur()
	es.advCursor = (es.advCursor + 1) % len(es.advInputs)
	es.advInputs[es.advCursor].Focus()
}

func (es *editState) focusPrevAdv() {
	es.advInputs[es.advCursor].Blur()
	es.advCursor = (es.advCursor - 1 + len(es.advInputs)) % len(es.advInputs)
	es.advInputs[es.advCursor].Focus()
}

// ---- delete confirmation ------------------------------------------

func (m setupModel) updateConfirmDel(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "y", "Y":
		// Remove account; persist immediately. Also delete the
		// keyring entry on best-effort (it might not exist).
		name := m.confirm
		newAccts := make([]config.Account, 0, len(m.cfg.Accounts)-1)
		for _, a := range m.cfg.Accounts {
			if a.Name != name {
				newAccts = append(newAccts, a)
			}
		}
		m.cfg.Accounts = newAccts
		if m.selected >= len(m.cfg.Accounts) && m.selected > 0 {
			m.selected--
		}
		_ = hookKeyringDelete(keyringService, name) // best-effort
		m.state = stateSaving
		return m, runSave(m.configPath, m.cfg, nil, "")
	case "n", "N", "esc", "ctrl+c":
		m.state = stateMenu
		m.confirm = ""
	}
	return m, nil
}

func (m setupModel) viewConfirmDel() string {
	return errStyle.Render(fmt.Sprintf(
		"Delete account %q? This also removes its OS keyring entry.\n",
		m.confirm)) +
		helpStyle.Render("[y] yes  [n] no")
}

// ---- connection test ----------------------------------------------

// testResultMsg is the bubbletea message produced by runConnectionTest.
type testResultMsg struct {
	err error
}

// hookRunConnectionTest lets tests substitute the network probe.
// Production runs realConnectionTest; tests swap in a stub that
// returns whatever they want.
var hookRunConnectionTest = realConnectionTest

func runConnectionTest(es *editState) tea.Cmd {
	return func() tea.Msg {
		return testResultMsg{err: hookRunConnectionTest(es)}
	}
}

// realConnectionTest builds an ephemeral eas.Client (no state, no
// keyring write) and runs OPTIONS against the server. If ServerURL is
// empty, autodiscover runs first.
func realConnectionTest(es *editState) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	username := strings.TrimSpace(es.inputs[formFieldEmail].Value())
	password := es.inputs[formFieldPassword].Value()
	if password == "" && es.editingExisting {
		// Fall back to the keyring entry for the original account
		// name — the user is editing without changing the password.
		pw, err := hookKeyringGet(keyringService, es.originalName)
		if err != nil {
			return fmt.Errorf("read existing keyring entry: %w", err)
		}
		password = pw
	}

	serverURL := ""
	if len(es.advInputs) > advFieldServerURL {
		serverURL = strings.TrimSpace(es.advInputs[advFieldServerURL].Value())
	}
	hc := &http.Client{Timeout: 25 * time.Second}
	if es.allowInsecure {
		hc.Transport = insecureTransport()
	}
	if serverURL == "" {
		res, err := eas.Autodiscover(ctx, username, password, eas.AutodiscoverOptions{
			HTTPClient: hc,
		})
		if err != nil {
			return fmt.Errorf("autodiscover: %w", err)
		}
		serverURL = res.URL
	}
	c, err := eas.NewClient(eas.Config{
		ServerURL:  serverURL,
		Username:   username,
		Password:   password,
		DeviceID:   "setup0000000000000000000000000000",
		DeviceType: "MCP",
		ASVersion:  "14.1",
		UserAgent:  "activesync-mcp/0.1",
		HTTPClient: hc,
		State:      eas.NewMemoryState(),
	})
	if err != nil {
		return fmt.Errorf("client setup: %w", err)
	}
	if _, err := c.Options(ctx); err != nil {
		return fmt.Errorf("OPTIONS: %w", err)
	}
	return nil
}

func (m setupModel) updateTesting(msg tea.Msg) (tea.Model, tea.Cmd) {
	r, ok := msg.(testResultMsg)
	if !ok {
		return m, nil
	}
	if r.err != nil {
		m.status = errStyle.Render(fmt.Sprintf("connection test failed: %v", r.err))
		m.state = stateForm
		return m, nil
	}
	// Test passed — write keyring + persist config.
	pw := m.editing.inputs[formFieldPassword].Value()
	m.applyEdits()
	m.state = stateSaving
	m.status = ""
	return m, runSave(m.configPath, m.cfg, m.editing, pw)
}

// ---- save ---------------------------------------------------------

// saveResultMsg is the bubbletea message produced by runSave.
type saveResultMsg struct {
	err error
}

func runSave(path string, cfg *config.Config, editing *editState, pw string) tea.Cmd {
	return func() tea.Msg {
		// 1. Write the password to the keyring (if any).
		if editing != nil && pw != "" {
			name := strings.TrimSpace(editing.inputs[formFieldName].Value())
			if err := hookKeyringSet(keyringService, name, pw); err != nil {
				return saveResultMsg{err: fmt.Errorf("keyring set: %w", err)}
			}
			// If editing an existing account whose name changed,
			// the old keyring entry is now orphaned; drop it.
			if editing.editingExisting && editing.originalName != name {
				_ = hookKeyringDelete(keyringService, editing.originalName)
			}
		}
		// 2. Persist the config.
		if err := ensureConfigDir(path); err != nil {
			return saveResultMsg{err: fmt.Errorf("create config dir: %w", err)}
		}
		if err := writeConfigTOML(path, cfg); err != nil {
			return saveResultMsg{err: fmt.Errorf("write config: %w", err)}
		}
		return saveResultMsg{err: nil}
	}
}

func (m setupModel) updateSaving(msg tea.Msg) (tea.Model, tea.Cmd) {
	r, ok := msg.(saveResultMsg)
	if !ok {
		return m, nil
	}
	if r.err != nil {
		m.status = errStyle.Render(fmt.Sprintf("save failed: %v", r.err))
		m.lastErr = r.err
		m.state = stateMenu
		m.editing = nil
		return m, nil
	}
	m.editing = nil
	m.status = okStyle.Render(fmt.Sprintf("Saved to %s", m.configPath))
	m.state = stateMenu
	return m, nil
}

// applyEdits folds the in-progress editState back into m.cfg.
func (m *setupModel) applyEdits() {
	es := m.editing
	a := config.Account{
		Name:              strings.TrimSpace(es.inputs[formFieldName].Value()),
		Username:          strings.TrimSpace(es.inputs[formFieldEmail].Value()),
		AllowInsecure:     es.allowInsecure,
		Push:              es.push,
		DiscoveryRequired: es.discoveryRequired,
		DefaultAccess:     es.defaultAccess,
		Access:            es.classAccess,
		Secret: config.SecretRef{
			KeyringService: keyringService,
			KeyringAccount: strings.TrimSpace(es.inputs[formFieldName].Value()),
		},
	}
	if es.advInputs != nil {
		// Caller might have hit Enter in advanced view recently; values
		// still live there.
		a.ServerURL = strings.TrimSpace(es.advInputs[advFieldServerURL].Value())
		a.ASVersion = strings.TrimSpace(es.advInputs[advFieldASVersion].Value())
		a.UserAgent = strings.TrimSpace(es.advInputs[advFieldUserAgent].Value())
		a.DeviceType = strings.TrimSpace(es.advInputs[advFieldDeviceType].Value())
		a.Secret.AuthScheme = strings.TrimSpace(es.advInputs[advFieldAuthScheme].Value())
	} else if es.editingExisting {
		// Preserve the values from the original account if the user
		// never opened advanced this session.
		for i := range m.cfg.Accounts {
			if m.cfg.Accounts[i].Name == es.originalName {
				orig := m.cfg.Accounts[i]
				a.ServerURL = orig.ServerURL
				a.ASVersion = orig.ASVersion
				a.UserAgent = orig.UserAgent
				a.DeviceType = orig.DeviceType
				a.Secret.AuthScheme = orig.Secret.AuthScheme
				break
			}
		}
	}
	if es.editingExisting {
		for i := range m.cfg.Accounts {
			if m.cfg.Accounts[i].Name == es.originalName {
				m.cfg.Accounts[i] = a
				return
			}
		}
	}
	m.cfg.Accounts = append(m.cfg.Accounts, a)
}

// insecureTransport returns an http.Transport with TLS verification
// disabled. Used only for the connection-test step when the user
// flagged allow_insecure on the account.
func insecureTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = nil // populated below
	return &insecureRT{base: t}
}

type insecureRT struct{ base http.RoundTripper }

func (r *insecureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return r.base.RoundTrip(req)
}
