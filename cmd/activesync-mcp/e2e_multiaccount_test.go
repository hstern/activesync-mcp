// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build e2e

// Multi-account state isolation. Two accounts pointing at the same
// testenv backend user with distinct names get distinct device IDs
// (see manager.generatedDeviceIDs) and therefore distinct sub-buckets
// in bbolt. The test exercises both accounts independently and then
// inspects the bbolt file directly to prove the per-account
// sub-buckets are populated separately and do not bleed into one
// shared bucket.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/bbolt"

	"activesync-mcp/lib/server"
)

func TestE2E_MultiAccountStateIsolation(t *testing.T) {
	stateDir := t.TempDir()
	cfgPath := writeMultiAccountConfig(t, stateDir, "alpha", "beta")
	cs := e2eClientWithConfig(t, cfgPath)

	// 1. Both accounts surface in accounts_list.
	var listOut server.AccountsListOutput
	callTool(t, cs, "accounts_list", server.AccountsListInput{}, &listOut)
	gotNames := map[string]bool{}
	for _, a := range listOut.Accounts {
		gotNames[a.Name] = true
	}
	if !gotNames["alpha"] || !gotNames["beta"] || len(gotNames) != 2 {
		t.Fatalf("accounts_list = %+v; want exactly {alpha, beta}", listOut.Accounts)
	}

	// 2. Each account can FolderSync independently. Doing this back-to-back
	//    catches any cross-account state interleaving in the Manager.
	var alphaOut, betaOut server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "alpha"}, &alphaOut)
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "beta"}, &betaOut)
	if len(alphaOut.Folders) == 0 || len(betaOut.Folders) == 0 {
		t.Fatalf("expected folders for both accounts; alpha=%d beta=%d",
			len(alphaOut.Folders), len(betaOut.Folders))
	}

	// 3. Calling alpha's email_list_folders again returns the cached
	//    hierarchy without falling over on the second-call delta path.
	//    This is the cousin of the cursor-leakage regression that
	//    inspired this whole audit (#78).
	var alphaSecond server.EmailListFoldersOutput
	callTool(t, cs, "email_list_folders",
		server.EmailListFoldersInput{Account: "alpha"}, &alphaSecond)
	if len(alphaSecond.Folders) != len(alphaOut.Folders) {
		t.Errorf("alpha second call returned %d folders, want %d (delta should not lose state)",
			len(alphaSecond.Folders), len(alphaOut.Folders))
	}

	// 4. Tear down the client so the server releases the bbolt lock
	//    before we inspect the file.
	if err := cs.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	// Closing the client only sends EOF to the server's stdin; give
	// it a beat to actually exit and release the flock.
	waitForBboltUnlock(t, filepath.Join(stateDir, "state.db"))

	// 5. Walk the bbolt file directly. Each account must own its own
	//    sub-bucket under both `synckey` and `folders`, with at least
	//    one entry. Critically: there must be no shared/default
	//    sub-bucket that would indicate accidental state pooling.
	assertPerAccountBuckets(t, filepath.Join(stateDir, "state.db"),
		[]string{"alpha", "beta"})
}

// writeMultiAccountConfig emits a TOML with two [[account]] blocks
// pointing at the same testenv user. The two account names produce
// distinct device IDs via generatedDeviceIDs so Z-Push treats them as
// independent devices on the same mailbox — the device-id collision
// would otherwise corrupt server-side state and fail FolderSync.
func writeMultiAccountConfig(t *testing.T, stateDir, alphaName, betaName string) string {
	t.Helper()
	const accountTpl = `[[account]]
name             = %q
server_url       = %q
username         = "integration"
device_type      = "GoActiveSyncTest"
as_version       = "14.0"
secret           = { command = ["printf", "%%s", "integration"] }
default_access   = "rw"

`
	url := e2eServerURL()
	body := fmt.Sprintf("state_dir = %q\nlog_level = \"info\"\n\n",
		escapeTOMLE2E(stateDir)) +
		fmt.Sprintf(accountTpl, alphaName, url) +
		fmt.Sprintf(accountTpl, betaName, url)
	cfgPath := filepath.Join(t.TempDir(), "multiaccount.toml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// waitForBboltUnlock polls until the bbolt file can be opened
// exclusively (i.e. the server has actually exited and released the
// flock). The MCP client.Close above only signals EOF; the goroutines
// take a moment to wind down. Bounded by a short deadline.
func waitForBboltUnlock(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: 100 * time.Millisecond})
		if err == nil {
			_ = db.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("bbolt at %s never unlocked within 5s of client close", path)
}

// assertPerAccountBuckets opens the bbolt file read-only and validates
// that under each of {synckey, folders} the only sub-buckets are
// exactly the named accounts, each non-empty. Any extra sub-bucket
// (e.g. a literal "default" or an empty-string key) signals state
// leakage.
func assertPerAccountBuckets(t *testing.T, dbPath string, accounts []string) {
	t.Helper()
	db, err := bbolt.Open(dbPath, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen bbolt: %v", err)
	}
	defer db.Close()

	for _, root := range []string{"synckey", "folders"} {
		got := map[string]int{}
		if err := db.View(func(tx *bbolt.Tx) error {
			b := tx.Bucket([]byte(root))
			if b == nil {
				return fmt.Errorf("root bucket %q missing", root)
			}
			return b.ForEach(func(k, v []byte) error {
				if v != nil {
					return fmt.Errorf("root %q has key %q with value (expected sub-buckets only)", root, k)
				}
				sub := b.Bucket(k)
				count := 0
				_ = sub.ForEach(func(_, _ []byte) error { count++; return nil })
				got[string(k)] = count
				return nil
			})
		}); err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
		if len(got) != len(accounts) {
			t.Errorf("%s sub-buckets = %v; want exactly %v", root, got, accounts)
		}
		for _, name := range accounts {
			n, ok := got[name]
			if !ok {
				t.Errorf("%s/%s missing", root, name)
				continue
			}
			if n == 0 {
				t.Errorf("%s/%s empty (expected at least one entry from FolderSync)", root, name)
			}
		}
	}
}
