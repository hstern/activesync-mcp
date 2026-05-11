package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PushController runs one long-poll Ping goroutine per push-enabled
// account. Each goroutine subscribes to the account's Inbox and default
// Calendar, blocks on Ping, and fans out a resource-update notification
// over the connected MCP sessions when the server reports changes.
//
// PushController is safe for concurrent use. Stop the controller via
// Close (also called automatically when the server's context is
// cancelled).
type PushController struct {
	cfg       *config.Config
	mgr       *Manager
	server    *mcp.Server
	logger    *slog.Logger
	heartbeat time.Duration

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// NewPushController constructs a controller for the given server. It
// does not start any goroutines until Start is called.
func NewPushController(cfg *config.Config, mgr *Manager, srv *mcp.Server) *PushController {
	return &PushController{
		cfg:       cfg,
		mgr:       mgr,
		server:    srv,
		logger:    slog.Default(),
		heartbeat: 5 * time.Minute,
		cancels:   make(map[string]context.CancelFunc),
	}
}

// Start launches one watcher per push-enabled account. Returns the
// number of watchers started (0 if no accounts have push=true).
func (p *PushController) Start(ctx context.Context) int {
	count := 0
	for i := range p.cfg.Accounts {
		a := &p.cfg.Accounts[i]
		if !a.Push {
			continue
		}
		acctCtx, cancel := context.WithCancel(ctx)
		p.mu.Lock()
		p.cancels[a.Name] = cancel
		p.mu.Unlock()
		go p.watchAccount(acctCtx, a.Name)
		count++
	}
	return count
}

// Close cancels all watchers and waits briefly for them to finish.
func (p *PushController) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.cancels {
		c()
	}
	p.cancels = nil
}

// pingStatusFolderHierarchyOutOfDate is MS-ASCMD §2.2.3.66's Status 7
// — the client's view of the folder hierarchy is stale and Ping must
// be retried after a fresh FolderSync. Z-Push's BackendCombined fires
// it reliably on the first Ping after a cold-boot FolderSync; other
// servers fire it whenever another client adds or removes a folder
// under us.
const pingStatusFolderHierarchyOutOfDate = 7

// watchAccount runs the per-account Ping loop until ctx is cancelled.
// It expands subscribed folders by calling FolderSync once at startup,
// then loops Ping calls. On any error, it backs off briefly before
// retrying so a transient network blip doesn't tight-loop.
//
// Status=7 (FolderHierarchyOutOfDate) is special-cased: re-FolderSync,
// re-subscribe, retry the Ping immediately. The recovery is allowed
// once per backoff cycle so a server that keeps returning Status=7
// can't pin us in a tight loop — repeated Status=7s fall through to
// the normal backoff path.
func (p *PushController) watchAccount(ctx context.Context, accountName string) {
	logger := p.logger.With("account", accountName)
	logger.Info("push watcher starting")
	defer logger.Info("push watcher stopped")

	c, err := p.mgr.Client(ctx, accountName)
	if err != nil {
		logger.Error("push watcher: client init failed", "err", err)
		return
	}

	folders, err := p.subscribedFolders(ctx, c)
	if err != nil {
		logger.Error("push watcher: subscribed folders", "err", err)
		return
	}
	if len(folders) == 0 {
		logger.Warn("push watcher: no folders to watch (no Inbox or Calendar?)")
		return
	}
	logger.Info("push watcher: subscribed", "folders", len(folders))

	heartbeat := int(p.heartbeat / time.Second)
	backoff := 2 * time.Second
	recoveredHierarchy := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		res, err := c.Ping(ctx, heartbeat, folders)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			if res != nil && res.Status == pingStatusFolderHierarchyOutOfDate && !recoveredHierarchy {
				logger.Info("push watcher: hierarchy stale; re-FolderSyncing")
				refreshed, ferr := p.subscribedFolders(ctx, c)
				if ferr == nil && len(refreshed) > 0 {
					folders = refreshed
					recoveredHierarchy = true
					logger.Info("push watcher: re-subscribed", "folders", len(folders))
					continue
				}
				logger.Warn("push watcher: hierarchy refresh failed", "err", ferr, "folders", len(refreshed))
			}
			logger.Warn("push watcher: ping error", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 2 * time.Second
		recoveredHierarchy = false

		if res.Status == 2 && len(res.ChangedFolders) > 0 {
			for _, fid := range res.ChangedFolders {
				p.notifyFolderChanged(ctx, accountName, fid)
			}
		}
	}
}

// subscribedFolders runs FolderSync and picks the inbox + default
// calendar to subscribe to. EAS limits the Folders count to 16 by
// default; we deliberately stay much lower.
func (p *PushController) subscribedFolders(ctx context.Context, c eas.Client) ([]eas.PingFolder, error) {
	fs, err := c.FolderSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("FolderSync: %w", err)
	}
	var out []eas.PingFolder
	for _, f := range fs.Added {
		switch f.Type {
		case eas.FolderTypeInbox:
			out = append(out, eas.PingFolder{ID: f.ServerID, Class: "Email"})
		case eas.FolderTypeCalendar:
			out = append(out, eas.PingFolder{ID: f.ServerID, Class: "Calendar"})
		}
	}
	return out, nil
}

// notifyFolderChanged emits an MCP resource-update notification to all
// connected sessions. The URI scheme is "activesync://account/folder";
// clients can subscribe to specific URIs or just observe the stream.
func (p *PushController) notifyFolderChanged(ctx context.Context, account, folderID string) {
	uri := fmt.Sprintf("activesync://%s/%s", account, folderID)
	if err := p.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{
		URI: uri,
	}); err != nil {
		p.logger.Warn("push: notify failed", "uri", uri, "err", err)
	}
}
