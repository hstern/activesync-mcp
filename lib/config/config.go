// Package config loads and validates the activesync-mcp TOML configuration.
//
// Secrets are never stored in the config file. Each account references its
// password either by an OS-keyring entry or by a fetch command (see
// secrets.go). The schema deliberately has no plaintext password field.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration document.
type Config struct {
	StateDir string    `toml:"state_dir"`
	LogLevel string    `toml:"log_level"`
	Accounts []Account `toml:"account"`
}

// Account describes a single ActiveSync account.
type Account struct {
	Name          string            `toml:"name"`
	ServerURL     string            `toml:"server_url"`
	Username      string            `toml:"username"`
	DeviceType    string            `toml:"device_type"`
	DeviceID      string            `toml:"device_id"`
	ASVersion     string            `toml:"as_version"`
	UserAgent     string            `toml:"user_agent"`
	AllowInsecure bool              `toml:"allow_insecure"`
	Secret        SecretRef         `toml:"secret"`
	DefaultAccess string            `toml:"default_access"`
	Access        map[string]string `toml:"access"`
	// Push enables a background long-poll Ping that surfaces server-side
	// changes as MCP resource-update notifications. Off by default.
	Push bool `toml:"push"`
	// TLS configures client-cert (mTLS) authentication. Optional; when
	// non-empty, the client cert is presented during the TLS handshake
	// in addition to whatever Authorization scheme Secret selects.
	TLS TLSConfig `toml:"tls"`
	// ProxyURL is an optional per-account HTTP/HTTPS/SOCKS5 proxy.
	// When empty, the process-level HTTP_PROXY env vars apply.
	ProxyURL string `toml:"proxy_url"`
	// Kerberos configures Kerberos / SPNEGO ("Negotiate") auth, used
	// when secret.auth_scheme = "negotiate".
	Kerberos KerberosConfig `toml:"kerberos"`
}

// KerberosConfig describes how to talk to a KDC for "Negotiate" auth.
type KerberosConfig struct {
	// Realm is the Kerberos realm (uppercase, e.g. "EXAMPLE.COM").
	Realm string `toml:"realm"`
	// SPN is the service principal name to request a ticket for, e.g.
	// "HTTP/mail.example.com". When empty, derived from the server URL.
	SPN string `toml:"spn"`
	// KeytabFile, when set, loads service credentials from this keytab.
	// Mutually exclusive with CCachePath; one of the two must be set.
	KeytabFile string `toml:"keytab_file"`
	// CCachePath, when set, uses an existing Kerberos credential cache
	// (the file kinit produces; defaults to /tmp/krb5cc_$UID).
	CCachePath string `toml:"ccache_path"`
	// Krb5ConfPath is the krb5.conf used to bootstrap. Defaults to
	// /etc/krb5.conf.
	Krb5ConfPath string `toml:"krb5_conf_path"`
}

// TLSConfig describes optional client-side TLS material (mTLS).
type TLSConfig struct {
	// ClientCertFile is the PEM-encoded client certificate (and any
	// intermediate CAs in the same file).
	ClientCertFile string `toml:"client_cert_file"`
	// ClientKeyFile is the PEM-encoded private key matching ClientCertFile.
	// May be PKCS#1, PKCS#8, or SEC1; encrypted PKCS#8 is supported via
	// ClientKeyPassphrase.
	ClientKeyFile string `toml:"client_key_file"`
	// ClientKeyPassphrase is a SecretRef that resolves to the passphrase
	// for an encrypted ClientKeyFile. Plaintext passphrases in the
	// config file are rejected by the same schema-omission rule used
	// for account passwords.
	ClientKeyPassphrase SecretRef `toml:"client_key_passphrase"`
	// ServerCAFile is a PEM file containing trusted root CA(s) for
	// servers using a private CA. Optional.
	ServerCAFile string `toml:"server_ca_file"`
	// MinVersion is the minimum TLS version: one of "1.2" or "1.3".
	// Default is Go's stdlib default (1.2 today).
	MinVersion string `toml:"min_version"`
	// CipherSuites overrides the cipher suite list. Names match Go's
	// crypto/tls constants without the "TLS_" prefix
	// (e.g. "ECDHE_RSA_WITH_AES_128_GCM_SHA256"). Empty means use Go's
	// secure default. TLS 1.3 cipher suites are not configurable in
	// Go and are ignored here regardless of input.
	CipherSuites []string `toml:"cipher_suites"`
}

// IsZero reports whether the TLSConfig is empty (no mTLS or custom CA).
func (t TLSConfig) IsZero() bool {
	return t.ClientCertFile == "" && t.ClientKeyFile == "" && t.ServerCAFile == ""
}

// SecretRef points at where to fetch the account password. Exactly one
// source must be populated.
type SecretRef struct {
	KeyringService string   `toml:"keyring_service"`
	KeyringAccount string   `toml:"keyring_account"`
	Command        []string `toml:"command"`
	// AuthScheme selects how the resolved value is sent on the wire:
	// "basic" (default) sends Username + value as HTTP Basic; "bearer"
	// sends value as a Bearer token; "ntlm" feeds value to the NTLM
	// transport as the password.
	AuthScheme string `toml:"auth_scheme"`
}

// Access values.
const (
	AccessRO = "ro"
	AccessRW = "rw"
)

// validClasses is the set of EAS classes that may appear in [account.access].
var validClasses = map[string]struct{}{
	"email":    {},
	"calendar": {},
	"contacts": {},
	"tasks":    {},
	"notes":    {},
}

// DefaultPath is the standard config location.
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "activesync-mcp", "config.toml")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "activesync-mcp", "config.toml")
	}
	return "config.toml"
}

// Load reads, decodes, validates, and applies defaults to the config at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	return Decode(f)
}

// Decode parses a config from an io.Reader.
func Decode(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if _, err := toml.Decode(string(data), &c); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() error {
	if c.StateDir == "" {
		c.StateDir = defaultStateDir()
	} else {
		c.StateDir = expandHome(c.StateDir)
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.DefaultAccess == "" {
			a.DefaultAccess = AccessRO
		}
		if a.DeviceType == "" {
			a.DeviceType = "MCP"
		}
		if a.ASVersion == "" {
			a.ASVersion = "14.1"
		}
		if a.UserAgent == "" {
			a.UserAgent = "activesync-mcp/0.1"
		}
	}
	return nil
}

func (c *Config) validate() error {
	if len(c.Accounts) == 0 {
		return errors.New("config: no [[account]] entries")
	}
	seen := make(map[string]struct{}, len(c.Accounts))
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.Name == "" {
			return fmt.Errorf("account[%d]: name is required", i)
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("account[%d] %q: duplicate name", i, a.Name)
		}
		seen[a.Name] = struct{}{}
		if a.ServerURL == "" {
			return fmt.Errorf("account %q: server_url is required", a.Name)
		}
		if a.Username == "" {
			return fmt.Errorf("account %q: username is required", a.Name)
		}
		if err := a.Secret.validate(); err != nil {
			return fmt.Errorf("account %q: %w", a.Name, err)
		}
		if a.DefaultAccess != AccessRO && a.DefaultAccess != AccessRW {
			return fmt.Errorf("account %q: default_access must be %q or %q, got %q",
				a.Name, AccessRO, AccessRW, a.DefaultAccess)
		}
		for k, v := range a.Access {
			if _, ok := validClasses[k]; !ok {
				return fmt.Errorf("account %q: access[%q]: unknown class (valid: email, calendar, contacts, tasks, notes)",
					a.Name, k)
			}
			if v != AccessRO && v != AccessRW {
				return fmt.Errorf("account %q: access[%q] must be %q or %q, got %q",
					a.Name, k, AccessRO, AccessRW, v)
			}
		}
	}
	return nil
}

func (s *SecretRef) validate() error {
	hasKeyring := s.KeyringService != "" || s.KeyringAccount != ""
	hasCommand := len(s.Command) > 0
	// Kerberos/Negotiate auth doesn't need a configured secret —
	// credentials come from a keytab or the user's ccache. Skip the
	// "must set source" check in that case.
	if s.AuthScheme != "negotiate" {
		switch {
		case !hasKeyring && !hasCommand:
			return errors.New("secret: must set either keyring_service+keyring_account or command")
		case hasKeyring && hasCommand:
			return errors.New("secret: keyring and command are mutually exclusive")
		case hasKeyring && (s.KeyringService == "" || s.KeyringAccount == ""):
			return errors.New("secret: keyring requires both keyring_service and keyring_account")
		case hasCommand && strings.TrimSpace(s.Command[0]) == "":
			return errors.New("secret: command[0] is empty")
		}
	}
	switch s.AuthScheme {
	case "", "basic", "bearer", "ntlm", "negotiate":
	default:
		return fmt.Errorf("secret: auth_scheme must be one of basic|bearer|ntlm|negotiate (got %q)", s.AuthScheme)
	}
	return nil
}

// FindAccount returns the account with the given name, or nil.
func (c *Config) FindAccount(name string) *Account {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			return &c.Accounts[i]
		}
	}
	return nil
}

func defaultStateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "activesync-mcp")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "activesync-mcp")
	}
	return "state"
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(h, strings.TrimPrefix(p, "~"))
}
