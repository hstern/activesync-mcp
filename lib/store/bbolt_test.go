package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func tempDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPolicyKey_RoundTrip(t *testing.T) {
	db := tempDB(t)
	st := db.AccountState("work")
	ctx := context.Background()

	pk, err := st.PolicyKey(ctx)
	if err != nil || pk != "" {
		t.Errorf("initial: %q err=%v", pk, err)
	}
	if err := st.SetPolicyKey(ctx, "K42"); err != nil {
		t.Fatal(err)
	}
	pk, _ = st.PolicyKey(ctx)
	if pk != "K42" {
		t.Errorf("after set: %q", pk)
	}
	// Empty string clears.
	if err := st.SetPolicyKey(ctx, ""); err != nil {
		t.Fatal(err)
	}
	pk, _ = st.PolicyKey(ctx)
	if pk != "" {
		t.Errorf("after clear: %q", pk)
	}
}

func TestSyncKey_PerFolderIsolation(t *testing.T) {
	db := tempDB(t)
	work := db.AccountState("work")
	personal := db.AccountState("personal")
	ctx := context.Background()

	if err := work.SetSyncKey(ctx, "inbox", "W1"); err != nil {
		t.Fatal(err)
	}
	if err := work.SetSyncKey(ctx, "calendar", "W2"); err != nil {
		t.Fatal(err)
	}
	if err := personal.SetSyncKey(ctx, "inbox", "P1"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		st      any
		folder  string
		want    string
		account string
	}{
		{work, "inbox", "W1", "work"},
		{work, "calendar", "W2", "work"},
		{work, "unknown", "0", "work"},
		{personal, "inbox", "P1", "personal"},
		{personal, "calendar", "0", "personal"},
	}
	for _, c := range cases {
		got, err := c.st.(interface {
			SyncKey(context.Context, string) (string, error)
		}).SyncKey(ctx, c.folder)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.account, c.folder, err)
		}
		if got != c.want {
			t.Errorf("%s/%s: got %q, want %q", c.account, c.folder, got, c.want)
		}
	}
}

func TestSetSyncKey_emptyAndZeroDelete(t *testing.T) {
	db := tempDB(t)
	st := db.AccountState("a")
	ctx := context.Background()
	_ = st.SetSyncKey(ctx, "f", "K")
	_ = st.SetSyncKey(ctx, "f", "0")
	if k, _ := st.SyncKey(ctx, "f"); k != "0" {
		t.Errorf("zero should clear: %q", k)
	}
	_ = st.SetSyncKey(ctx, "f", "K")
	_ = st.SetSyncKey(ctx, "f", "")
	if k, _ := st.SyncKey(ctx, "f"); k != "0" {
		t.Errorf("empty should clear: %q", k)
	}
}

func TestPersistenceAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	db1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st1 := db1.AccountState("work")
	_ = st1.SetPolicyKey(context.Background(), "K1")
	_ = st1.SetSyncKey(context.Background(), "inbox", "S1")
	if err := db1.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	st2 := db2.AccountState("work")
	if k, _ := st2.PolicyKey(context.Background()); k != "K1" {
		t.Errorf("policy key not persisted: %q", k)
	}
	if k, _ := st2.SyncKey(context.Background(), "inbox"); k != "S1" {
		t.Errorf("sync key not persisted: %q", k)
	}
}

func TestResetAccount(t *testing.T) {
	db := tempDB(t)
	work := db.AccountState("work")
	other := db.AccountState("other")
	ctx := context.Background()

	_ = work.SetPolicyKey(ctx, "WK")
	_ = work.SetSyncKey(ctx, "inbox", "WI")
	_ = other.SetPolicyKey(ctx, "OK")
	_ = other.SetSyncKey(ctx, "inbox", "OI")

	if err := db.ResetAccount("work"); err != nil {
		t.Fatal(err)
	}
	if k, _ := work.PolicyKey(ctx); k != "" {
		t.Errorf("work policy not cleared: %q", k)
	}
	if k, _ := work.SyncKey(ctx, "inbox"); k != "0" {
		t.Errorf("work sync not cleared: %q", k)
	}
	// Other account untouched.
	if k, _ := other.PolicyKey(ctx); k != "OK" {
		t.Errorf("other policy disturbed: %q", k)
	}
	if k, _ := other.SyncKey(ctx, "inbox"); k != "OI" {
		t.Errorf("other sync disturbed: %q", k)
	}
}

func TestConcurrent(t *testing.T) {
	db := tempDB(t)
	st := db.AccountState("a")
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = st.SetSyncKey(ctx, "f", "key")
			_, _ = st.SyncKey(ctx, "f")
			_ = st.SetPolicyKey(ctx, "p")
			_, _ = st.PolicyKey(ctx)
			_ = i
		}(i)
	}
	wg.Wait()
}
