package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"activesync-mcp/lib/config"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/wbxml"
)

// fakeStateProvider returns one MemoryState per account.
type fakeStateProvider struct {
	mu     sync.Mutex
	states map[string]eas.StateStore
}

func (f *fakeStateProvider) AccountState(name string) eas.StateStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string]eas.StateStore{}
	}
	if s, ok := f.states[name]; ok {
		return s
	}
	s := eas.NewMemoryState()
	f.states[name] = s
	return s
}

// fakeResolver returns canned passwords keyed by account name.
type fakeResolver struct {
	pw  map[string]string
	err error
}

func (f *fakeResolver) Resolve(_ context.Context, a *config.Account) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if pw, ok := f.pw[a.Name]; ok {
		return pw, nil
	}
	return "", errors.New("no pw for " + a.Name)
}

// provisionFakeServer responds to the two-phase Provision handshake plus
// any subsequent commands with empty 200 OKs.
func provisionFakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	var (
		mu    sync.Mutex
		count int
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch r.URL.Query().Get("Cmd") {
		case "Settings":
			// Manager calls Settings/DeviceInformation before the first
			// Provision; respond with a benign Status=1.
			doc := &wbxml.Document{
				Root: wbxml.E(wbxml.PageSettings, "Settings",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				),
			}
			b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(b)
			return
		case "Provision":
			mu.Lock()
			count++
			call := count
			mu.Unlock()
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
		default:
			w.WriteHeader(200)
		}
	}))
}

func newTestManager(t *testing.T, srv *httptest.Server) *Manager {
	t.Helper()
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DeviceType:    "TEST",
			DefaultAccess: config.AccessRO,
			Access:        map[string]string{"calendar": config.AccessRW},
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}, {
			Name:          "beta",
			ServerURL:     srv.URL,
			Username:      "u",
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Secret:        config.SecretRef{KeyringService: "x", KeyringAccount: "beta"},
		}},
	}
	res := &fakeResolver{pw: map[string]string{"alpha": "pa", "beta": "pb"}}
	store := &fakeStateProvider{}
	dev, _ := newGeneratedDeviceIDs("0123456789abcdef")
	return NewManager(cfg, store, res, dev)
}

func TestManager_lazyProvision(t *testing.T) {
	srv := provisionFakeServer(t)
	defer srv.Close()
	m := newTestManager(t, srv)

	c, err := m.Client(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
	// Repeat call: should not re-provision.
	c2, err := m.Client(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if c != c2 {
		t.Errorf("client not cached: %p vs %p", c, c2)
	}
}

func TestManager_unknownAccount(t *testing.T) {
	srv := provisionFakeServer(t)
	defer srv.Close()
	m := newTestManager(t, srv)
	_, err := m.Client(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "unknown account") {
		t.Errorf("err = %v", err)
	}
}

func TestManager_resolverError(t *testing.T) {
	srv := provisionFakeServer(t)
	defer srv.Close()
	m := newTestManager(t, srv)
	m.resolver = &fakeResolver{err: errors.New("locked")}
	_, err := m.Client(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "resolve secret") {
		t.Errorf("err = %v", err)
	}
}

func TestManager_CheckClass(t *testing.T) {
	srv := provisionFakeServer(t)
	defer srv.Close()
	m := newTestManager(t, srv)

	if err := m.CheckClass("alpha", config.ClassEmail, false); err != nil {
		t.Errorf("ro email read on alpha: %v", err)
	}
	if err := m.CheckClass("alpha", config.ClassEmail, true); err == nil {
		t.Error("write to ro class should fail")
	}
	if err := m.CheckClass("alpha", config.ClassCalendar, true); err != nil {
		t.Errorf("write to rw class: %v", err)
	}
	if err := m.CheckClass("missing", config.ClassEmail, false); err == nil {
		t.Error("unknown account should fail")
	}
}

func TestGeneratedDeviceIDs_stablePerAccount(t *testing.T) {
	d, err := newGeneratedDeviceIDs("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := d.DeviceID("alpha")
	b, _ := d.DeviceID("beta")
	a2, _ := d.DeviceID("alpha")
	if len(a) != 32 || len(b) != 32 {
		t.Errorf("len: %d %d", len(a), len(b))
	}
	if a != a2 {
		t.Error("not stable per account")
	}
	if a == b {
		t.Error("collision between accounts")
	}
}

func TestNewGeneratedDeviceIDs_seedTooShort(t *testing.T) {
	_, err := newGeneratedDeviceIDs("ab")
	if err == nil {
		t.Error("want error for short seed")
	}
}

func TestStaticDeviceIDs(t *testing.T) {
	s := staticDeviceIDs{"a": "deadbeef"}
	id, err := s.DeviceID("a")
	if err != nil || id != "deadbeef" {
		t.Errorf("a: id=%q err=%v", id, err)
	}
	if _, err := s.DeviceID("missing"); err == nil {
		t.Error("missing should error")
	}
}

func TestNewGeneratedDeviceIDs_emptySeedGeneratesRandom(t *testing.T) {
	d, err := newGeneratedDeviceIDs("")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	id, err := d.DeviceID("alpha")
	if err != nil || len(id) != 32 {
		t.Errorf("id=%q err=%v (want 32-char id)", id, err)
	}
	// A second instance should produce a different ID (different seed).
	d2, err := newGeneratedDeviceIDs("")
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := d2.DeviceID("alpha")
	if id == id2 {
		t.Error("two empty-seed instances produced identical IDs (random source not used)")
	}
}

func TestNewDefaultManager(t *testing.T) {
	cfg := &config.Config{}
	m, err := NewDefaultManager(cfg, &fakeStateProvider{}, "0123456789abcdef")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if m == nil {
		t.Fatal("nil manager")
	}
	// Bad seed (too short) should error out.
	if _, err := NewDefaultManager(cfg, &fakeStateProvider{}, "ab"); err == nil {
		t.Error("short seed should propagate")
	}
}

func TestDefaultHTTPClient_returnsDefaultWhenNoCustomization(t *testing.T) {
	a := &config.Account{Username: "u"}
	c := defaultHTTPClient(a)
	if c != http.DefaultClient {
		t.Errorf("expected http.DefaultClient when no TLS/proxy, got %p", c)
	}
}

func TestDefaultHTTPClient_badProxyURL(t *testing.T) {
	a := &config.Account{Username: "u", ProxyURL: "://bad-url"}
	c := defaultHTTPClient(a)
	if c == nil {
		t.Fatal("nil client")
	}
	// Doing any RoundTrip should surface the parse error via failTransport.
	_, err := c.Transport.RoundTrip(&http.Request{})
	if err == nil || !strings.Contains(err.Error(), "proxy_url") {
		t.Errorf("err = %v; want one mentioning proxy_url", err)
	}
}

func TestDefaultHTTPClient_proxyURL(t *testing.T) {
	a := &config.Account{Username: "u", ProxyURL: "http://proxy.example:3128"}
	c := defaultHTTPClient(a)
	if c == nil || c.Transport == nil {
		t.Fatal("nil client/transport")
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", c.Transport)
	}
	if tr.Proxy == nil {
		t.Error("Proxy should be set")
	}
}

func TestRuntimeOSLabel_format(t *testing.T) {
	got := runtimeOSLabel()
	if !strings.Contains(got, "/") {
		t.Errorf("runtimeOSLabel() = %q; want OS/arch with slash", got)
	}
}

func TestCoalesce(t *testing.T) {
	if got := coalesce("a", "b"); got != "a" {
		t.Errorf("coalesce non-empty: %q", got)
	}
	if got := coalesce("", "fallback"); got != "fallback" {
		t.Errorf("coalesce empty: %q", got)
	}
	if got := coalesce("", ""); got != "" {
		t.Errorf("coalesce both empty: %q", got)
	}
}

func TestManager_provisionFailure(t *testing.T) {
	// A server that returns Status=140 (RemoteWipe) on Provision is
	// the simplest concrete failure: NewClient succeeds, but Provision
	// surfaces the error wrapped by Manager.Client.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch r.URL.Query().Get("Cmd") {
		case "Settings":
			b, _ := wbxml.Marshal(&wbxml.Document{
				Root: wbxml.E(wbxml.PageSettings, "Settings",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				),
			}, wbxml.DefaultRegistry())
			w.Write(b)
		case "Provision":
			b, _ := wbxml.Marshal(&wbxml.Document{
				Root: wbxml.E(wbxml.PageProvision, "Provision",
					wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("140")),
				),
			}, wbxml.DefaultRegistry())
			w.Write(b)
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()
	m := newTestManager(t, srv)
	_, err := m.Client(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "provision") {
		t.Errorf("err = %v, want one wrapped under 'provision'", err)
	}
}

func TestManager_settingsAndNegotiateErrorsAreSwallowed(t *testing.T) {
	// Settings/DeviceInformation and NegotiateVersion are best-effort:
	// even if they error, Provision still runs and Client returns
	// successfully. We exercise that by returning HTTP 500 for any
	// Cmd other than Provision (Settings + OPTIONS) — the manager
	// should still hand back a working client.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		if r.URL.Query().Get("Cmd") == "Provision" {
			b, _ := wbxml.Marshal(&wbxml.Document{
				Root: wbxml.E(wbxml.PageProvision, "Provision",
					wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageProvision, "Policies",
						wbxml.E(wbxml.PageProvision, "Policy",
							wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
							wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
							wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text("OK")),
						),
					),
				),
			}, wbxml.DefaultRegistry())
			w.Write(b)
			return
		}
		http.Error(w, "settings/options unhappy", 500)
	}))
	defer srv.Close()
	m := newTestManager(t, srv)
	c, err := m.Client(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("err = %v (Settings/Negotiate failures should be swallowed)", err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
}

func TestDefaultHTTPClient_TLSLoadError(t *testing.T) {
	// A configured ClientCertFile that doesn't exist makes the TLS
	// loader fail; defaultHTTPClient wraps the failure in a
	// failTransport so the next RoundTrip surfaces it.
	a := &config.Account{
		Username: "u",
		TLS:      config.TLSConfig{ClientCertFile: "/no/such/cert.pem", ClientKeyFile: "/no/such/key.pem"},
	}
	c := defaultHTTPClient(a)
	_, err := c.Transport.RoundTrip(&http.Request{})
	if err == nil || !strings.Contains(err.Error(), "tls:") {
		t.Errorf("err = %v, want one wrapped under 'tls:'", err)
	}
}

func TestPrepareListCursor_emptyResetsToZero(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{
		Name: "alpha", DefaultAccess: config.AccessRO,
		Secret: config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
	}}}
	store := &fakeStateProvider{}
	m := NewManager(cfg, store, &fakeResolver{}, staticDeviceIDs{})
	st := store.AccountState("alpha")
	// Pre-populate with a stale cursor that a prior session would
	// have left behind.
	if err := st.SetSyncKey(context.Background(), "inbox", "K42"); err != nil {
		t.Fatal(err)
	}

	if err := m.PrepareListCursor(context.Background(), "alpha", "inbox", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := st.SyncKey(context.Background(), "inbox")
	if got != "0" {
		t.Errorf("empty cursor: SyncKey = %q, want \"0\"", got)
	}
}

func TestPrepareListCursor_passThroughResumes(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{
		Name: "alpha", Secret: config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
	}}}
	store := &fakeStateProvider{}
	m := NewManager(cfg, store, &fakeResolver{}, staticDeviceIDs{})

	if err := m.PrepareListCursor(context.Background(), "alpha", "inbox", "K42"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.AccountState("alpha").SyncKey(context.Background(), "inbox")
	if got != "K42" {
		t.Errorf("SyncKey = %q, want K42", got)
	}
}

func TestDeviceInfoFor_usesAccountFields(t *testing.T) {
	a := &config.Account{
		Name:       "alpha",
		Username:   "u",
		DeviceType: "MyDevice",
		UserAgent:  "Custom/1.0",
	}
	info := deviceInfoFor(a)
	if info.Model != "MyDevice" {
		t.Errorf("Model = %q, want MyDevice", info.Model)
	}
	if info.UserAgent != "Custom/1.0" {
		t.Errorf("UserAgent = %q, want Custom/1.0", info.UserAgent)
	}
	if !strings.Contains(info.FriendlyName, "alpha") {
		t.Errorf("FriendlyName should include account name, got %q", info.FriendlyName)
	}
	// With empty fields, defaults kick in.
	info2 := deviceInfoFor(&config.Account{})
	if info2.Model == "" || info2.UserAgent == "" {
		t.Errorf("defaults should populate Model+UserAgent: %+v", info2)
	}
}
