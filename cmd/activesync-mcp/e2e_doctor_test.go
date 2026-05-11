// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestE2E_DoctorReportsHealthyAgainstTestenv(t *testing.T) {
	bin := e2eBinary(t)
	cfg := e2eConfigPath(t)
	cmd := exec.Command(bin, "doctor", "--config", cfg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("doctor exit %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "test") || !strings.Contains(out, "OK") {
		t.Errorf("doctor stdout missing account name + OK:\n%s", out)
	}
}

func TestE2E_DoctorReportsAuthFailure(t *testing.T) {
	bin := e2eBinary(t)
	// Build a config that points at the testenv but uses a bogus
	// password. The server will respond 401; doctor should surface
	// "FAIL OPTIONS" or similar.
	stateDir := t.TempDir()
	cfgPath := t.TempDir() + "/bad.toml"
	body := `state_dir = "` + escapeTOMLE2E(stateDir) + `"
[[account]]
name = "test"
server_url = "` + e2eServerURL() + `"
username = "integration"
secret = { command = ["printf", "%s", "wrong-password"] }
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "doctor", "--config", cfgPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("doctor with bad credentials exited 0\nstdout: %s", stdout.String())
	}
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "FAIL") {
		t.Errorf("doctor output missing FAIL marker:\n%s", out)
	}
}

func TestE2E_DoctorReportsUnreachableHost(t *testing.T) {
	bin := e2eBinary(t)
	stateDir := t.TempDir()
	cfgPath := t.TempDir() + "/unreachable.toml"
	body := `state_dir = "` + escapeTOMLE2E(stateDir) + `"
[[account]]
name = "test"
# port 1 is reserved + closed; connect refused immediately on most stacks.
server_url = "http://127.0.0.1:1/Microsoft-Server-ActiveSync"
username = "u"
secret = { command = ["printf", "%s", "x"] }
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "doctor", "--config", cfgPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("doctor against unreachable host exited 0")
	}
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "FAIL") {
		t.Errorf("doctor output missing FAIL marker:\n%s", out)
	}
}
