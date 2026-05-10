// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"activesync-mcp/lib/config"
)

func TestDefaultCCachePath_returnsKrb5ccUnderTmp(t *testing.T) {
	got := defaultCCachePath()
	// Either the standard MIT path (/tmp/krb5cc_<uid>) when user.Current
	// works, or the KRB5CCNAME env fallback. In a normal test env, it
	// should be the former.
	if got == "" {
		t.Fatal("defaultCCachePath returned empty string")
	}
	if !strings.HasPrefix(got, "/tmp/krb5cc_") && os.Getenv("KRB5CCNAME") != got {
		t.Errorf("unexpected path: %q (no KRB5CCNAME env override either)", got)
	}
}

func TestWrapKerberosTransport_missingKrb5Conf(t *testing.T) {
	a := &config.Account{
		Username: "u@EXAMPLE",
		Kerberos: config.KerberosConfig{
			Krb5ConfPath: filepath.Join(t.TempDir(), "no-such-krb5.conf"),
		},
	}
	_, err := wrapKerberosTransport(&http.Client{}, a, "https://eas.example/")
	if err == nil {
		t.Fatal("want error for missing krb5.conf")
	}
	if !strings.Contains(err.Error(), "load") {
		t.Errorf("err = %v; want one mentioning load failure", err)
	}
}

func TestWrapKerberosTransport_badKeytab(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "krb5.conf")
	if err := os.WriteFile(confPath, []byte(minimalKrb5Conf), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &config.Account{
		Username: "u",
		Kerberos: config.KerberosConfig{
			Krb5ConfPath: confPath,
			KeytabFile:   filepath.Join(dir, "no-such.keytab"),
			Realm:        "EXAMPLE.COM",
		},
	}
	_, err := wrapKerberosTransport(&http.Client{}, a, "https://eas.example/")
	if err == nil {
		t.Fatal("want error for missing keytab")
	}
	if !strings.Contains(err.Error(), "keytab") {
		t.Errorf("err = %v; want one mentioning keytab", err)
	}
}

func TestWrapKerberosTransport_badCCachePath(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "krb5.conf")
	if err := os.WriteFile(confPath, []byte(minimalKrb5Conf), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &config.Account{
		Username: "u@EXAMPLE.COM",
		Kerberos: config.KerberosConfig{
			Krb5ConfPath: confPath,
			CCachePath:   filepath.Join(dir, "no-such-ccache"),
		},
	}
	_, err := wrapKerberosTransport(&http.Client{}, a, "https://eas.example/")
	if err == nil {
		t.Fatal("want error for missing ccache")
	}
	if !strings.Contains(err.Error(), "ccache") {
		t.Errorf("err = %v; want one mentioning ccache", err)
	}
}

// minimalKrb5Conf is just enough to satisfy gokrb5's Load — it
// requires libdefaults present.
const minimalKrb5Conf = `[libdefaults]
 default_realm = EXAMPLE.COM
[realms]
 EXAMPLE.COM = {
  kdc = kdc.example.com
 }
`
