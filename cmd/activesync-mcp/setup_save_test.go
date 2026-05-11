// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"activesync-mcp/lib/config"
)

// emit/decode round-trip is the cheapest way to assert that the TUI
// can write configs that the loader will accept. If the loader's
// schema drifts, this test breaks the same day.

func TestEmitConfigTOML_keyringRoundTrip(t *testing.T) {
	in := &config.Config{
		Accounts: []config.Account{{
			Name:      "work",
			Username:  "henry@example.com",
			ServerURL: "https://mail.example.com/Microsoft-Server-ActiveSync",
			Push:      true,
			Secret: config.SecretRef{
				KeyringService: "activesync-mcp",
				KeyringAccount: "work",
			},
			Access: map[string]string{
				"calendar": config.AccessRW,
			},
			DefaultAccess: config.AccessRO,
		}},
	}
	body := emitConfigTOML(in)
	got, err := config.Decode(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode emitted config: %v\n--- emitted ---\n%s", err, body)
	}
	if len(got.Accounts) != 1 {
		t.Fatalf("len(accounts) = %d", len(got.Accounts))
	}
	a := got.Accounts[0]
	if a.Name != "work" || a.Username != "henry@example.com" || a.ServerURL != in.Accounts[0].ServerURL {
		t.Errorf("scalar fields: %+v", a)
	}
	if !a.Push {
		t.Error("Push not preserved")
	}
	if a.Secret.KeyringService != "activesync-mcp" || a.Secret.KeyringAccount != "work" {
		t.Errorf("Secret: %+v", a.Secret)
	}
	if a.Access["calendar"] != config.AccessRW {
		t.Errorf("Access: %v", a.Access)
	}
}

func TestEmitConfigTOML_emptyServerURLPreserved(t *testing.T) {
	// The whole point of the new schema: ServerURL=="" is valid and
	// triggers serve-time autodiscover. The emitter must not synthesise
	// a value, and the comment line must be preserved.
	in := &config.Config{
		Accounts: []config.Account{{
			Name:     "work",
			Username: "henry@example.com",
			Secret:   config.SecretRef{KeyringService: "activesync-mcp", KeyringAccount: "work"},
		}},
	}
	body := emitConfigTOML(in)
	if !strings.Contains(body, "autodiscover at serve time") {
		t.Errorf("expected explanatory comment for empty ServerURL:\n%s", body)
	}
	got, err := config.Decode(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Accounts[0].ServerURL != "" {
		t.Errorf("ServerURL = %q, want empty", got.Accounts[0].ServerURL)
	}
}

func TestEmitConfigTOML_commandSecret(t *testing.T) {
	// If the existing config used a `secret = { command = [...] }`
	// account (rather than keyring), the emitter must preserve that
	// form rather than overwriting it with a keyring entry.
	in := &config.Config{
		Accounts: []config.Account{{
			Name:      "work",
			Username:  "u",
			ServerURL: "https://x",
			Secret: config.SecretRef{
				Command: []string{"pass", "show", "mail/work"},
			},
		}},
	}
	body := emitConfigTOML(in)
	if !strings.Contains(body, `command = ["pass", "show", "mail/work"]`) {
		t.Errorf("command secret not preserved:\n%s", body)
	}
}

func TestEmitConfigTOML_authSchemeBearer(t *testing.T) {
	in := &config.Config{
		Accounts: []config.Account{{
			Name: "x", Username: "u", ServerURL: "https://x",
			Secret: config.SecretRef{
				KeyringService: "activesync-mcp",
				KeyringAccount: "x",
				AuthScheme:     "bearer",
			},
		}},
	}
	body := emitConfigTOML(in)
	if !strings.Contains(body, `auth_scheme = "bearer"`) {
		t.Errorf("auth_scheme not emitted:\n%s", body)
	}
	got, _ := config.Decode(strings.NewReader(body))
	if got.Accounts[0].Secret.AuthScheme != "bearer" {
		t.Errorf("auth_scheme = %q", got.Accounts[0].Secret.AuthScheme)
	}
}

func TestEmitConfigTOML_skipsDefaults(t *testing.T) {
	// Default values should NOT be written — the emitted file stays
	// minimal so a hand-edit later doesn't have to slog through the
	// default values just to spot the meaningful ones.
	in := &config.Config{
		LogLevel: "info", // default
		Accounts: []config.Account{{
			Name: "x", Username: "u", ServerURL: "https://x",
			DeviceType:    "MCP",                // default
			ASVersion:     "14.1",               // default
			UserAgent:     "activesync-mcp/0.1", // default
			DefaultAccess: config.AccessRO,      // default
			Secret:        config.SecretRef{KeyringService: "activesync-mcp", KeyringAccount: "x"},
		}},
	}
	body := emitConfigTOML(in)
	for _, shouldNotContain := range []string{
		`log_level`,
		`device_type`,
		`as_version`,
		`user_agent`,
		`default_access`,
	} {
		if strings.Contains(body, shouldNotContain) {
			t.Errorf("default value emitted (%q):\n%s", shouldNotContain, body)
		}
	}
}

func TestWriteConfigTOML_atomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name: "x", Username: "u", ServerURL: "https://x",
			Secret: config.SecretRef{KeyringService: "activesync-mcp", KeyringAccount: "x"},
		}},
	}
	if err := writeConfigTOML(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Name != "x" {
		t.Errorf("round-trip lost accounts: %+v", got.Accounts)
	}
}

func TestTOMLString_quotesSpecialChars(t *testing.T) {
	cases := map[string]string{
		"plain":           `"plain"`,
		`with "quote"`:    `"with \"quote\""`,
		`back\slash`:      `"back\\slash"`,
		"control\x07char": `"control\acontrol char"`, // strconv.Quote will escape \a; we just check it's a quoted form
	}
	for in := range cases {
		got := tomlString(in)
		if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
			t.Errorf("tomlString(%q) = %q (must be quoted)", in, got)
		}
	}
}
