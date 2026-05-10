package config

// Class enumerates the EAS data classes whose write access can be
// independently gated.
type Class string

const (
	ClassEmail    Class = "email"
	ClassCalendar Class = "calendar"
	ClassContacts Class = "contacts"
	ClassTasks    Class = "tasks"
	ClassNotes    Class = "notes"
)

// AllClasses returns the canonical list of gated classes.
func AllClasses() []Class {
	return []Class{ClassEmail, ClassCalendar, ClassContacts, ClassTasks, ClassNotes}
}

// CanWrite reports whether this account permits writes for class c.
//
// Resolution: per-class override in [account.access] wins; otherwise
// default_access (which itself defaults to "ro" — see applyDefaults).
func (a *Account) CanWrite(c Class) bool {
	if v, ok := a.Access[string(c)]; ok {
		return v == AccessRW
	}
	return a.DefaultAccess == AccessRW
}

// WritableAccounts returns the names of accounts that permit writes for c.
// Used at MCP tool registration to scope each write tool's `account` enum.
func (c *Config) WritableAccounts(class Class) []string {
	out := make([]string, 0, len(c.Accounts))
	for i := range c.Accounts {
		if c.Accounts[i].CanWrite(class) {
			out = append(out, c.Accounts[i].Name)
		}
	}
	return out
}

// AccountNames returns every configured account name in declaration order.
func (c *Config) AccountNames() []string {
	out := make([]string, len(c.Accounts))
	for i := range c.Accounts {
		out[i] = c.Accounts[i].Name
	}
	return out
}
