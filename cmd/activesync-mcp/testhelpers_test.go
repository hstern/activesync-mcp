// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// withConfigFile writes a TOML config to a temp dir and returns its path.
func withConfigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// tempStdio gives subcommand tests a writable stdout + stderr backed
// by tempfiles, plus a `read()` callback that returns their contents.
// LIFO close ordering: the file Close cleanups are registered AFTER
// t.TempDir's, so they fire FIRST — Windows refuses to remove the
// temp dir while a child file is still open.
func tempStdio(t *testing.T) (*os.File, *os.File, func() (string, string)) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	errF, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errF.Close() })
	read := func() (string, string) {
		if _, err := out.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := errF.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		o, _ := io.ReadAll(out)
		e, _ := io.ReadAll(errF)
		return string(o), string(e)
	}
	return out, errF, read
}
