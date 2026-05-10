package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sync"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"
)

// StateProvider abstracts the bbolt store so tests can swap in
// in-memory state without spinning up a real database file.
type StateProvider interface {
	AccountState(account string) eas.StateStore
}

// SecretResolver matches *config.SecretResolver and lets tests inject
// a fake password lookup.
type SecretResolver interface {
	Resolve(ctx context.Context, a *config.Account) (string, error)
}

// Manager owns the per-account EAS clients. Clients are constructed
// lazily on first use, the password is fetched from the secret store
// at that point, and the client is provisioned automatically.
//
// One Manager is shared across all MCP tool handlers. It is safe for
// concurrent use.
type Manager struct {
	cfg       *config.Config
	store     StateProvider
	resolver  SecretResolver
	httpFor   func(a *config.Account) *http.Client
	deviceIDs DeviceIDProvider

	mu      sync.Mutex
	clients map[string]*managedClient
}

// DeviceIDProvider returns a stable 32-hex device ID for an account.
// The default implementation persists to the bbolt store via a separate
// bucket; tests may inject a fixed value.
type DeviceIDProvider interface {
	DeviceID(account string) (string, error)
}

// managedClient bundles a ready-to-use eas.Client with provisioning state.
type managedClient struct {
	client      eas.Client
	provisioned bool
	provisionMu sync.Mutex
}

// deviceInfoFor builds the DeviceInformation payload for an account.
// Sourced from the account config and process environment so two
// activesync-mcp installations don't look like the same device.
func deviceInfoFor(a *config.Account) eas.DeviceInformation {
	return eas.DeviceInformation{
		Model:        coalesce(a.DeviceType, "MCP"),
		FriendlyName: "activesync-mcp/" + a.Name,
		OS:           runtimeOSLabel(),
		OSLanguage:   "en",
		UserAgent:    coalesce(a.UserAgent, "go-activesync/0.1"),
	}
}

func coalesce(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func runtimeOSLabel() string { return runtime.GOOS + "/" + runtime.GOARCH }

// NewManager constructs a Manager. store provides per-account StateStores;
// resolver fetches passwords; deviceIDs supplies stable per-account IDs.
func NewManager(cfg *config.Config, store StateProvider, resolver SecretResolver, deviceIDs DeviceIDProvider) *Manager {
	return &Manager{
		cfg:       cfg,
		store:     store,
		resolver:  resolver,
		deviceIDs: deviceIDs,
		httpFor:   defaultHTTPClient,
		clients:   make(map[string]*managedClient),
	}
}

// NewDefaultManager wires the production-default Manager: the supplied
// bbolt-backed store, the OS-keyring-backed secret resolver, and a
// generated device-id provider seeded from deviceSeed. deviceSeed should
// be a stable per-installation value (e.g. read from the bbolt store) so
// device IDs remain stable across process restarts; an empty string asks
// for a random seed each call (NOT recommended for production).
func NewDefaultManager(cfg *config.Config, store StateProvider, deviceSeed string) (*Manager, error) {
	dev, err := newGeneratedDeviceIDs(deviceSeed)
	if err != nil {
		return nil, err
	}
	return NewManager(cfg, store, config.DefaultResolver(), dev), nil
}

// Client returns a provisioned EAS client for the named account. First
// call performs lookup, password resolution, client construction, and
// the Provision handshake. Subsequent calls return the cached client.
func (m *Manager) Client(ctx context.Context, accountName string) (eas.Client, error) {
	a := m.cfg.FindAccount(accountName)
	if a == nil {
		return nil, fmt.Errorf("manager: unknown account %q", accountName)
	}

	mc, err := m.getOrBuildClient(ctx, a)
	if err != nil {
		return nil, err
	}

	mc.provisionMu.Lock()
	defer mc.provisionMu.Unlock()
	if !mc.provisioned {
		// Negotiate the highest protocol version both ends support
		// before any other command. Best-effort: a server that refuses
		// OPTIONS will still let us try Provision at our default.
		if _, err := mc.client.NegotiateVersion(ctx); err != nil {
			_ = err
		}
		// MS-ASPROV §3.1.5.1: in EAS 14.0+ clients SHOULD send a
		// Settings/DeviceInformation/Set before the initial Provision.
		if err := mc.client.SettingsDeviceInformation(ctx, deviceInfoFor(a)); err != nil {
			_ = err
		}
		if err := mc.client.Provision(ctx); err != nil {
			return nil, fmt.Errorf("manager: account %q: provision: %w", accountName, err)
		}
		mc.provisioned = true
	}
	return mc.client, nil
}

func (m *Manager) getOrBuildClient(ctx context.Context, a *config.Account) (*managedClient, error) {
	m.mu.Lock()
	if mc, ok := m.clients[a.Name]; ok {
		m.mu.Unlock()
		return mc, nil
	}
	m.mu.Unlock()

	deviceID := a.DeviceID
	if deviceID == "" {
		var err error
		deviceID, err = m.deviceIDs.DeviceID(a.Name)
		if err != nil {
			return nil, fmt.Errorf("manager: account %q: device id: %w", a.Name, err)
		}
	}

	cfg := eas.Config{
		ServerURL:  a.ServerURL,
		Username:   a.Username,
		DeviceID:   deviceID,
		DeviceType: a.DeviceType,
		ASVersion:  a.ASVersion,
		UserAgent:  a.UserAgent,
		HTTPClient: m.httpFor(a),
		State:      m.store.AccountState(a.Name),
	}

	switch a.Secret.AuthScheme {
	case "", "basic":
		// Resolve once: Basic auth doesn't need a refresh callback.
		pw, err := m.resolver.Resolve(ctx, a)
		if err != nil {
			return nil, fmt.Errorf("manager: account %q: resolve secret: %w", a.Name, err)
		}
		cfg.Password = pw

	case "bearer":
		// Wrap resolver in a per-request callback so OAuth refresh tokens
		// can be re-fetched (the resolver's command/keyring may itself
		// implement caching or refresh logic).
		acct := a
		cfg.AuthHeader = func(ctx context.Context) (string, error) {
			tok, err := m.resolver.Resolve(ctx, acct)
			if err != nil {
				return "", err
			}
			return "Bearer " + tok, nil
		}
		cfg.RetryOn401 = true

	case "ntlm":
		pw, err := m.resolver.Resolve(ctx, a)
		if err != nil {
			return nil, fmt.Errorf("manager: account %q: resolve secret: %w", a.Name, err)
		}
		cfg.Password = pw
		// NTLM is a transport-layer handshake; wrap the existing
		// http.Client transport. Username can be in DOMAIN\user form;
		// go-ntlmssp parses that.
		cfg.HTTPClient = wrapNTLMTransport(cfg.HTTPClient)

	case "negotiate":
		// SPNEGO/Kerberos. No password from the resolver — credentials
		// come from a keytab or the user's existing ccache. We still
		// require Username to be set so eas.NewClient is happy and the
		// initial validation passes; the value is unused on the wire.
		cfg.Password = "x" // placeholder for NewClient validation
		wrapped, err := wrapKerberosTransport(cfg.HTTPClient, a, cfg.ServerURL)
		if err != nil {
			return nil, fmt.Errorf("manager: account %q: kerberos: %w", a.Name, err)
		}
		cfg.HTTPClient = wrapped
		// SPNEGO sets Authorization itself; suppress our default header.
		cfg.AuthHeader = func(_ context.Context) (string, error) { return "", nil }

	default:
		return nil, fmt.Errorf("manager: account %q: unknown auth_scheme %q", a.Name, a.Secret.AuthScheme)
	}

	c, err := eas.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("manager: account %q: %w", a.Name, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.clients[a.Name]; ok {
		// Lost the race; discard ours.
		return existing, nil
	}
	mc := &managedClient{client: c}
	m.clients[a.Name] = mc
	return mc, nil
}

// wrapNTLMTransport returns an http.Client whose transport understands
// the NTLM challenge/response handshake. Pulled into a separate file
// so the NTLM dependency can be swapped or stubbed in tests.
func wrapNTLMTransport(orig *http.Client) *http.Client {
	out := &http.Client{
		Timeout:       orig.Timeout,
		CheckRedirect: orig.CheckRedirect,
		Jar:           orig.Jar,
	}
	base := orig.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out.Transport = ntlmRoundTripper(base)
	return out
}

// CheckClass returns nil if the account permits the given access for the
// class, or an error suitable for surfacing through MCP otherwise. Used
// as defense-in-depth alongside the per-tool account-enum scoping.
func (m *Manager) CheckClass(accountName string, class config.Class, needsWrite bool) error {
	a := m.cfg.FindAccount(accountName)
	if a == nil {
		return fmt.Errorf("unknown account %q", accountName)
	}
	if needsWrite && !a.CanWrite(class) {
		return fmt.Errorf("account %q does not permit writes to class %q", accountName, class)
	}
	return nil
}

func defaultHTTPClient(a *config.Account) *http.Client {
	tlsCfg, err := a.TLS.Load(context.Background(), config.DefaultResolver(), a.AllowInsecure)
	if err != nil {
		return &http.Client{Transport: failTransport{err: fmt.Errorf("tls: %w", err)}}
	}
	if tlsCfg == nil && a.ProxyURL == "" {
		return http.DefaultClient
	}
	tr := &http.Transport{}
	if tlsCfg != nil {
		tr.TLSClientConfig = tlsCfg
	}
	if a.ProxyURL != "" {
		u, err := url.Parse(a.ProxyURL)
		if err != nil {
			return &http.Client{Transport: failTransport{err: fmt.Errorf("proxy_url: %w", err)}}
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr}
}

// failTransport always returns the configured error.
type failTransport struct{ err error }

func (f failTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// --- DeviceID provider --------------------------------------------------

// staticDeviceIDs is the trivial DeviceIDProvider used by tests.
type staticDeviceIDs map[string]string

func (s staticDeviceIDs) DeviceID(account string) (string, error) {
	if id, ok := s[account]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no device id for %q", account)
}

// generatedDeviceIDs derives a 32-hex device ID from a single seed,
// stable per account name. Used only when the application doesn't
// supply a persistent provider; the device ID is one of the rare
// pieces of state that should outlive a process restart, but bbolt
// is currently the only store and we don't want to import it from
// internal/server. Phase 5 wires a bbolt-backed provider.
func newGeneratedDeviceIDs(seedHex string) (DeviceIDProvider, error) {
	if seedHex == "" {
		// Generate a fresh random seed; warns the caller that IDs will
		// drift across process restarts.
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		seedHex = hex.EncodeToString(buf)
	}
	if len(seedHex) < 8 {
		return nil, errors.New("device id seed must be >= 8 hex chars")
	}
	return generatedDeviceIDs{seed: seedHex}, nil
}

type generatedDeviceIDs struct{ seed string }

func (g generatedDeviceIDs) DeviceID(account string) (string, error) {
	sum := sha256.Sum256([]byte(g.seed + ":" + account))
	return hex.EncodeToString(sum[:16]), nil // 32 hex chars
}
