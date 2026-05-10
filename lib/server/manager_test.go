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
