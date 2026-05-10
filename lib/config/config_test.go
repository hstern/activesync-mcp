package config

import (
	"strings"
	"testing"
)

func TestDecode_minimalKeyringAccount(t *testing.T) {
	src := `
[[account]]
name       = "work"
server_url = "https://mail.example.com/Microsoft-Server-ActiveSync"
username   = "henry"
secret     = { keyring_service = "activesync-mcp", keyring_account = "work" }
`
	c, err := Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(c.Accounts) != 1 {
		t.Fatalf("want 1 account, got %d", len(c.Accounts))
	}
	a := c.Accounts[0]
	if a.Name != "work" {
		t.Errorf("name: %q", a.Name)
	}
	if a.DefaultAccess != AccessRO {
		t.Errorf("default_access default: %q, want %q", a.DefaultAccess, AccessRO)
	}
	if a.DeviceType != "MCP" {
		t.Errorf("device_type default: %q", a.DeviceType)
	}
	if a.ASVersion != "14.1" {
		t.Errorf("as_version default: %q", a.ASVersion)
	}
	if c.LogLevel != "info" {
		t.Errorf("log_level default: %q", c.LogLevel)
	}
}

func TestDecode_perClassAccess(t *testing.T) {
	src := `
[[account]]
name       = "work"
server_url = "https://x"
username   = "u"
secret     = { command = ["echo", "hi"] }
default_access = "ro"

[account.access]
calendar = "rw"
tasks    = "rw"
`
	c, err := Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a := c.Accounts[0]
	if a.Access["calendar"] != AccessRW {
		t.Errorf("calendar access: %q", a.Access["calendar"])
	}
	if a.Access["tasks"] != AccessRW {
		t.Errorf("tasks access: %q", a.Access["tasks"])
	}
}

func TestDecode_unknownPasswordKeyIgnored(t *testing.T) {
	// The schema has no password field; an unknown TOML key is ignored
	// rather than rejected. (We rely on absence-from-schema as the
	// no-cleartext-secrets enforcement.)
	src := `
[[account]]
name       = "work"
server_url = "https://x"
username   = "u"
password   = "should-be-ignored"
secret     = { command = ["echo", "hi"] }
`
	if _, err := Decode(strings.NewReader(src)); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestDecode_validationErrors(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "no accounts",
			src:     ``,
			wantSub: "no [[account]]",
		},
		{
			name: "missing name",
			src: `
[[account]]
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }`,
			wantSub: "name is required",
		},
		{
			name: "duplicate name",
			src: `
[[account]]
name = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }
[[account]]
name = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }`,
			wantSub: "duplicate name",
		},
		{
			name: "missing server_url",
			src: `
[[account]]
name     = "a"
username = "u"
secret   = { command = ["x"] }`,
			wantSub: "server_url is required",
		},
		{
			name: "missing username",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
secret     = { command = ["x"] }`,
			wantSub: "username is required",
		},
		{
			name: "no secret source",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"`,
			wantSub: "must set either",
		},
		{
			name: "secret keyring partial",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { keyring_service = "svc" }`,
			wantSub: "keyring requires both",
		},
		{
			name: "secret both sources",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { keyring_service = "s", keyring_account = "a", command = ["x"] }`,
			wantSub: "mutually exclusive",
		},
		{
			name: "bad default_access",
			src: `
[[account]]
name           = "a"
server_url     = "https://x"
username       = "u"
secret         = { command = ["x"] }
default_access = "yes"`,
			wantSub: "default_access must be",
		},
		{
			name: "unknown access class",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }
[account.access]
mail = "rw"`,
			wantSub: "unknown class",
		},
		{
			name: "bad access value",
			src: `
[[account]]
name       = "a"
server_url = "https://x"
username   = "u"
secret     = { command = ["x"] }
[account.access]
email = "write"`,
			wantSub: "must be",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tc.src))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestFindAccount(t *testing.T) {
	c := &Config{
		Accounts: []Account{
			{Name: "a"},
			{Name: "b"},
		},
	}
	if c.FindAccount("a") == nil {
		t.Errorf("a not found")
	}
	if c.FindAccount("b") == nil {
		t.Errorf("b not found")
	}
	if c.FindAccount("c") != nil {
		t.Errorf("c should not be found")
	}
}

func TestExpandHome(t *testing.T) {
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path mangled: %q", got)
	}
	got := expandHome("~/foo")
	if strings.HasPrefix(got, "~") {
		t.Errorf("home not expanded: %q", got)
	}
}
