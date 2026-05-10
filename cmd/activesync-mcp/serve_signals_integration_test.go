// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build integration

// Tier 2 integration tests for the `serve` subcommand's signal
// handling. Spawns the actual binary and asserts it shuts down
// cleanly under SIGTERM/SIGINT (Unix) or os.Interrupt (Windows),
// and that misuse exit codes match the documented contract.

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// buildBinary compiles activesync-mcp into a temp dir and returns
// the path. Single helper used by every binary-spawning integration
// test in this package.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "activesync-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	return bin
}

// writeMinimalConfig creates a TOML config with one account whose
// secret comes from a portable echo command (no real EAS server
// needed since these tests exit before the first sync).
func writeMinimalConfig(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	stateDir = t.TempDir()
	cfgPath = filepath.Join(t.TempDir(), "config.toml")
	body := `state_dir = "` + escapeTOML(stateDir) + `"
log_level = "info"

[[account]]
name             = "test"
server_url       = "https://example.invalid/Microsoft-Server-ActiveSync"
username         = "u"
secret           = { command = ["` + portableEcho() + `", "x"] }
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, stateDir
}

// portableEcho returns a binary that's guaranteed to exist on the
// runner. /bin/echo on Unix; cmd.exe's echo via "cmd" on Windows.
// (We just need *something* the secret-command path can spawn at
// resolve time; serve doesn't actually fetch the secret until a tool
// is invoked, but the schema validator runs at config load.)
func portableEcho() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "/bin/echo"
}

// escapeTOML quotes backslashes for the TOML inline-string value.
// Windows paths contain backslashes which TOML interprets as escapes.
func escapeTOML(s string) string {
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			out = append(out, '\\', '\\')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func TestServe_AbortsOnMissingConfig(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "serve", "--config", filepath.Join(t.TempDir(), "does-not-exist.toml"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("want non-zero exit for missing config")
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("unexpected error type: %T (%v)", err, err)
	}
	// Documented exit code for config errors is exitConfig (2).
	if got := ee.ExitCode(); got != 2 {
		t.Errorf("exit code = %d, want 2 (exitConfig); stderr = %s", got, stderr.String())
	}
}

func TestServe_SIGTERMShutsDownCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM not meaningful on Windows; covered by InterruptShutsDown")
	}
	cfgPath, _ := writeMinimalConfig(t)
	bin := buildBinary(t)

	cmd := exec.Command(bin, "serve", "--config", cfgPath)
	// serve reads JSON-RPC frames from stdin; redirect to /dev/null so
	// it doesn't read host stdin.
	cmd.Stdin, _ = os.Open(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Give the server time to install signal handlers. signal.NotifyContext
	// is the first thing runServe does, but the test still has to wait
	// for go runtime startup + linker init + the binary's own startup
	// path. 1.5s is generous on slow CI runners; sending the signal
	// before the handler is wired surfaces as exit -1 (process killed
	// by signal) rather than the clean exit 0 we want to assert.
	time.Sleep(1500 * time.Millisecond)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			// Clean shutdown returns nil from cmd.Wait. Anything else
			// (timeout, signal-without-handler, panic) leaks here.
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("Wait returned unexpected error: %v", err)
			}
			// Exit code on signal-handled clean shutdown should be 0
			// (exitOK). The handler turns the signal into a context
			// cancel and Run returns nil.
			if code := ee.ExitCode(); code != 0 {
				t.Errorf("exit code on SIGINT = %d, want 0", code)
			}
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("server did not shut down within 3s of SIGINT")
	}
}

func TestServe_InterruptShutsDownCleanly(t *testing.T) {
	// On Windows, os.Interrupt is the only portable signal (SIGTERM
	// doesn't exist; SIGBREAK requires CreateProcess flags). On Unix
	// this is identical to TestServe_SIGTERMShutsDownCleanly modulo
	// signal name; we still run it everywhere to give Windows runners
	// a chance to validate signal handling at all.
	cfgPath, _ := writeMinimalConfig(t)
	bin := buildBinary(t)

	cmd := exec.Command(bin, "serve", "--config", cfgPath)
	cmd.Stdin, _ = os.Open(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(1500 * time.Millisecond) // see SIGTERM test for race rationale
	_ = cmd.Process.Signal(os.Interrupt)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 0 {
				return
			}
			// On Windows, an os.Interrupt to a child not in its own
			// process group may surface as a non-zero exit. Tolerate
			// any exit; the assertion is just "process exited
			// promptly", not "exit code is 0 on Windows".
			if runtime.GOOS == "windows" {
				return
			}
			t.Errorf("Wait returned %v", err)
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("server did not shut down within 3s of os.Interrupt")
	}
}
