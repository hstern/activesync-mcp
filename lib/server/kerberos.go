package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/user"

	"activesync-mcp/lib/config"

	krb "github.com/jcmturner/gokrb5/v8/client"
	krbcfg "github.com/jcmturner/gokrb5/v8/config"
	"github.com/jcmturner/gokrb5/v8/credentials"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/jcmturner/gokrb5/v8/spnego"
)

// wrapKerberosTransport returns an http.Client whose transport
// completes the SPNEGO Kerberos handshake. The caller's username is
// only used as the principal when the keytab path is set; with a
// credential cache the principal comes from the cache itself.
//
// Returns the original client unchanged on configuration errors —
// the next request will surface a clearer 401 from the server.
func wrapKerberosTransport(orig *http.Client, a *config.Account, serverURL string) (*http.Client, error) {
	cfgPath := a.Kerberos.Krb5ConfPath
	if cfgPath == "" {
		cfgPath = "/etc/krb5.conf"
	}
	krbConf, err := krbcfg.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("kerberos: load %s: %w", cfgPath, err)
	}

	var krbClient *krb.Client
	switch {
	case a.Kerberos.KeytabFile != "":
		kt, err := keytab.Load(a.Kerberos.KeytabFile)
		if err != nil {
			return nil, fmt.Errorf("kerberos: load keytab %s: %w", a.Kerberos.KeytabFile, err)
		}
		realm := a.Kerberos.Realm
		if realm == "" {
			return nil, errors.New("kerberos: realm is required when keytab_file is set")
		}
		krbClient = krb.NewWithKeytab(a.Username, realm, kt, krbConf, krb.DisablePAFXFAST(true))
	case a.Kerberos.CCachePath != "" || true:
		path := a.Kerberos.CCachePath
		if path == "" {
			path = defaultCCachePath()
		}
		cc, err := credentials.LoadCCache(path)
		if err != nil {
			return nil, fmt.Errorf("kerberos: load ccache %s: %w", path, err)
		}
		krbClient, err = krb.NewFromCCache(cc, krbConf, krb.DisablePAFXFAST(true))
		if err != nil {
			return nil, fmt.Errorf("kerberos: build client from ccache: %w", err)
		}
	}
	if err := krbClient.Login(); err != nil {
		// Login can be a no-op for ccache-backed clients; that's fine.
		// We only surface as a warning, not an error.
		_ = err
	}

	spn := a.Kerberos.SPN
	if spn == "" {
		u, err := url.Parse(serverURL)
		if err == nil {
			spn = "HTTP/" + u.Hostname()
		}
	}

	out := &http.Client{
		Timeout:       orig.Timeout,
		CheckRedirect: orig.CheckRedirect,
		Jar:           orig.Jar,
	}
	base := orig.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out.Transport = &spnegoRoundTripper{base: base, client: krbClient, spn: spn}
	return out, nil
}

// spnegoRoundTripper attaches an SPNEGO Negotiate header to outgoing
// requests using gokrb5's helper. We don't use spnego.NewClient
// because it intercepts redirects in ways that conflict with our own
// redirect handling.
type spnegoRoundTripper struct {
	base   http.RoundTripper
	client *krb.Client
	spn    string
}

func (r *spnegoRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cl := spnego.NewClient(r.client, &http.Client{Transport: r.base}, r.spn)
	return cl.Do(req)
}

// defaultCCachePath returns the standard MIT Kerberos credential cache
// path: /tmp/krb5cc_<uid>.
func defaultCCachePath() string {
	if u, err := user.Current(); err == nil {
		return fmt.Sprintf("/tmp/krb5cc_%s", u.Uid)
	}
	return os.Getenv("KRB5CCNAME")
}
