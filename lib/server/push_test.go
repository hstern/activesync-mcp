package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
	"github.com/hstern/go-activesync/wbxml"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pushFakeServer responds to Provision, FolderSync (returning an inbox),
// and Ping. By default Ping returns Status=2 with the inbox marked
// changed. If pingStatusSequence is non-empty the i-th Ping returns
// the i-th status code (clamped to the last entry once the sequence
// is exhausted) — used by the Status=7 recovery test.
type pushFakeServer struct {
	mu                 sync.Mutex
	provCalls          int
	pingCalls          int32
	fsCalls            int32
	pingStatusSequence []int
	pingTimes          []time.Time
}

func (f *pushFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	switch r.URL.Query().Get("Cmd") {
	case "Settings":
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "Provision":
		f.mu.Lock()
		f.provCalls++
		call := f.provCalls
		f.mu.Unlock()
		key := "TKEY"
		if call == 2 {
			key = "FKEY"
		}
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageProvision, "Policies",
					wbxml.E(wbxml.PageProvision, "Policy",
						wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
						wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text(key)),
					),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "FolderSync":
		atomic.AddInt32(&f.fsCalls, 1)
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS")),
				wbxml.E(wbxml.PageFolderHierarchy, "Changes",
					wbxml.E(wbxml.PageFolderHierarchy, "Count", wbxml.Text("1")),
					wbxml.E(wbxml.PageFolderHierarchy, "Add",
						wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("inbox-1")),
						wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text("0")),
						wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Inbox")),
						wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text("2")),
					),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(b)
	case "Ping":
		n := atomic.AddInt32(&f.pingCalls, 1)
		f.mu.Lock()
		f.pingTimes = append(f.pingTimes, time.Now())
		status := 2
		if len(f.pingStatusSequence) > 0 {
			idx := int(n) - 1
			if idx >= len(f.pingStatusSequence) {
				idx = len(f.pingStatusSequence) - 1
			}
			status = f.pingStatusSequence[idx]
		}
		f.mu.Unlock()
		root := wbxml.E(wbxml.PagePing, "Ping",
			wbxml.E(wbxml.PagePing, "Status", wbxml.Text(strconv.Itoa(status))),
		)
		// Status 2 (changes) carries the changed-folder list. Other
		// statuses (1=no changes, 7=hierarchy stale, …) don't.
		if status == 2 {
			root.Children = append(root.Children, wbxml.E(wbxml.PagePing, "Folders",
				wbxml.E(wbxml.PagePing, "Folder",
					wbxml.E(wbxml.PagePing, "Id", wbxml.Text("inbox-1")),
					wbxml.E(wbxml.PagePing, "Class", wbxml.Text("Email")),
				),
			))
		}
		b, _ := wbxml.Marshal(&wbxml.Document{Root: root}, wbxml.DefaultRegistry())
		w.Write(b)
	default:
		http.Error(w, "unhandled "+r.URL.Query().Get("Cmd"), 400)
	}
}

func TestPushController_notifiesOnChanges(t *testing.T) {
	f := &pushFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()

	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Push:          true,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	store := &fakeStateProvider{}
	mgr := NewManager(cfg, store, res, staticDeviceIDs{"alpha": "dev"})
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ServerOptions{})

	push := NewPushController(cfg, mgr, mcpSrv)
	push.heartbeat = 100 * time.Millisecond // exercise the loop quickly

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	started := push.Start(ctx)
	if started != 1 {
		t.Fatalf("started = %d", started)
	}

	// Wait until at least one Ping has been issued; bound the wait.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&f.pingCalls) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	push.Close()
	if atomic.LoadInt32(&f.pingCalls) == 0 {
		t.Error("Ping was never called")
	}
}

func TestPushController_recoversFromHierarchyOutOfDate(t *testing.T) {
	// First Ping returns Status=7 (FolderHierarchyOutOfDate), the rest
	// return Status=2 (changes available). The watcher must re-FolderSync
	// and immediately retry the Ping — without sleeping the 2s minimum
	// backoff. We assert that by measuring the gap between the first
	// and second Ping plus checking that FolderSync ran twice (initial
	// + recovery).
	f := &pushFakeServer{pingStatusSequence: []int{7, 2}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()

	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Push:          true,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	mgr := NewManager(cfg, &fakeStateProvider{}, res, staticDeviceIDs{"alpha": "dev"})
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ServerOptions{})

	push := NewPushController(cfg, mgr, mcpSrv)
	push.heartbeat = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if n := push.Start(ctx); n != 1 {
		t.Fatalf("started = %d", n)
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&f.pingCalls) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	push.Close()

	pings := atomic.LoadInt32(&f.pingCalls)
	if pings < 2 {
		t.Fatalf("ping calls = %d, want >= 2 (recovery should have retried)", pings)
	}
	if got := atomic.LoadInt32(&f.fsCalls); got < 2 {
		t.Errorf("FolderSync calls = %d, want >= 2 (initial + recovery)", got)
	}
	f.mu.Lock()
	gap := f.pingTimes[1].Sub(f.pingTimes[0])
	f.mu.Unlock()
	// The recovery path skips the 2s backoff, so the gap should be
	// well under 1s in practice (just an HTTP roundtrip + FolderSync
	// against the in-process httptest server).
	if gap >= 1*time.Second {
		t.Errorf("gap between Ping #1 and #2 = %v; want <1s (recovery should not back off)", gap)
	}
}

func TestPushController_restartLifecycle(t *testing.T) {
	// Two PushController lifecycles back-to-back against the same
	// Manager and the same fake server. The second Start must produce
	// fresh Pings — proving Close cancels the watcher cleanly and
	// nothing in the Manager pins the old controller's state in a way
	// that blocks a fresh subscription. This is the regression you'd
	// hit if `Close` left a goroutine alive holding the eas.Client or
	// if a stale `cancels` map entry survived restart.
	f := &pushFakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()

	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Push:          true,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "p"}}
	mgr := NewManager(cfg, &fakeStateProvider{}, res, staticDeviceIDs{"alpha": "dev"})
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ServerOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	waitForPings := func(min int32) {
		t.Helper()
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if atomic.LoadInt32(&f.pingCalls) >= min {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("only %d Pings observed; wanted >= %d within 1.5s",
			atomic.LoadInt32(&f.pingCalls), min)
	}

	// --- Lifecycle 1 -------------------------------------------------
	p1 := NewPushController(cfg, mgr, mcpSrv)
	p1.heartbeat = 100 * time.Millisecond
	if n := p1.Start(ctx); n != 1 {
		t.Fatalf("first Start = %d, want 1", n)
	}
	waitForPings(1)
	pingsAfterFirst := atomic.LoadInt32(&f.pingCalls)
	p1.Close()

	// Give the watcher goroutine a moment to actually exit so the
	// second lifecycle's Pings are unambiguously from p2.
	time.Sleep(100 * time.Millisecond)
	pingsAtBoundary := atomic.LoadInt32(&f.pingCalls)

	// --- Lifecycle 2 -------------------------------------------------
	p2 := NewPushController(cfg, mgr, mcpSrv)
	p2.heartbeat = 100 * time.Millisecond
	if n := p2.Start(ctx); n != 1 {
		t.Fatalf("second Start = %d, want 1", n)
	}
	waitForPings(pingsAtBoundary + 1)
	p2.Close()

	if got := atomic.LoadInt32(&f.pingCalls); got <= pingsAfterFirst {
		t.Errorf("second lifecycle produced no new Pings: total=%d, after first=%d", got, pingsAfterFirst)
	}
}

func TestPushController_doubleCloseIsSafe(t *testing.T) {
	// Close is documented to be safe to call multiple times (the
	// server's context-cancellation path may fire it after the user
	// has already called it). Assert no panic and no double-cancel.
	cfg := &config.Config{}
	mgr := NewManager(cfg, &fakeStateProvider{}, &fakeResolver{}, staticDeviceIDs{})
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ServerOptions{})
	p := NewPushController(cfg, mgr, mcpSrv)
	p.Close()
	p.Close() // must not panic on the nil cancels map
}

func TestPushController_zeroAccountsStartsNothing(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "noPush",
			ServerURL:     "https://x",
			Username:      "u",
			DefaultAccess: config.AccessRO,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "noPush"},
		}},
	}
	mgr := NewManager(cfg, &fakeStateProvider{}, &fakeResolver{}, staticDeviceIDs{})
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ServerOptions{})
	push := NewPushController(cfg, mgr, mcpSrv)
	if n := push.Start(context.Background()); n != 0 {
		t.Errorf("started = %d, want 0", n)
	}
	push.Close()
}

func TestSubscribedFolders_filtersToInboxAndCalendar(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
				return &eas.FolderSyncResult{
					Added: []eas.Folder{
						{ServerID: "i", Type: eas.FolderTypeInbox},
						{ServerID: "t", Type: eas.FolderTypeTasks},
						{ServerID: "c", Type: eas.FolderTypeCalendar},
						{ServerID: "n", Type: eas.FolderTypeNotes},
					},
				}, nil
			},
		},
	}
	p := &PushController{logger: slog.New(slog.DiscardHandler)}
	got, err := p.subscribedFolders(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (inbox + calendar)", len(got))
	}
	classes := map[string]bool{}
	for _, f := range got {
		classes[f.Class] = true
	}
	if !classes["Email"] || !classes["Calendar"] {
		t.Errorf("got classes = %v, want Email + Calendar", got)
	}
}

func TestSubscribedFolders_emptyWhenNoMatchingTypes(t *testing.T) {
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
				return &eas.FolderSyncResult{
					Added: []eas.Folder{
						{ServerID: "t", Type: eas.FolderTypeTasks},
					},
				}, nil
			},
		},
	}
	p := &PushController{logger: slog.New(slog.DiscardHandler)}
	got, err := p.subscribedFolders(context.Background(), mock)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestSubscribedFolders_folderSyncError(t *testing.T) {
	wantErr := errors.New("network down")
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			FolderSyncFunc: func(context.Context) (*eas.FolderSyncResult, error) {
				return nil, wantErr
			},
		},
	}
	p := &PushController{logger: slog.New(slog.DiscardHandler)}
	_, err := p.subscribedFolders(context.Background(), mock)
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped %v", err, wantErr)
	}
}

func TestPushController_clientInitErrorReturnsCleanly(t *testing.T) {
	// An account flagged push=true but with an unresolvable secret
	// should not crash the watcher; it logs and returns. We can't
	// observe the goroutine directly, but Start should report 1
	// and the controller should remain Close-able.
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name: "alpha", ServerURL: "https://x", Username: "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Push:          true,
			Secret:        config.SecretRef{KeyringService: "s", KeyringAccount: "alpha"},
		}},
	}
	res := &fakeResolver{err: errors.New("locked")}
	mgr := NewManager(cfg, &fakeStateProvider{}, res, staticDeviceIDs{"alpha": "dev"})
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, &mcp.ServerOptions{})
	push := NewPushController(cfg, mgr, mcpSrv)
	if n := push.Start(context.Background()); n != 1 {
		t.Fatalf("started = %d", n)
	}
	// Goroutine returns silently; allow it to finish before Close so
	// Close's cancel is a no-op rather than a race.
	time.Sleep(50 * time.Millisecond)
	push.Close()
}
