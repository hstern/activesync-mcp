// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"activesync-mcp/lib/config"
)

// writeConfigTOML serialises cfg to a TOML document and writes it
// atomically to path. Atomicity = write to a sibling tmp file then
// rename: a crashing setup process can't leave a half-written
// config.toml that a subsequent serve would refuse to load.
//
// We hand-roll the emitter rather than using BurntSushi/toml's
// Encoder so the layout matches the autodiscover-CLI output we used
// to print, and so we can include explanatory comments. Comment
// preservation across edits is intentionally not attempted — the TUI
// is the source of truth for accounts, and config.toml is rewritten
// in full on every save.
func writeConfigTOML(path string, cfg *config.Config) error {
	body := emitConfigTOML(cfg)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func emitConfigTOML(cfg *config.Config) string {
	var b strings.Builder
	b.WriteString("# activesync-mcp configuration. Edit via `activesync-mcp setup`;\n")
	b.WriteString("# this file is rewritten in full on every save (comments will be lost).\n\n")

	if cfg.StateDir != "" {
		b.WriteString("state_dir = " + tomlString(cfg.StateDir) + "\n")
	}
	if cfg.LogLevel != "" && cfg.LogLevel != "info" {
		b.WriteString("log_level = " + tomlString(cfg.LogLevel) + "\n")
	}
	if cfg.StateDir != "" || (cfg.LogLevel != "" && cfg.LogLevel != "info") {
		b.WriteString("\n")
	}

	for i := range cfg.Accounts {
		writeAccountTOML(&b, &cfg.Accounts[i])
	}
	return b.String()
}

func writeAccountTOML(b *strings.Builder, a *config.Account) {
	b.WriteString("[[account]]\n")
	b.WriteString("name       = " + tomlString(a.Name) + "\n")
	b.WriteString("username   = " + tomlString(a.Username) + "\n")
	if a.ServerURL != "" {
		b.WriteString("server_url = " + tomlString(a.ServerURL) + "\n")
	} else {
		b.WriteString("# server_url left empty: autodiscover at serve time\n")
	}
	if a.ASVersion != "" && a.ASVersion != "14.1" {
		b.WriteString("as_version = " + tomlString(a.ASVersion) + "\n")
	}
	if a.UserAgent != "" && a.UserAgent != "activesync-mcp/0.1" {
		b.WriteString("user_agent = " + tomlString(a.UserAgent) + "\n")
	}
	if a.DeviceType != "" && a.DeviceType != "MCP" {
		b.WriteString("device_type = " + tomlString(a.DeviceType) + "\n")
	}
	if a.AllowInsecure {
		b.WriteString("allow_insecure = true\n")
	}
	if a.Push {
		b.WriteString("push = true\n")
	}
	if a.DiscoveryRequired {
		b.WriteString("discovery_required = true\n")
	}
	if a.DefaultAccess != "" && a.DefaultAccess != config.AccessRO {
		b.WriteString("default_access = " + tomlString(a.DefaultAccess) + "\n")
	}
	// secret block — keyring-based by convention from the TUI; if
	// the file already had a command-based secret we preserve the
	// command form.
	switch {
	case len(a.Secret.Command) > 0:
		b.WriteString("secret     = { command = [")
		for i, s := range a.Secret.Command {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tomlString(s))
		}
		b.WriteString("]")
		if a.Secret.AuthScheme != "" {
			b.WriteString(", auth_scheme = " + tomlString(a.Secret.AuthScheme))
		}
		b.WriteString(" }\n")
	default:
		b.WriteString("secret     = { keyring_service = " + tomlString(a.Secret.KeyringService) +
			", keyring_account = " + tomlString(a.Secret.KeyringAccount))
		if a.Secret.AuthScheme != "" {
			b.WriteString(", auth_scheme = " + tomlString(a.Secret.AuthScheme))
		}
		b.WriteString(" }\n")
	}
	if len(a.Access) > 0 {
		b.WriteString("\n[account.access]\n")
		// Sort keys for deterministic output.
		keys := []string{"email", "calendar", "contacts", "tasks", "notes"}
		for _, k := range keys {
			if v, ok := a.Access[k]; ok {
				b.WriteString(fmt.Sprintf("%-9s = %s\n", k, tomlString(v)))
			}
		}
	}
	b.WriteString("\n")
}

// tomlString quotes s as a TOML basic string. We escape the bare
// minimum: double-quote, backslash, control chars. Anything fancier
// gets the strconv.Quote treatment.
func tomlString(s string) string {
	if needsRawEscape(s) {
		return strconv.Quote(s)
	}
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func needsRawEscape(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
