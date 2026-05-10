package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/wbxml"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pushFakeServer responds to Provision, FolderSync (returning an inbox),
// and Ping (returning Status=2 with the inbox marked changed). The
// goroutine should fan out a notification at least once.
type pushFakeServer struct {
	mu        sync.Mutex
	provCalls int
	pingCalls int32
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
		atomic.AddInt32(&f.pingCalls, 1)
		// Status 2 = changes available on inbox-1.
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PagePing, "Ping",
				wbxml.E(wbxml.PagePing, "Status", wbxml.Text("2")),
				wbxml.E(wbxml.PagePing, "Folders",
					wbxml.E(wbxml.PagePing, "Folder",
						wbxml.E(wbxml.PagePing, "Id", wbxml.Text("inbox-1")),
						wbxml.E(wbxml.PagePing, "Class", wbxml.Text("Email")),
					),
				),
			),
		}
		b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
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
