package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunDoctor_allHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("MS-ASProtocolVersions", "14.0,14.1")
		w.Header().Set("MS-ASProtocolCommands", "FolderSync,Provision")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := withConfigFile(t, `
[[account]]
name       = "alpha"
server_url = "`+srv.URL+`"
username   = "u"
secret     = { command = ["printf", "p"] }
`)
	out, errF, read := tempStdio(t)
	code := runDoctor([]string{}, &cfg, out, errF)
	if code != exitOK {
		_, e := read()
		t.Fatalf("exit %d, stderr=%s", code, e)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "alpha") || !strings.Contains(stdout, "OK") {
		t.Errorf("stdout: %s", stdout)
	}
}

func TestRunDoctor_versionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("MS-ASProtocolVersions", "12.1")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := withConfigFile(t, `
[[account]]
name       = "alpha"
server_url = "`+srv.URL+`"
username   = "u"
secret     = { command = ["printf", "p"] }
`)
	out, errF, read := tempStdio(t)
	code := runDoctor([]string{}, &cfg, out, errF)
	if code != exitRuntime {
		t.Errorf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "FAIL") {
		t.Errorf("stdout: %s", stdout)
	}
}

func TestRunDoctor_credentialFailure(t *testing.T) {
	cfg := withConfigFile(t, `
[[account]]
name       = "alpha"
server_url = "https://x"
username   = "u"
secret     = { command = ["false"] }
`)
	out, errF, read := tempStdio(t)
	code := runDoctor([]string{}, &cfg, out, errF)
	if code != exitRuntime {
		t.Errorf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "FAIL credentials") {
		t.Errorf("stdout: %s", stdout)
	}
}

func TestRunDoctor_badConfig(t *testing.T) {
	out, errF, read := tempStdio(t)
	cfg := "/no/such/file"
	code := runDoctor([]string{}, &cfg, out, errF)
	if code != exitConfig {
		t.Errorf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "activesync-mcp setup") {
		t.Errorf("stderr: %s", e)
	}
}

func TestRunDoctor_unreachableServer(t *testing.T) {
	// Point at a port that nothing is listening on. The OPTIONS call
	// should fail at the transport layer; doctor surfaces the failure
	// per-account and exits with exitRuntime.
	cfg := withConfigFile(t, `
[[account]]
name       = "alpha"
server_url = "http://127.0.0.1:1"
username   = "u"
secret     = { command = ["printf", "p"] }
`)
	out, errF, read := tempStdio(t)
	code := runDoctor([]string{}, &cfg, out, errF)
	if code != exitRuntime {
		t.Errorf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "FAIL OPTIONS") {
		t.Errorf("stdout: %s", stdout)
	}
}
