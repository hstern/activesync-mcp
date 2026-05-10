package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

// Test the easprobe end-to-end against an in-process EAS-shaped server.

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeEAS returns a server that handles OPTIONS, the two-phase Provision,
// and a single FolderSync call. It's enough to drive easprobe through to
// completion.
func fakeEAS(t *testing.T) *httptest.Server {
	t.Helper()
	var (
		mu             sync.Mutex
		provisionCalls int
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("MS-ASProtocolVersions", "14.1")
			w.Header().Set("MS-ASProtocolCommands", "FolderSync,Provision,Sync,SendMail")
			w.WriteHeader(http.StatusOK)
			return
		}
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
			mu.Lock()
			provisionCalls++
			call := provisionCalls
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
		case "FolderSync":
			doc := &wbxml.Document{
				Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
					wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS-1")),
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
		default:
			http.Error(w, "unhandled cmd", 400)
		}
	}))
}

func TestEasprobe_endToEnd(t *testing.T) {
	srv := fakeEAS(t)
	defer srv.Close()

	cfg := writeConfig(t, `
[[account]]
name       = "smoke"
server_url = "`+srv.URL+`"
username   = "henry"
secret     = { command = ["printf", "supersecret"] }
`)

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := run([]string{"-account", "smoke", "-config", cfg}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"OPTIONS", "14.1", "Provision", "FolderSync", "Inbox", "FS-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\n--- stdout ---\n%s", want, out)
		}
	}
}

func TestEasprobe_missingAccount(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := run([]string{}, stdout, stderr)
	if code != 2 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "-account is required") {
		t.Errorf("stderr=%s", stderr.String())
	}
}

func TestEasprobe_unknownAccount(t *testing.T) {
	cfg := writeConfig(t, `
[[account]]
name       = "x"
server_url = "https://x"
username   = "u"
secret     = { command = ["printf", "p"] }
`)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := run([]string{"-config", cfg, "-account", "missing"}, stdout, stderr)
	if code != 2 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "no account named") {
		t.Errorf("stderr=%s", stderr.String())
	}
}

func TestEasprobe_badConfigPath(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := run([]string{"-config", "/no/such/config.toml", "-account", "x"}, stdout, stderr)
	if code != 2 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "open config") {
		t.Errorf("stderr=%s", stderr.String())
	}
}

func TestEasprobe_secretCommandFails(t *testing.T) {
	// `false` exits non-zero; the secret resolver wraps that.
	cfg := writeConfig(t, `
[[account]]
name       = "x"
server_url = "https://x"
username   = "u"
secret     = { command = ["false"] }
`)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := run([]string{"-config", cfg, "-account", "x"}, stdout, stderr)
	if code != 3 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "secret command") {
		t.Errorf("stderr=%s", stderr.String())
	}
}

func TestEasprobe_optionsFails(t *testing.T) {
	// Server returns 500 on OPTIONS; the run loop should bail with 3.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			http.Error(w, "boom", 500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := writeConfig(t, `
[[account]]
name       = "x"
server_url = "`+srv.URL+`"
username   = "u"
secret     = { command = ["printf", "p"] }
`)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := run([]string{"-config", cfg, "-account", "x"}, stdout, stderr)
	if code != 3 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "OPTIONS") {
		t.Errorf("stderr=%s", stderr.String())
	}
}
