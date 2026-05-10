// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build integration

// Tier 2 integration tests for the user-facing CLI subcommands run
// during initial setup: `keyring set/get/delete` and `autodiscover`.
// Spawns the real binary and exercises the subcommands against the
// real OS keyring (skipped if unreachable). Catches argv quoting,
// stdin handling, exit codes, and config path resolution — all the
// things that look fine in unit tests but break the moment the binary
// is running for real on a fresh box.

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// uniqueAccount returns a per-test account label that won't collide
// with parallel runs or stale entries from a crashed earlier run.
func uniqueAccount(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("setup-int-%s-%d", t.Name(), time.Now().UnixNano())
}

// keyringReachable is the binary-spawn variant of the helper in
// lib/config/keyring_integration_test.go. We re-derive it here rather
// than exporting from another package because tests in different
// packages can't share helpers without surfacing them in the
// non-test API.
func keyringReachable(t *testing.T) bool {
	t.Helper()
	const probeSvc, probeAcct = "activesync-mcp-test", "setup-probe"
	defer func() { _ = keyring.Delete(probeSvc, probeAcct) }()
	if err := keyring.Set(probeSvc, probeAcct, "ok"); err != nil {
		t.Skipf("OS keyring unreachable: %v", err)
		return false
	}
	return true
}

// writeKeyringConfig writes a TOML config that points one account at
// the keyring entry we want to manipulate.
func writeKeyringConfig(t *testing.T, svc, acct string) string {
	t.Helper()
	body := fmt.Sprintf(`state_dir = %q
[[account]]
name             = "test"
server_url       = "https://example.invalid/Microsoft-Server-ActiveSync"
username         = "u"
secret           = { keyring_service = %q, keyring_account = %q }
`, escapeTOML(t.TempDir()), svc, acct)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfg
}

// runBinary spawns the activesync-mcp binary with the given args, pipes
// `stdin` to its stdin, and returns (stdout, stderr, exit code).
func runBinary(t *testing.T, bin string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("unexpected error type: %T (%v)", err, err)
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// ---- keyring CLI subcommand round-trip ---------------------------------

func TestSetup_KeyringSetGetDeleteRoundTrip(t *testing.T) {
	if !keyringReachable(t) {
		return
	}
	bin := buildBinary(t)
	const svc = "activesync-mcp-test"
	acct := uniqueAccount(t)
	cfgPath := writeKeyringConfig(t, svc, acct)
	t.Cleanup(func() { _ = keyring.Delete(svc, acct) })

	const password = "round-trip-via-binary-!@#$%"

	// 1. set: pipe the password on stdin (the no-echo prompt path
	//    falls back to plain line-read on non-TTY).
	stdout, stderr, code := runBinary(t, bin, password+"\n",
		"keyring", "set", "--account", "test", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("keyring set exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "stored:") {
		t.Errorf("set stdout missing 'stored:' confirmation: %q", stdout)
	}

	// 2. get: should report "set:" (never the value).
	stdout, stderr, code = runBinary(t, bin, "",
		"keyring", "get", "--account", "test", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("keyring get exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "set:") || strings.Contains(stdout, password) {
		t.Errorf("get leaked or omitted: %q", stdout)
	}

	// 3. delete: should succeed.
	stdout, stderr, code = runBinary(t, bin, "",
		"keyring", "delete", "--account", "test", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("keyring delete exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "deleted:") {
		t.Errorf("delete stdout: %q", stdout)
	}

	// 4. get after delete: should report "not set:".
	stdout, _, code = runBinary(t, bin, "",
		"keyring", "get", "--account", "test", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("keyring get post-delete exit %d", code)
	}
	if !strings.Contains(stdout, "not set:") {
		t.Errorf("get after delete should say 'not set:': %q", stdout)
	}
}

func TestSetup_KeyringRejectsEmptyPassword(t *testing.T) {
	if !keyringReachable(t) {
		return
	}
	bin := buildBinary(t)
	cfgPath := writeKeyringConfig(t, "activesync-mcp-test", uniqueAccount(t))

	// Empty stdin → empty password → exit non-zero with a clear
	// rejection (catches the "user hits enter and we'd otherwise
	// silently store an empty string" footgun).
	_, stderr, code := runBinary(t, bin, "\n",
		"keyring", "set", "--account", "test", "--config", cfgPath)
	if code == 0 {
		t.Fatal("want non-zero exit for empty password")
	}
	if !strings.Contains(stderr, "empty password rejected") {
		t.Errorf("stderr missing rejection message: %q", stderr)
	}
}

func TestSetup_KeyringRejectsUnknownAccount(t *testing.T) {
	if !keyringReachable(t) {
		return
	}
	bin := buildBinary(t)
	cfgPath := writeKeyringConfig(t, "activesync-mcp-test", uniqueAccount(t))

	_, stderr, code := runBinary(t, bin, "x\n",
		"keyring", "set", "--account", "no-such-account", "--config", cfgPath)
	if code == 0 {
		t.Fatal("want non-zero exit for unknown account")
	}
	if !strings.Contains(stderr, "no account named") {
		t.Errorf("stderr should mention the missing account: %q", stderr)
	}
}

// ---- autodiscover CLI subcommand error paths --------------------------
//
// The happy path needs DNS + a real autodiscover endpoint, which we
// can't stand up at the binary level (runAutodiscover doesn't expose
// an endpoint override). Cover the validation paths that DON'T need
// the network instead — those are also the ones a confused user is
// most likely to trigger.

func TestSetup_AutodiscoverRejectsMissingEmail(t *testing.T) {
	bin := buildBinary(t)
	_, stderr, code := runBinary(t, bin, "", "autodiscover")
	if code == 0 {
		t.Fatal("want non-zero exit for missing --email")
	}
	if !strings.Contains(stderr, "--email is required") {
		t.Errorf("stderr: %q", stderr)
	}
}

func TestSetup_AutodiscoverRejectsEmptyPassword(t *testing.T) {
	bin := buildBinary(t)
	_, stderr, code := runBinary(t, bin, "\n",
		"autodiscover", "--email", "user@example.invalid")
	if code == 0 {
		t.Fatal("want non-zero exit for empty password")
	}
	if !strings.Contains(stderr, "empty password rejected") {
		t.Errorf("stderr: %q", stderr)
	}
}

// ---- top-level CLI surface --------------------------------------------

func TestSetup_UnknownSubcommandPrintsHelp(t *testing.T) {
	bin := buildBinary(t)
	// An unknown first-word arg is the closest thing to "show me help"
	// the CLI offers. It must print the subcommand list (so a confused
	// user can recover) and exit promptly.
	cmd := exec.Command(bin, "this-is-not-a-subcommand")
	cmd.Stdin, _ = os.Open(os.DevNull)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("unknown subcommand hung for 3s")
	}
	combined := stdout.String() + stderr.String()
	for _, want := range []string{"serve", "keyring", "autodiscover", "doctor"} {
		if !strings.Contains(combined, want) {
			t.Errorf("help missing %q subcommand:\n%s", want, combined)
		}
	}
}
