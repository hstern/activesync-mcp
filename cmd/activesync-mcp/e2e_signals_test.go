// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

// Signal handling under in-flight MCP tool calls. The Tier 2
// serve_signals_integration_test.go covers the trivial case (server
// idle, signal, exit). The case that bites real users is "signal
// arrives while a tool handler is mid-EAS-roundtrip" — the shutdown
// path must unwind both the goroutine running the handler and the
// MCP loop without deadlocking. This test reproduces that scenario
// against the live testenv.

package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"activesync-mcp/lib/server"
)

func TestE2E_SignalDuringInFlightToolCall(t *testing.T) {
	bin := e2eBinary(t)
	cfg := e2eConfigPath(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.Command(bin, "serve", "--config", cfg)
	cmd.Stderr = testWriter{t}
	transport := &mcp.CommandTransport{Command: cmd}
	c := mcp.NewClient(&mcp.Implementation{Name: "signal-e2e", Version: "0"}, nil)
	cs, err := c.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// No t.Cleanup(cs.Close) — we drive shutdown explicitly below to
	// assert its timing.

	// Warm-up: prove the server is past startup before the hot path.
	// Without this the signal could race with init and look like a
	// shutdown bug when really the handler hadn't been wired yet.
	var folders server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "test"}, &folders)

	// Issue a tool call in a goroutine. email_send goes through an EAS
	// SendMail POST plus postfix delivery — even on the testenv it
	// gives the signal a wide window to land mid-flight. The point is
	// not to win the race but to prove both outcomes are safe:
	// either the response makes it back before the signal lands, or
	// the transport breaks under the handler and CallTool errors.
	callDone := make(chan error, 1)
	go func() {
		callCtx, callCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer callCancel()
		_, err := cs.CallTool(callCtx, &mcp.CallToolParams{
			Name: "email_send",
			Arguments: server.EmailSendInput{
				Account:  "test",
				To:       []server.EmailAddress{{Address: "integration@asmcp.test"}},
				Subject:  "signal-during-call",
				BodyText: "x",
			},
		})
		callDone <- err
	}()

	// Small delay so the request frame is on the wire by the time the
	// signal lands. 100ms is enough on the testenv; tightening it
	// further just narrows the in-flight window without changing what
	// the test asserts.
	time.Sleep(100 * time.Millisecond)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	// Goroutine must return promptly. A hang here means the shutdown
	// path is blocked behind an in-flight handler that never gets
	// cancelled — exactly the regression this test guards against.
	select {
	case err := <-callDone:
		t.Logf("CallTool returned: err=%v", err)
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("CallTool did not return within 10s of SIGINT")
	}

	// cs.Close closes stdin and waits for the process. We bound the
	// total close time at 7s — the SDK gives the subprocess 5s to
	// exit naturally before SIGTERM-ing it, and then a brief tail to
	// reap. If close returns within that envelope, the server shut
	// down on its own SIGINT handler (the natural-exit path), which
	// is the assertion. Anything longer indicates the SDK had to fall
	// back to SIGTERM/SIGKILL — a regression in our handler.
	closeDone := make(chan error, 1)
	go func() { closeDone <- cs.Close() }()
	select {
	case <-closeDone:
		// Close returned; cmd was reaped. Close's error is ignored
		// because exec.Wait surfaces ExitError for any non-zero exit
		// (including signal-handled clean exits where ExitCode is 0
		// but the signal field is set on some platforms).
	case <-time.After(7 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("cs.Close did not return within 7s of SIGINT — server shutdown hung")
	}
}
