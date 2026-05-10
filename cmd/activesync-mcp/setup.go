// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"activesync-mcp/lib/config"

	tea "github.com/charmbracelet/bubbletea"
)

// runSetup is the user-facing config bootstrap subcommand. It opens an
// interactive TUI for adding/editing/deleting accounts and writes the
// result back to the configured config.toml. On a fresh machine it
// also offers to create the config file (and parent directory).
func runSetup(argv []string, configPath *string, stdout, stderr *os.File) int {
	flagset := flag.NewFlagSet("setup", flag.ContinueOnError)
	flagset.SetOutput(stderr)
	flagset.StringVar(configPath, "config", *configPath, "path to config.toml")
	if err := flagset.Parse(argv); err != nil {
		return exitUsageErr
	}

	// Try to load the existing config. A missing file is fine — we'll
	// prompt the user to create one. Any other error (bad TOML, denied
	// permissions, etc.) is fatal: the user should fix the file by
	// hand before re-running setup.
	//
	// config.Load wraps the underlying os error in fmt.Errorf("open
	// config: %w", ...), so the unwrap-aware errors.Is check is
	// required — os.IsNotExist would miss it.
	cfg, err := config.Load(*configPath)
	switch {
	case err == nil:
		// loaded
	case errors.Is(err, fs.ErrNotExist):
		cfg = &config.Config{}
	default:
		fmt.Fprintf(stderr, "activesync-mcp setup: %v\n", err)
		return exitConfig
	}

	// In a non-TTY environment (CI, piped stdin) bubbletea would
	// happily run but the user can't interact. Fail loudly with an
	// actionable message.
	if !hookIsInteractive() {
		fmt.Fprintln(stderr, "activesync-mcp setup: requires an interactive terminal")
		return exitUsageErr
	}

	model := newSetupModel(*configPath, cfg)
	prog := tea.NewProgram(model, tea.WithOutput(stdout), tea.WithInput(os.Stdin))
	final, err := prog.Run()
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp setup: %v\n", err)
		return exitRuntime
	}
	m, ok := final.(setupModel)
	if !ok || m.lastErr != nil {
		if ok && m.lastErr != nil {
			fmt.Fprintf(stderr, "activesync-mcp setup: %v\n", m.lastErr)
		}
		return exitRuntime
	}
	return exitOK
}

// hookIsInteractive lets tests override the TTY probe. Production
// inspects os.Stdin; tests substitute a stub so they can exercise the
// non-interactive bail without redirecting the test process's stdin.
var hookIsInteractive = isInteractive

// isInteractive reports whether stdin is a terminal. In CI / when
// stdin is piped we refuse to run rather than spinning bubbletea on a
// stream that can't drive it.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ensureConfigDir creates the parent directory of the config path with
// 0700 perms; harmless if it already exists. Used right before saving.
func ensureConfigDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o700)
}
