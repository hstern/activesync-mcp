// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build integration

// Tier 2 integration tests for the secret.command path through
// SecretResolver. Uses real os/exec subprocesses so quoting,
// stdout capture, exit-code surfacing, and context-deadline
// handling are exercised end-to-end on each OS.

package config

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// printArgv builds a cross-platform command that prints `payload`
// verbatim (no trailing newline) and exits 0.
//
//   - Unix: `printf` is in coreutils, doesn't add newlines, doesn't
//     quote.
//   - Windows: PowerShell's [Console]::Out.Write avoids cmd.exe's
//     redirect-quoting hell. exec.Command splits args literally so
//     `cmd /c set /p= <NUL` doesn't actually redirect — only a single
//     joined command string would. PowerShell sidesteps that entirely.
func printArgv(payload string) []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell", "-NoProfile", "-NonInteractive",
			"-Command", "[Console]::Out.Write('" + payload + "')"}
	}
	return []string{"printf", "%s", payload}
}

func TestSecretCommand_StdoutCaptured(t *testing.T) {
	const want = "hunter2-with-symbols-!@#$%"
	r := DefaultResolver()
	got, err := r.Resolve(context.Background(), &Account{
		Name:   "test-acct",
		Secret: SecretRef{Command: printArgv(want)},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
}

func TestSecretCommand_TrailingNewlineTrimmed(t *testing.T) {
	const want = "secret"
	// Use the platform's `echo` which always adds a newline; the
	// resolver must strip it.
	var argv []string
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "echo", want}
	} else {
		argv = []string{"sh", "-c", "echo " + want}
	}
	r := DefaultResolver()
	got, err := r.Resolve(context.Background(), &Account{
		Name:   "test-acct",
		Secret: SecretRef{Command: argv},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != want {
		t.Errorf("Resolve = %q, want %q (newline not stripped?)", got, want)
	}
}

func TestSecretCommand_NonZeroExitFails(t *testing.T) {
	var argv []string
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "exit", "1"}
	} else {
		argv = []string{"sh", "-c", "exit 1"}
	}
	r := DefaultResolver()
	_, err := r.Resolve(context.Background(), &Account{
		Name:   "test-acct",
		Secret: SecretRef{Command: argv},
	})
	if err == nil {
		t.Fatal("want error for non-zero exit")
	}
	// SecretResolver wraps with "secret command:" prefix.
	if !contains(err.Error(), "secret command") {
		t.Errorf("err = %q, missing 'secret command' prefix", err)
	}
}

func TestSecretCommand_EmptyOutputFails(t *testing.T) {
	r := DefaultResolver()
	_, err := r.Resolve(context.Background(), &Account{
		Name:   "test-acct",
		Secret: SecretRef{Command: printArgv("")},
	})
	if err == nil {
		t.Fatal("want error for empty stdout")
	}
}

func TestSecretCommand_StderrSurfacedOnFailure(t *testing.T) {
	const sentinel = "this-should-be-in-the-error"
	var argv []string
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", "echo", sentinel, "1>&2", "&&", "exit", "1"}
	} else {
		argv = []string{"sh", "-c", "echo " + sentinel + " >&2; exit 1"}
	}
	r := DefaultResolver()
	_, err := r.Resolve(context.Background(), &Account{
		Name:   "test-acct",
		Secret: SecretRef{Command: argv},
	})
	if err == nil {
		t.Fatal("want error")
	}
	if !contains(err.Error(), sentinel) {
		t.Errorf("err = %q, missing stderr sentinel %q", err, sentinel)
	}
}

func TestSecretCommand_ContextDeadlineHonoured(t *testing.T) {
	var argv []string
	if runtime.GOOS == "windows" {
		// `timeout` waits up to N seconds. Run it directly (not via
		// cmd /c) so context cancellation kills the actual blocking
		// process — going through cmd would orphan a grandchild.
		argv = []string{"timeout", "/t", "5", "/nobreak"}
	} else {
		// Run sleep directly. `sh -c "sleep 5"` would put sleep in a
		// sub-process; killing sh leaves sleep running and Go's exec
		// waits for inherited stdio pipes, so cmd.Wait blocks the
		// full 5s and the deadline assertion fires.
		argv = []string{"sleep", "5"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := DefaultResolver()
	_, err := r.Resolve(ctx, &Account{
		Name:   "test-acct",
		Secret: SecretRef{Command: argv},
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want error for context-deadline exceeded")
	}
	// Slack: 2s headroom on slow runners. The point is "didn't wait the full 5s".
	if elapsed > 2*time.Second {
		t.Errorf("waited %v, want <2s — deadline not honoured", elapsed)
	}
}

func TestSecretCommand_BinaryNotFound(t *testing.T) {
	r := DefaultResolver()
	_, err := r.Resolve(context.Background(), &Account{
		Name:   "test-acct",
		Secret: SecretRef{Command: []string{"/no/such/binary/exists/anywhere"}},
	})
	if err == nil {
		t.Fatal("want error for missing binary")
	}
}
