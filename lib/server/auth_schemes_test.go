package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/wbxml"
)

// authCaptureServer records the Authorization header on every request
// and responds to Settings + Provision (so the lazy-provision path
// completes without errors).
type authCaptureServer struct {
	provCalls int32
	captured  []string // Authorization header per call
}

func (a *authCaptureServer) handle(w http.ResponseWriter, r *http.Request) {
	a.captured = append(a.captured, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	switch r.URL.Query().Get("Cmd") {
	case "Settings":
		w.Write(mustMarshal(&wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			),
		}))
	case "Provision":
		n := atomic.AddInt32(&a.provCalls, 1)
		key := "TKEY"
		if n == 2 {
			key = "FKEY"
		}
		w.Write(mustMarshal(&wbxml.Document{
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
		}))
	default:
		w.WriteHeader(200)
	}
}

func mustMarshal(d *wbxml.Document) []byte {
	b, err := wbxml.Marshal(d, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return b
}

func TestManager_basicAuthScheme_sendsBasic(t *testing.T) {
	cap := &authCaptureServer{}
	srv := httptest.NewServer(http.HandlerFunc(cap.handle))
	defer srv.Close()

	cfg := singleAccountConfig(srv.URL, "henry", config.SecretRef{
		KeyringService: "x", KeyringAccount: "alpha", AuthScheme: "basic",
	})
	mgr := NewManager(cfg, &fakeStateProvider{},
		&fakeResolver{pw: map[string]string{"alpha": "hunter2"}},
		staticDeviceIDs{"alpha": "dev"})

	if _, err := mgr.Client(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if len(cap.captured) == 0 || !strings.HasPrefix(cap.captured[0], "Basic ") {
		t.Errorf("first auth header = %q", cap.captured)
	}
}

func TestManager_bearerAuthScheme_sendsBearer(t *testing.T) {
	cap := &authCaptureServer{}
	srv := httptest.NewServer(http.HandlerFunc(cap.handle))
	defer srv.Close()

	cfg := singleAccountConfig(srv.URL, "henry", config.SecretRef{
		KeyringService: "x", KeyringAccount: "alpha", AuthScheme: "bearer",
	})
	mgr := NewManager(cfg, &fakeStateProvider{},
		&fakeResolver{pw: map[string]string{"alpha": "tok-abc"}},
		staticDeviceIDs{"alpha": "dev"})

	if _, err := mgr.Client(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	for _, a := range cap.captured {
		if !strings.HasPrefix(a, "Bearer tok-") {
			t.Errorf("auth header without Bearer prefix: %q", a)
		}
	}
}

func TestManager_unknownAuthScheme_rejected(t *testing.T) {
	cfg := singleAccountConfig("https://x", "henry", config.SecretRef{
		KeyringService: "x", KeyringAccount: "alpha", AuthScheme: "weird",
	})
	mgr := NewManager(cfg, &fakeStateProvider{},
		&fakeResolver{pw: map[string]string{"alpha": "p"}},
		staticDeviceIDs{"alpha": "dev"})
	_, err := mgr.Client(context.Background(), "alpha")
	if err == nil || !strings.Contains(err.Error(), "auth_scheme") {
		t.Errorf("err = %v", err)
	}
}

func TestNTLMRoundTripperWrapsBase(t *testing.T) {
	// Real NTLM handshake is hard to stub; just confirm wrapNTLMTransport
	// produces the go-ntlmssp Negotiator transport rather than the
	// underlying http.Transport.
	out := wrapNTLMTransport(&http.Client{Transport: http.DefaultTransport})
	if out.Transport == nil {
		t.Fatal("Transport not set")
	}
	if got := stringTypeName(out.Transport); !strings.Contains(got, "Negotiator") {
		t.Errorf("transport = %q, want Negotiator wrapper", got)
	}
}

// singleAccountConfig builds a single-account Config used by the auth
// scheme tests.
func singleAccountConfig(serverURL, username string, secret config.SecretRef) *config.Config {
	return &config.Config{
		Accounts: []config.Account{{
			Name:          "alpha",
			ServerURL:     serverURL,
			Username:      username,
			ASVersion:     "14.1",
			DefaultAccess: config.AccessRO,
			Secret:        secret,
		}},
	}
}

func stringTypeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", v)
}
