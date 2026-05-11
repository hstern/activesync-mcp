// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"activesync-mcp/lib/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/zalando/go-keyring"
)

// withTUIHooks installs in-memory keyring + a no-op connection-test
// stub for the duration of a test, restoring originals on cleanup.
func withTUIHooks(t *testing.T) (kr map[string]string, testErr *error) {
	t.Helper()
	kr = map[string]string{}
	var mu sync.Mutex
	var stubErr error
	testErr = &stubErr

	prevSet, prevGet, prevDel := hookKeyringSet, hookKeyringGet, hookKeyringDelete
	prevTest := hookRunConnectionTest

	hookKeyringSet = func(svc, user, pw string) error {
		mu.Lock()
		defer mu.Unlock()
		kr[svc+"/"+user] = pw
		return nil
	}
	hookKeyringGet = func(svc, user string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		v, ok := kr[svc+"/"+user]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return v, nil
	}
	hookKeyringDelete = func(svc, user string) error {
		mu.Lock()
		defer mu.Unlock()
		delete(kr, svc+"/"+user)
		return nil
	}
	hookRunConnectionTest = func(*editState) error { return stubErr }

	t.Cleanup(func() {
		hookKeyringSet, hookKeyringGet, hookKeyringDelete = prevSet, prevGet, prevDel
		hookRunConnectionTest = prevTest
	})
	return kr, testErr
}

// readUntil polls tm.Output() until predicate matches or 2s elapses.
func readUntil(t *testing.T, tm *teatest.TestModel, want string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(),
		func(b []byte) bool { return strings.Contains(string(b), want) },
		teatest.WithDuration(2*time.Second),
		teatest.WithCheckInterval(20*time.Millisecond),
	)
}

// TestRunSetup_missingConfigGetsPastLoad is a regression test for the
// bug where `setup` against a non-existent config printed
// "open config: no such file or directory" and exited 2 instead of
// proceeding to the empty-menu TUI.
//
// Root cause: config.Load wraps the underlying os error
// (`fmt.Errorf("open config: %w", err)`); the original guard used
// os.IsNotExist which doesn't unwrap. The fix is errors.Is.
//
// We can't run the TUI in `go test` (no TTY), so the test pins the
// post-fix control flow: with a missing config, runSetup must reach
// the isInteractive() guard, not bail at the config load.
//
//	BUG:    exitConfig + "open config: no such file or directory"
//	FIXED:  exitUsageErr + "requires an interactive terminal"
//
// stubNonInteractive forces hookIsInteractive to report "not a TTY"
// for the duration of the test so runSetup hits its non-interactive
// bail rather than launching bubbletea (which would block on stdin).
func stubNonInteractive(t *testing.T) {
	t.Helper()
	prev := hookIsInteractive
	hookIsInteractive = func() bool { return false }
	t.Cleanup(func() { hookIsInteractive = prev })
}

func TestRunSetup_missingConfigGetsPastLoad(t *testing.T) {
	stubNonInteractive(t)
	out, errF, read := tempStdio(t)
	cfg := "/no/such/path/config.toml"
	code := runSetup(nil, &cfg, out, errF)

	if code != exitUsageErr {
		t.Errorf("exit %d (want %d) — load step should have succeeded with empty cfg", code, exitUsageErr)
	}
	_, e := read()
	if strings.Contains(e, "open config") {
		t.Errorf("regression: bailed at config load instead of reaching the TUI guard:\n%s", e)
	}
	if !strings.Contains(e, "requires an interactive terminal") {
		t.Errorf("expected to reach the TTY guard, got:\n%s", e)
	}
}

// TestRunSetup_corruptConfigBailsAtLoad asserts the OTHER side of the
// fix: a non-not-exist error (here: invalid TOML) still aborts at
// load. The bug-fix only relaxes the missing-file case.
func TestRunSetup_corruptConfigBailsAtLoad(t *testing.T) {
	stubNonInteractive(t)
	cfg := withConfigFile(t, "this is not [valid toml")
	out, errF, read := tempStdio(t)
	code := runSetup(nil, &cfg, out, errF)
	if code != exitConfig {
		t.Errorf("exit %d (want %d) — corrupt config should still bail at load", code, exitConfig)
	}
	_, e := read()
	if !strings.Contains(e, "decode config") {
		t.Errorf("expected the underlying decode error, got:\n%s", e)
	}
}

func TestSetupTUI_emptyMenu_quit(t *testing.T) {
	withTUIHooks(t)
	cfg := &config.Config{}
	tm := teatest.NewTestModel(t, newSetupModel("/tmp/test.toml", cfg),
		teatest.WithInitialTermSize(120, 40))

	readUntil(t, tm, "No accounts configured yet.")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

func TestSetupTUI_addAccount_writesConfigAndKeyring(t *testing.T) {
	kr, _ := withTUIHooks(t) // testErr stays nil → connection test passes
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := &config.Config{}

	tm := teatest.NewTestModel(t, newSetupModel(path, cfg),
		teatest.WithInitialTermSize(120, 40))

	// Open add form.
	readUntil(t, tm, "No accounts configured yet.")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	readUntil(t, tm, "Add account")

	// Type name.
	tm.Type("work")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	// Type email.
	tm.Type("henry@example.com")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	// Type password.
	tm.Type("hunter2")
	// Submit; connection test stub returns nil → save fires.
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// Connection test + save fire asynchronously via tea.Cmds. Give
	// them time to land before we quit and inspect side effects.
	time.Sleep(150 * time.Millisecond)

	// Quit cleanly.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	// Side effects: config file exists, keyring set.
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Name != "work" {
		t.Errorf("accounts = %+v", got.Accounts)
	}
	if got.Accounts[0].Username != "henry@example.com" {
		t.Errorf("username = %q", got.Accounts[0].Username)
	}
	if kr["activesync-mcp/work"] != "hunter2" {
		t.Errorf("keyring entry = %q (full kr=%v)", kr["activesync-mcp/work"], kr)
	}
}

func TestSetupTUI_addAccount_failedConnectionTestBlocksSave(t *testing.T) {
	kr, testErr := withTUIHooks(t)
	*testErr = errors.New("simulated network failure")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	tm := teatest.NewTestModel(t, newSetupModel(path, &config.Config{}),
		teatest.WithInitialTermSize(120, 40))

	readUntil(t, tm, "No accounts configured yet.")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	readUntil(t, tm, "Add account")
	tm.Type("work")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	tm.Type("henry@example.com")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	tm.Type("hunter2")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// Give the connection-test cmd a moment to fire and produce its
	// testResultMsg. Then quit and assert on the FinalModel state.
	time.Sleep(100 * time.Millisecond)
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(setupModel)

	// No save happened: keyring stays empty, no config file.
	if len(kr) != 0 {
		t.Errorf("keyring should be empty after failed test, got %v", kr)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("config file should not have been written")
	}
	// FinalModel preserves the cfg snapshot — no accounts added.
	if len(final.cfg.Accounts) != 0 {
		t.Errorf("accounts added despite test failure: %+v", final.cfg.Accounts)
	}
}

func TestSetupTUI_passwordMaskedByDefault_revealOnToggle(t *testing.T) {
	withTUIHooks(t)
	tm := teatest.NewTestModel(t, newSetupModel("/tmp/p.toml", &config.Config{}),
		teatest.WithInitialTermSize(120, 40))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	readUntil(t, tm, "Add account")
	// Move to password field (Tab, Tab) and type a recognisable value.
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	tm.Type("ZZsecretZZ")

	// Confirm the value is rendered as bullets, not plaintext.
	readUntil(t, tm, "•••••")
	out := tm.Output()
	buf := make([]byte, 64*1024)
	n, _ := out.Read(buf)
	if strings.Contains(string(buf[:n]), "ZZsecretZZ") {
		t.Errorf("password leaked while masked:\n%s", buf[:n])
	}

	// Toggle reveal; the actual characters should now appear.
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlG})
	readUntil(t, tm, "ZZsecretZZ")

	// Cancel + quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

func TestSetupTUI_deleteAccount_dropsKeyringEntry(t *testing.T) {
	kr, _ := withTUIHooks(t)
	kr["activesync-mcp/work"] = "hunter2" // pre-existing entry
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name: "work", Username: "u", ServerURL: "https://x",
			Secret: config.SecretRef{KeyringService: "activesync-mcp", KeyringAccount: "work"},
		}},
	}

	tm := teatest.NewTestModel(t, newSetupModel(path, cfg),
		teatest.WithInitialTermSize(120, 40))

	readUntil(t, tm, "work")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	readUntil(t, tm, "Delete account")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	// The save fires asynchronously via runSave; give it a moment
	// to land before we quit and inspect side effects.
	time.Sleep(100 * time.Millisecond)

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	// Keyring entry should be gone, config should have no accounts.
	if _, ok := kr["activesync-mcp/work"]; ok {
		t.Errorf("keyring entry not deleted: %v", kr)
	}
	got, _ := config.Load(path)
	if got != nil && len(got.Accounts) != 0 {
		t.Errorf("accounts after delete: %+v", got.Accounts)
	}
}
