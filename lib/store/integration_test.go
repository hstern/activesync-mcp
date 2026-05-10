// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build integration

// Tier 2 integration tests for the bbolt-backed StateStore. The
// existing unit tests under bbolt_test.go cover the in-process happy
// path; these target the file-system surface that varies most across
// OSes — exclusive locking (bbolt's flock vs Windows LockFileEx) and
// reopen-after-close persistence.

package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStore_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	// Round 1: write some state and close cleanly.
	{
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		state := db.AccountState("alpha")
		if err := state.SetPolicyKey(context.Background(), "POL-42"); err != nil {
			t.Fatalf("SetPolicyKey: %v", err)
		}
		if err := state.SetSyncKey(context.Background(), "inbox", "SK-1"); err != nil {
			t.Fatalf("SetSyncKey inbox: %v", err)
		}
		if err := state.SetSyncKey(context.Background(), "calendar", "SK-2"); err != nil {
			t.Fatalf("SetSyncKey calendar: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}

	// Round 2: reopen the same file and assert every value survived.
	// This is the path that breaks if the on-disk format drifts or
	// if a platform-specific buffering quirk (Windows write-through
	// vs Unix mmap) loses data.
	{
		db, err := Open(path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		state := db.AccountState("alpha")
		if got, _ := state.PolicyKey(context.Background()); got != "POL-42" {
			t.Errorf("PolicyKey after reopen = %q, want POL-42", got)
		}
		if got, _ := state.SyncKey(context.Background(), "inbox"); got != "SK-1" {
			t.Errorf("SyncKey(inbox) after reopen = %q, want SK-1", got)
		}
		if got, _ := state.SyncKey(context.Background(), "calendar"); got != "SK-2" {
			t.Errorf("SyncKey(calendar) after reopen = %q, want SK-2", got)
		}
	}
}

func TestStore_LockContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	// Second open of the same file must time out (the bbolt option is
	// 5s) — bbolt holds an exclusive flock on Unix and LockFileEx on
	// Windows. Without this guard, two `activesync-mcp serve` instances
	// could corrupt each other's state.
	second, err := Open(path)
	if err == nil {
		_ = second.Close()
		t.Fatal("second concurrent Open should fail; both succeeded")
	}
}

func TestStore_LockReleasedAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// After Close, the lock must be released so subsequent Opens
	// succeed. Catches a regression where a deferred resource holds
	// the file handle past Close.
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
}

func TestStore_ResetAccountClearsOnlyTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, acct := range []string{"alpha", "beta"} {
		st := db.AccountState(acct)
		_ = st.SetPolicyKey(context.Background(), acct+"-pol")
		_ = st.SetSyncKey(context.Background(), "inbox", acct+"-inbox")
		_ = st.SetSyncKey(context.Background(), "calendar", acct+"-cal")
	}

	if err := db.ResetAccount("alpha"); err != nil {
		t.Fatalf("ResetAccount: %v", err)
	}

	// alpha is wiped. PolicyKey returns "" for absent (no policy yet);
	// SyncKey returns "0" for absent (EAS bootstrap key — means "do a
	// fresh sync"). Both signal "no state remembered".
	a := db.AccountState("alpha")
	if got, _ := a.PolicyKey(context.Background()); got != "" {
		t.Errorf("alpha PolicyKey post-reset = %q, want empty", got)
	}
	if got, _ := a.SyncKey(context.Background(), "inbox"); got != "0" {
		t.Errorf("alpha SyncKey(inbox) post-reset = %q, want \"0\" (bootstrap)", got)
	}

	// beta is untouched.
	b := db.AccountState("beta")
	if got, _ := b.PolicyKey(context.Background()); got != "beta-pol" {
		t.Errorf("beta PolicyKey post-reset = %q, want beta-pol", got)
	}
	if got, _ := b.SyncKey(context.Background(), "inbox"); got != "beta-inbox" {
		t.Errorf("beta SyncKey(inbox) post-reset = %q, want beta-inbox", got)
	}
}

func TestStore_OpenCreatesParentDir(t *testing.T) {
	// Path with a non-existent parent — Open should mkdir-p so users
	// can point state_dir at a brand-new location and have it Just Work.
	path := filepath.Join(t.TempDir(), "nested", "deeper", "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open with non-existent parent: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
}
