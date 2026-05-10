package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// withConfigFile writes a TOML config to a temp dir and returns its path.
func withConfigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// withHooks installs in-memory hook fakes and restores originals.
func withHooks(t *testing.T) (store map[string]string, getErr *error, delErr *error) {
	t.Helper()
	store = map[string]string{}
	var gErr, dErr error
	getErr = &gErr
	delErr = &dErr

	prevSet, prevGet, prevDel, prevRead := hookSet, hookGet, hookDelete, hookReadPassword
	hookSet = func(svc, user, pw string) error {
		store[svc+"/"+user] = pw
		return nil
	}
	hookGet = func(svc, user string) (string, error) {
		if gErr != nil {
			return "", gErr
		}
		v, ok := store[svc+"/"+user]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return v, nil
	}
	hookDelete = func(svc, user string) error {
		if dErr != nil {
			return dErr
		}
		if _, ok := store[svc+"/"+user]; !ok {
			return keyring.ErrNotFound
		}
		delete(store, svc+"/"+user)
		return nil
	}
	hookReadPassword = func(_ string, _ io.Reader, _ io.Writer) (string, error) {
		return "test-password", nil
	}
	t.Cleanup(func() {
		hookSet, hookGet, hookDelete, hookReadPassword = prevSet, prevGet, prevDel, prevRead
	})
	return store, getErr, delErr
}

const testConfig = `
[[account]]
name             = "work"
server_url       = "https://x"
username         = "u"
secret           = { keyring_service = "svc", keyring_account = "work-acct" }

[[account]]
name             = "cmdy"
server_url       = "https://y"
username         = "u"
secret           = { command = ["echo", "hi"] }
`

func tempStdio(t *testing.T) (*os.File, *os.File, func() (string, string)) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	// Close before t.TempDir's cleanup runs. Cleanups are LIFO, so
	// registering this AFTER os.CreateTemp ensures it runs BEFORE the
	// temp-dir removal — which Windows refuses while a file inside it
	// is still open.
	t.Cleanup(func() { _ = out.Close() })
	errF, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = errF.Close() })
	read := func() (string, string) {
		if _, err := out.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := errF.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		o, _ := io.ReadAll(out)
		e, _ := io.ReadAll(errF)
		return string(o), string(e)
	}
	return out, errF, read
}

func TestKeyringSet_storesPassword(t *testing.T) {
	store, _, _ := withHooks(t)
	cfg := withConfigFile(t, testConfig)

	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set", "--account", "work"}, &cfg, out, errF)
	if code != exitOK {
		_, e := read()
		t.Fatalf("exit %d, stderr: %s", code, e)
	}
	if got := store["svc/work-acct"]; got != "test-password" {
		t.Errorf("stored = %q", got)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "stored:") {
		t.Errorf("stdout missing confirmation: %q", stdout)
	}
}

func TestKeyringSet_emptyPasswordRejected(t *testing.T) {
	withHooks(t)
	hookReadPassword = func(string, io.Reader, io.Writer) (string, error) { return "", nil }
	cfg := withConfigFile(t, testConfig)

	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set", "--account", "work"}, &cfg, out, errF)
	if code != exitUsageErr {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "empty") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyringGet_setAndNotSet(t *testing.T) {
	store, _, _ := withHooks(t)
	cfg := withConfigFile(t, testConfig)

	// Not set yet.
	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"get", "--account", "work"}, &cfg, out, errF)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "not set") {
		t.Errorf("stdout: %q", stdout)
	}

	// Set then re-check.
	store["svc/work-acct"] = "ZZsecretZZ"
	out2, errF2, read2 := tempStdio(t)
	code = runKeyring([]string{"get", "--account", "work"}, &cfg, out2, errF2)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	stdout, _ = read2()
	if !strings.Contains(stdout, "set:") || strings.Contains(stdout, "not set") {
		t.Errorf("stdout: %q", stdout)
	}
	// Crucially, the value is never printed.
	if strings.Contains(stdout, "ZZsecretZZ") {
		t.Errorf("get leaked the password value: %q", stdout)
	}
}

func TestKeyringGet_otherError(t *testing.T) {
	_, getErr, _ := withHooks(t)
	*getErr = errors.New("backend down")
	cfg := withConfigFile(t, testConfig)

	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"get", "--account", "work"}, &cfg, out, errF)
	if code != exitRuntime {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "backend down") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyringDelete_alreadyAbsent(t *testing.T) {
	withHooks(t)
	cfg := withConfigFile(t, testConfig)

	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"delete", "--account", "work"}, &cfg, out, errF)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "already absent") {
		t.Errorf("stdout: %q", stdout)
	}
}

func TestKeyringDelete_present(t *testing.T) {
	store, _, _ := withHooks(t)
	store["svc/work-acct"] = "ZZsecretZZ"
	cfg := withConfigFile(t, testConfig)

	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"delete", "--account", "work"}, &cfg, out, errF)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "deleted") {
		t.Errorf("stdout: %q", stdout)
	}
	if _, still := store["svc/work-acct"]; still {
		t.Error("entry not deleted")
	}
}

func TestKeyring_commandAccountRejected(t *testing.T) {
	withHooks(t)
	cfg := withConfigFile(t, testConfig)

	// "cmdy" uses secret.command; the keyring subcommands should refuse.
	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set", "--account", "cmdy"}, &cfg, out, errF)
	if code != exitConfig {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "does not use the keyring") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyring_unknownAccount(t *testing.T) {
	withHooks(t)
	cfg := withConfigFile(t, testConfig)

	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set", "--account", "nope"}, &cfg, out, errF)
	if code != exitConfig {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "no account named") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyring_missingAccountFlag(t *testing.T) {
	withHooks(t)
	cfg := withConfigFile(t, testConfig)

	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set"}, &cfg, out, errF)
	if code != exitUsageErr {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "--account is required") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyring_unknownAction(t *testing.T) {
	withHooks(t)
	cfg := withConfigFile(t, testConfig)
	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"frobnicate", "--account", "work"}, &cfg, out, errF)
	if code != exitUsageErr {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "unknown action") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyring_noAction(t *testing.T) {
	withHooks(t)
	cfg := withConfigFile(t, "")
	out, errF, read := tempStdio(t)
	code := runKeyring(nil, &cfg, out, errF)
	if code != exitUsageErr {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "missing action") {
		t.Errorf("stderr: %q", e)
	}
}

func TestReadPasswordTTY_nonTTY(t *testing.T) {
	in := strings.NewReader("hunter2\nignored")
	out := &strings.Builder{}
	pw, err := readPasswordTTY("", in, out)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "hunter2" {
		t.Errorf("pw = %q", pw)
	}
}

func TestReadPasswordTTY_carriageReturnTrim(t *testing.T) {
	in := strings.NewReader("hunter2\r\n")
	out := &strings.Builder{}
	pw, err := readPasswordTTY("", in, out)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "hunter2" {
		t.Errorf("pw = %q", pw)
	}
}

func TestKeyringSet_passwordReadError(t *testing.T) {
	withHooks(t)
	hookReadPassword = func(string, io.Reader, io.Writer) (string, error) {
		return "", errors.New("tty unreadable")
	}
	cfg := withConfigFile(t, testConfig)
	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set", "--account", "work"}, &cfg, out, errF)
	if code != exitRuntime {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "tty unreadable") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyringSet_storeError(t *testing.T) {
	withHooks(t)
	hookSet = func(string, string, string) error { return errors.New("backend down") }
	cfg := withConfigFile(t, testConfig)
	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set", "--account", "work"}, &cfg, out, errF)
	if code != exitRuntime {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "backend down") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyringDelete_otherError(t *testing.T) {
	_, _, delErr := withHooks(t)
	*delErr = errors.New("backend down")
	cfg := withConfigFile(t, testConfig)
	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"delete", "--account", "work"}, &cfg, out, errF)
	if code != exitRuntime {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "backend down") {
		t.Errorf("stderr: %q", e)
	}
}

func TestKeyring_badConfigPath(t *testing.T) {
	withHooks(t)
	cfg := "/no/such/file"
	out, errF, read := tempStdio(t)
	code := runKeyring([]string{"set", "--account", "x"}, &cfg, out, errF)
	if code != exitConfig {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "open config") {
		t.Errorf("stderr: %q", e)
	}
}
