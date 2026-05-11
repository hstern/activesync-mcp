// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build integration

// Tier 2 integration test for the binary's top-level CLI surface.
// The keyring/autodiscover subcommands were replaced by the
// interactive `setup` TUI, so the per-subcommand round-trip tests
// that used to live here are gone; what remains is the smoke test
// that an unknown first-word arg still prints a recoverable help
// listing of the current subcommands.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSetup_UnknownSubcommandPrintsHelp(t *testing.T) {
	bin := buildBinary(t)
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
	for _, want := range []string{"serve", "setup", "doctor"} {
		if !strings.Contains(combined, want) {
			t.Errorf("help missing %q subcommand:\n%s", want, combined)
		}
	}
}
