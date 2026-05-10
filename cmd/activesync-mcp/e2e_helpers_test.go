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
// All tests in this file expect the testenv stack to be up at
// localhost:8580. `make e2e` brings it up; CI's e2e job does the same.

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
// temp location whose state_dir points at a per-test bbolt directory.
// Returns the new config path.
func e2eConfigPath(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	src, err := os.ReadFile("testdata/e2e_config.toml")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "e2e.toml")
	// state_dir override: replace the placeholder with the per-test dir.
	body := []byte("state_dir = \"" + escapeTOMLE2E(stateDir) + "\"\n" + stripStateDir(string(src)))
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
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
	bin := e2eBinary(t)
	cfg := e2eConfigPath(t)

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
