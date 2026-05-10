package config

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"github.com/youmark/pkcs8"
)

// LoadTLSConfig builds a *tls.Config from the account's TLS settings.
// Returns nil and a nil error when the account has no TLS overrides
// (the caller should use http.DefaultTransport in that case).
//
// resolver is used to fetch the optional ClientKeyPassphrase. Pass
// DefaultResolver() in production code; tests can stub it.
func (t TLSConfig) Load(ctx context.Context, resolver *SecretResolver, allowInsecure bool) (*tls.Config, error) {
	if t.IsZero() && !allowInsecure && t.MinVersion == "" && len(t.CipherSuites) == 0 {
		return nil, nil
	}
	out := &tls.Config{InsecureSkipVerify: allowInsecure}
	switch t.MinVersion {
	case "":
	case "1.2":
		out.MinVersion = tls.VersionTLS12
	case "1.3":
		out.MinVersion = tls.VersionTLS13
	default:
		return nil, fmt.Errorf("tls: min_version must be 1.2 or 1.3 (got %q)", t.MinVersion)
	}
	if len(t.CipherSuites) > 0 {
		ids, err := cipherSuiteIDs(t.CipherSuites)
		if err != nil {
			return nil, err
		}
		out.CipherSuites = ids
	}

	if t.ServerCAFile != "" {
		caPEM, err := os.ReadFile(t.ServerCAFile)
		if err != nil {
			return nil, fmt.Errorf("tls: read server CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("tls: server CA file %q contains no usable certs", t.ServerCAFile)
		}
		out.RootCAs = pool
	}

	if t.ClientCertFile == "" && t.ClientKeyFile == "" {
		return out, nil
	}
	if t.ClientCertFile == "" || t.ClientKeyFile == "" {
		return nil, errors.New("tls: client_cert_file and client_key_file must be set together")
	}

	certPEM, err := os.ReadFile(t.ClientCertFile)
	if err != nil {
		return nil, fmt.Errorf("tls: read client cert: %w", err)
	}
	keyPEM, err := os.ReadFile(t.ClientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: read client key: %w", err)
	}

	if encryptedPEM(keyPEM) {
		passphrase, err := t.ClientKeyPassphrase.resolveStrict(ctx, resolver)
		if err != nil {
			return nil, fmt.Errorf("tls: client key is encrypted but passphrase: %w", err)
		}
		decrypted, err := decryptPKCS8PEM(keyPEM, passphrase)
		if err != nil {
			return nil, fmt.Errorf("tls: decrypt client key: %w", err)
		}
		keyPEM = decrypted
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tls: assemble keypair: %w", err)
	}
	out.Certificates = []tls.Certificate{cert}
	return out, nil
}

// encryptedPEM reports whether keyPEM contains an encrypted private key
// block (PKCS#8 with EncryptedPrivateKeyInfo, or the legacy PEM
// "Proc-Type: 4,ENCRYPTED" header).
func encryptedPEM(keyPEM []byte) bool {
	for {
		block, rest := pem.Decode(keyPEM)
		if block == nil {
			return false
		}
		if block.Type == "ENCRYPTED PRIVATE KEY" {
			return true
		}
		if _, ok := block.Headers["DEK-Info"]; ok {
			return true
		}
		keyPEM = rest
	}
}

// decryptPKCS8PEM decrypts an encrypted PKCS#8 PEM key. Returns a fresh
// PEM blob containing the unencrypted key in PKCS#8 form so the caller
// can pass it to tls.X509KeyPair.
//
// Note: the legacy PEM-encrypted format (Proc-Type: 4,ENCRYPTED with
// DEK-Info) is not supported; users with such keys should re-encrypt
// to PKCS#8 (openssl pkcs8 -topk8 ...). The legacy format is insecure.
func decryptPKCS8PEM(keyPEM []byte, passphrase string) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	if block.Type != "ENCRYPTED PRIVATE KEY" {
		return nil, fmt.Errorf("unexpected PEM type %q (want ENCRYPTED PRIVATE KEY); legacy DEK-Info encryption is not supported, re-encrypt with `openssl pkcs8 -topk8`", block.Type)
	}
	key, err := pkcs8.ParsePKCS8PrivateKey(block.Bytes, []byte(passphrase))
	if err != nil {
		return nil, err
	}
	switch key.(type) {
	case *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey:
	default:
		return nil, fmt.Errorf("unsupported key type %T", key)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// cipherSuiteIDs converts the human-readable cipher names from
// TLSConfig.CipherSuites to the numeric IDs Go's crypto/tls expects.
// Names should match the constants in crypto/tls without the "TLS_"
// prefix (e.g. "ECDHE_RSA_WITH_AES_128_GCM_SHA256").
func cipherSuiteIDs(names []string) ([]uint16, error) {
	avail := tls.CipherSuites()
	byName := make(map[string]uint16, len(avail))
	for _, c := range avail {
		byName[c.Name] = c.ID
		// Also accept the de-prefixed form.
		short := c.Name
		if len(short) > 4 && short[:4] == "TLS_" {
			short = short[4:]
		}
		byName[short] = c.ID
	}
	out := make([]uint16, 0, len(names))
	for _, n := range names {
		id, ok := byName[n]
		if !ok {
			id, ok = byName["TLS_"+n]
		}
		if !ok {
			return nil, fmt.Errorf("tls: unknown cipher suite %q", n)
		}
		out = append(out, id)
	}
	return out, nil
}

// resolveStrict returns the secret value or an error if the SecretRef
// is unset (treats empty as a hard configuration error rather than the
// no-source-set case used elsewhere).
func (s SecretRef) resolveStrict(ctx context.Context, r *SecretResolver) (string, error) {
	if s.KeyringService == "" && len(s.Command) == 0 {
		return "", errors.New("no source configured")
	}
	if r == nil {
		r = DefaultResolver()
	}
	return r.Resolve(ctx, &Account{Secret: s})
}
