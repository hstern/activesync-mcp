// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e || scenario

// End-to-end test helpers shared by Tier 3 (single-tool roundtrips,
// `e2e` tag) and Tier 4 (multi-call agent flows, `scenario` tag).
// Spawns the activesync-mcp binary as a subprocess and connects to
// it as an MCP client over stdio. Each test gets a fresh bbolt state
// dir so reruns don't accumulate sync keys.
//
// The shared binary is built once per test process (sync.Once) — the
// per-test cost is just `cmd.Start()` + the MCP handshake.
//
// All tests in this file expect a testenv stack to be up. The
// canonical default is Z-Push 2.7.6 on localhost:8580; the CI matrix
// rotates the stack via EAS_INTEGRATION_URL (and pins the active
// stack name in EAS_INTEGRATION_STACK so skipOnStack can opt tests
// out per-stack). `make e2e` brings up the default; CI's e2e job
// brings up the matrix-selected stack.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"activesync-mcp/lib/server"
)

var (
	binBuildOnce sync.Once
	binPath      string
	binBuildErr  error
)

// e2eBinary returns the path to the activesync-mcp binary, building
// it the first time this function is called per test process.
func e2eBinary(t *testing.T) string {
	t.Helper()
	binBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "activesync-mcp-e2e-*")
		if err != nil {
			binBuildErr = err
			return
		}
		out := filepath.Join(dir, "activesync-mcp")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			binBuildErr = fmt.Errorf("go build: %w", err)
			return
		}
		binPath = out
	})
	if binBuildErr != nil {
		t.Fatalf("build binary: %v", binBuildErr)
	}
	return binPath
}

// e2eConfigPath copies the canonical testdata/e2e_config.toml into a
// temp location whose state_dir points at a per-test bbolt directory
// and whose server_url is overridden when EAS_INTEGRATION_URL is set
// (so the CI matrix can point the same suite at different stacks).
// Returns the new config path.
func e2eConfigPath(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	src, err := os.ReadFile("testdata/e2e_config.toml")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	// state_dir is top-level, so strip-and-prepend works. server_url
	// lives inside [[account]], so it has to be replaced in place to
	// stay in its block.
	body := stripStateDir(string(src))
	body = replaceLineByKey(body, "server_url",
		"server_url       = \""+e2eServerURL()+"\"")
	body = "state_dir = \"" + escapeTOMLE2E(stateDir) + "\"\n" + body
	cfgPath := filepath.Join(t.TempDir(), "e2e.toml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// stripStateDir removes the original state_dir line so our prepended
// override is the only one parsed.
func stripStateDir(s string) string {
	out := make([]byte, 0, len(s))
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		line := s[start : i+1]
		if !startsWith(line, "state_dir") {
			out = append(out, line...)
		}
		start = i + 1
	}
	if start < len(s) {
		out = append(out, s[start:]...)
	}
	return string(out)
}

// replaceLineByKey rewrites the first line whose leading
// non-whitespace token matches key, replacing the whole line (minus
// trailing newline) with replacement. If no match is found, returns
// the input unchanged.
func replaceLineByKey(s, key, replacement string) string {
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			j := 0
			for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
			if startsWith(line[j:], key) {
				return s[:start] + replacement + s[i:]
			}
			start = i + 1
		}
	}
	return s
}

// e2eServerURL returns the EAS server URL for e2e tests. The
// canonical default points at the Z-Push 2.7.6 testenv on port 8580;
// CI overrides via EAS_INTEGRATION_URL so the matrix can rotate
// across testenv stacks (zpush-2.6 lives on 8583, etc.).
func e2eServerURL() string {
	if u := os.Getenv("EAS_INTEGRATION_URL"); u != "" {
		return u
	}
	return "http://localhost:8580/Microsoft-Server-ActiveSync"
}

// skipOnStack skips the calling test if EAS_INTEGRATION_STACK matches
// any of the listed stacks. Mirrors the library-side helper in
// go-activesync/eas/integration_test.go. Used to opt tests out of
// stacks with documented protocol-surface gaps — e.g. Z-Push 2.6
// Provision returns HTTP 500 on PHP 8, so any test that triggers a
// Provision-dependent tool call has to skip on zpush-2.6.
func skipOnStack(t *testing.T, reason string, stacks ...string) {
	t.Helper()
	cur := os.Getenv("EAS_INTEGRATION_STACK")
	for _, s := range stacks {
		if cur == s {
			t.Skipf("skipped on stack %q: %s", cur, reason)
		}
	}
}

func startsWith(s, pre string) bool {
	if len(s) < len(pre) {
		return false
	}
	return s[:len(pre)] == pre
}

// e2eClient spawns the activesync-mcp binary in serve mode and
// connects to it as an MCP client over stdio. Returns the connected
// session ready for CallTool. Cleans up the subprocess and bbolt
// state dir at test end.
func e2eClient(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return e2eClientWithConfig(t, e2eConfigPath(t))
}

// e2eClientWithConfig is e2eClient against an arbitrary config path.
// Tests that need a non-default config (e.g. multi-account) build the
// TOML themselves and hand it in here.
//
// Skips on zpush-2.6 because the binary's lazy-Provision path is hit
// on the first tool call (or by push subscriptions during startup),
// and Z-Push 2.6's Provision handler returns HTTP 500 on PHP 8. Any
// test that needs an MCP session against the testenv can't run on
// 2.6; tests that only need the binary (e.g. doctor, accounts_list
// via SIGTERM) spawn it directly without going through e2eClient.
func e2eClientWithConfig(t *testing.T, cfg string) *mcp.ClientSession {
	t.Helper()
	skipOnStack(t, "Z-Push 2.6 Provision returns HTTP 500 on PHP 8 (go-activesync#7)", "zpush-2.6")
	bin := e2eBinary(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	cmd := exec.Command(bin, "serve", "--config", cfg)
	cmd.Stderr = testWriter{t}
	transport := &mcp.CommandTransport{Command: cmd}

	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// testWriter forwards subprocess stderr into the test log so a failure
// surfaces server-side errors without interleaving with go test stdout.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("server stderr: %s", p)
	return len(p), nil
}

// callTool is a thin wrapper that issues an MCP CallTool and decodes
// the structured output JSON into `out`. Returns the result so callers
// can also inspect IsError + raw content.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) returned IsError; content: %v", name, formatContent(res.Content))
	}
	if out != nil && res.StructuredContent != nil {
		// SDK delivers structured output as JSON-marshalable bytes.
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured content: %v", err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("unmarshal %s output: %v\nraw=%s", name, err, raw)
		}
	}
	return res
}

// callToolExpectError is the inverse: assert IsError=true and return
// the result for further inspection.
func callToolExpectError(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s) transport error: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(%s) succeeded but expected IsError=true", name)
	}
	return res
}

func formatContent(content []mcp.Content) string {
	if len(content) == 0 {
		return "<empty>"
	}
	if t, ok := content[0].(*mcp.TextContent); ok {
		return t.Text
	}
	return fmt.Sprintf("%+v", content[0])
}

// escapeTOML is duplicated from setup_integration_test.go because Go
// doesn't share unexported helpers across build tags within a package
// without lots of file-level juggling. Trivial enough.
func escapeTOMLE2E(s string) string {
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

// contextWithTimeout returns a fresh background context with a
// timeout. Used by cleanup paths after t.Context() is already done.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// findInboxID lists email folders and returns the Inbox's server ID.
func findInboxID(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	var out server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "test"}, &out)
	for _, f := range out.Folders {
		if f.Type == "Inbox" {
			return f.ID
		}
	}
	t.Fatalf("no Inbox folder in %d-folder list", len(out.Folders))
	return ""
}

// findFolderByType returns the first folder of the given type
// (SentItems, Drafts, DeletedItems, etc.). Returns "" if none.
func findFolderByType(t *testing.T, cs *mcp.ClientSession, want string) string {
	t.Helper()
	var out server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "test"}, &out)
	for _, f := range out.Folders {
		if f.Type == want {
			return f.ID
		}
	}
	return ""
}

// findCalendarFolder returns the default calendar folder's id (the
// first non-tasks calendar surfaced by Z-Push's BackendCalDAV).
func findCalendarFolder(t *testing.T, cs *mcp.ClientSession) string {
	t.Helper()
	var out server.CalendarListFoldersOutput
	callTool(t, cs, "calendar_list_folders",
		server.CalendarListFoldersInput{Account: "test"}, &out)
	for _, f := range out.Folders {
		if f.Type == "Calendar" {
			return f.ID
		}
	}
	t.Fatalf("no Calendar folder in: %+v", out.Folders)
	return ""
}

// sendLoopbackEmail sends a message to the testenv user and returns
// the unique subject that identifies it. Caller polls the inbox to
// find the resulting message via waitForMessageInInbox.
func sendLoopbackEmail(t *testing.T, cs *mcp.ClientSession, body string) string {
	t.Helper()
	subject := fmt.Sprintf("e2e-%s-%d", t.Name(), time.Now().UnixNano())
	callTool(t, cs, "email_send", server.EmailSendInput{
		Account:  "test",
		To:       []server.EmailAddress{{Address: "integration@asmcp.test"}},
		Subject:  subject,
		BodyText: body,
	}, nil)
	return subject
}

// waitForMessageInInbox polls email_list until a message with the
// given subject appears, or fails the test.
func waitForMessageInInbox(t *testing.T, cs *mcp.ClientSession, inboxID, subject string) server.EmailRow {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var out server.EmailListOutput
		callTool(t, cs, "email_list", server.EmailListInput{
			Account: "test", FolderID: inboxID, WindowSize: 50,
		}, &out)
		for _, e := range out.Items {
			if e.Subject == subject {
				return e
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("message %q never appeared in Inbox after 20s", subject)
	return server.EmailRow{}
}

// mustCallToolBool is a sloppy variant for cleanup paths that
// fire-and-forget — never fails the test. Uses its own background
// context because t.Context() is already cancelled by the time
// t.Cleanup runs.
func mustCallToolBool(t *testing.T, cs *mcp.ClientSession, name string, args any) bool {
	t.Helper()
	ctx, cancel := contextWithTimeout(30 * time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Logf("cleanup CallTool(%s): %v", name, err)
		return false
	}
	if res.IsError {
		t.Logf("cleanup CallTool(%s) IsError: %s", name, formatContent(res.Content))
		return false
	}
	return true
}

// decodeStructured marshal-then-unmarshal-decodes the structured
// content of a CallToolResult into out. Used by tests that handle
// both success and IsError outcomes inline rather than going through
// callTool's hard-fail.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.StructuredContent == nil {
		return
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// Static check so this file doesn't fail unused-import on platforms
// where errors isn't transitively pulled.
var _ = errors.New
