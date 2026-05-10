package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hstern/go-activesync/eas"
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

func TestFolderCache_appliesAddUpdateDelete(t *testing.T) {
	db := tempDB(t)
	cache := db.FolderCache("work")

	// First sync: server returns the entire hierarchy in Added.
	if err := cache.Apply(&eas.FolderSyncResult{
		SyncKey: "FS1",
		Added: []eas.Folder{
			{ServerID: "inbox", DisplayName: "Inbox", Type: eas.FolderTypeInbox},
			{ServerID: "sent", DisplayName: "Sent", Type: eas.FolderTypeSentItems},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := cache.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("after first apply, got %d folders, want 2", len(got))
	}

	// Second sync: an update + a new folder + a delete.
	if err := cache.Apply(&eas.FolderSyncResult{
		SyncKey: "FS2",
		Added:   []eas.Folder{{ServerID: "drafts", DisplayName: "Drafts", Type: eas.FolderTypeDrafts}},
		Updated: []eas.Folder{{ServerID: "inbox", DisplayName: "Inbox (renamed)", Type: eas.FolderTypeInbox}},
		Deleted: []string{"sent"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = cache.All()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]eas.Folder{}
	for _, f := range got {
		byID[f.ServerID] = f
	}
	if len(byID) != 2 {
		t.Errorf("after delete + add, want 2 folders, got %d (%v)", len(byID), byID)
	}
	if byID["inbox"].DisplayName != "Inbox (renamed)" {
		t.Errorf("Updated didn't replace existing folder: %+v", byID["inbox"])
	}
	if _, has := byID["sent"]; has {
		t.Error("Deleted folder is still present")
	}
	if _, has := byID["drafts"]; !has {
		t.Error("Newly Added folder missing")
	}
}

func TestFolderCache_perAccountIsolation(t *testing.T) {
	db := tempDB(t)
	work := db.FolderCache("work")
	personal := db.FolderCache("personal")

	if err := work.Apply(&eas.FolderSyncResult{
		Added: []eas.Folder{{ServerID: "w-inbox", DisplayName: "Work Inbox"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := personal.Apply(&eas.FolderSyncResult{
		Added: []eas.Folder{{ServerID: "p-inbox", DisplayName: "Personal Inbox"}},
	}); err != nil {
		t.Fatal(err)
	}
	wf, _ := work.All()
	pf, _ := personal.All()
	if len(wf) != 1 || wf[0].ServerID != "w-inbox" {
		t.Errorf("work folders bled: %+v", wf)
	}
	if len(pf) != 1 || pf[0].ServerID != "p-inbox" {
		t.Errorf("personal folders bled: %+v", pf)
	}
}

func TestFolderCache_emptyAccountReturnsEmpty(t *testing.T) {
	db := tempDB(t)
	got, err := db.FolderCache("never-used").All()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d folders for fresh account, want 0", len(got))
	}
}

func TestFolderCache_persistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db1.FolderCache("work").Apply(&eas.FolderSyncResult{
		Added: []eas.Folder{{ServerID: "inbox", DisplayName: "Inbox"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db1.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	got, err := db2.FolderCache("work").All()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ServerID != "inbox" {
		t.Errorf("not persisted: %+v", got)
	}
}

func TestResetAccount_clearsFolderCache(t *testing.T) {
	db := tempDB(t)
	if err := db.FolderCache("work").Apply(&eas.FolderSyncResult{
		Added: []eas.Folder{{ServerID: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ResetAccount("work"); err != nil {
		t.Fatal(err)
	}
	got, err := db.FolderCache("work").All()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ResetAccount didn't clear folder cache: %+v", got)
	}
}

func TestOpen_lockHeldByAnotherProcess(t *testing.T) {
	// bbolt's exclusive flock is the simulated-second-process trap
	// users hit when they accidentally launch two `activesync-mcp
	// serve` instances against the same state_dir. The cryptic
	// "timeout" they used to see now becomes an actionable message
	// naming the root cause.
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, err = Open(path)
	if err == nil {
		t.Fatal("want error opening a second handle to the same db")
	}
	if !strings.Contains(err.Error(), "another activesync-mcp process") {
		t.Errorf("err = %v; want one mentioning the other process", err)
	}
}

func TestOpen_unwritablePath(t *testing.T) {
	// Use an existing regular file as the parent directory: MkdirAll
	// fails because the parent isn't a directory. The error path is
	// what we want to exercise.
	dir := t.TempDir()
	regular := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(filepath.Join(regular, "child", "state.db"))
	if err == nil {
		t.Fatal("want error opening under a non-directory parent")
	}
	if !strings.Contains(err.Error(), "store:") {
		t.Errorf("err = %v, want one prefixed 'store:'", err)
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
