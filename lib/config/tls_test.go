package config

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youmark/pkcs8"
)

// generateSelfSignedCert returns PEM-encoded cert + (optionally
// encrypted) PKCS#8 private key.
func generateSelfSignedCert(t *testing.T, encryptKeyPass string) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	if encryptKeyPass != "" {
		encDER, err := pkcs8.MarshalPrivateKey(priv, []byte(encryptKeyPass), nil)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: encDER})
	} else {
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	return certPEM, keyPEM
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTLS_loadEmpty(t *testing.T) {
	got, err := TLSConfig{}.Load(context.Background(), nil, false)
	if err != nil || got != nil {
		t.Errorf("empty + secure: %v %v", got, err)
	}
}

func TestTLS_loadInsecureOnly(t *testing.T) {
	got, err := TLSConfig{}.Load(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.InsecureSkipVerify {
		t.Error("InsecureSkipVerify not set")
	}
}

func TestTLS_loadPlainKeyPair(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateSelfSignedCert(t, "")
	cf := writeFile(t, dir, "cert.pem", certPEM)
	kf := writeFile(t, dir, "key.pem", keyPEM)

	got, err := TLSConfig{ClientCertFile: cf, ClientKeyFile: kf}.Load(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Certificates) != 1 {
		t.Errorf("certs = %d", len(got.Certificates))
	}
}

func TestTLS_loadEncryptedKey(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateSelfSignedCert(t, "supersecret")
	cf := writeFile(t, dir, "cert.pem", certPEM)
	kf := writeFile(t, dir, "key.pem", keyPEM)

	resolver := &SecretResolver{
		Run: func(_ context.Context, argv []string) (string, error) {
			return "supersecret\n", nil
		},
	}
	tls := TLSConfig{
		ClientCertFile: cf, ClientKeyFile: kf,
		ClientKeyPassphrase: SecretRef{Command: []string{"echo", "supersecret"}},
	}
	got, err := tls.Load(context.Background(), resolver, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Certificates) != 1 {
		t.Errorf("certs = %d", len(got.Certificates))
	}
}

func TestTLS_loadEncryptedKey_wrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateSelfSignedCert(t, "right")
	cf := writeFile(t, dir, "cert.pem", certPEM)
	kf := writeFile(t, dir, "key.pem", keyPEM)
	resolver := &SecretResolver{
		Run: func(_ context.Context, _ []string) (string, error) { return "wrong", nil },
	}
	tls := TLSConfig{
		ClientCertFile: cf, ClientKeyFile: kf,
		ClientKeyPassphrase: SecretRef{Command: []string{"x"}},
	}
	if _, err := tls.Load(context.Background(), resolver, false); err == nil {
		t.Error("want error for wrong passphrase")
	}
}

func TestTLS_loadEncryptedKey_missingPassphrase(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateSelfSignedCert(t, "x")
	cf := writeFile(t, dir, "cert.pem", certPEM)
	kf := writeFile(t, dir, "key.pem", keyPEM)
	tls := TLSConfig{ClientCertFile: cf, ClientKeyFile: kf}
	_, err := tls.Load(context.Background(), nil, false)
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("err = %v", err)
	}
}

func TestTLS_certWithoutKeyRejected(t *testing.T) {
	dir := t.TempDir()
	certPEM, _ := generateSelfSignedCert(t, "")
	cf := writeFile(t, dir, "cert.pem", certPEM)
	_, err := TLSConfig{ClientCertFile: cf}.Load(context.Background(), nil, false)
	if err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Errorf("err = %v", err)
	}
}

func TestTLS_loadServerCA(t *testing.T) {
	dir := t.TempDir()
	caPEM, _ := generateSelfSignedCert(t, "")
	caf := writeFile(t, dir, "ca.pem", caPEM)
	got, err := TLSConfig{ServerCAFile: caf}.Load(context.Background(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.RootCAs == nil {
		t.Error("RootCAs nil")
	}
}

func TestTLS_loadServerCA_invalid(t *testing.T) {
	dir := t.TempDir()
	caf := writeFile(t, dir, "bad.pem", []byte("not a cert"))
	_, err := TLSConfig{ServerCAFile: caf}.Load(context.Background(), nil, false)
	if err == nil {
		t.Error("want error")
	}
}

func TestEncryptedPEM(t *testing.T) {
	if encryptedPEM([]byte("not pem")) {
		t.Error("non-PEM should not be flagged encrypted")
	}
	plain := []byte("-----BEGIN PRIVATE KEY-----\nMC4CAQAw\n-----END PRIVATE KEY-----\n")
	if encryptedPEM(plain) {
		t.Error("plain PKCS#8 should not be flagged encrypted")
	}
	enc := []byte("-----BEGIN ENCRYPTED PRIVATE KEY-----\nMC4CAQAw\n-----END ENCRYPTED PRIVATE KEY-----\n")
	if !encryptedPEM(enc) {
		t.Error("ENCRYPTED PRIVATE KEY should be flagged encrypted")
	}
}

func TestEncryptedPEM_legacyDEKInfo(t *testing.T) {
	// The legacy openssl-traditional encrypted format is signaled by a
	// DEK-Info header on a regular RSA/EC PRIVATE KEY block.
	legacy := []byte("-----BEGIN RSA PRIVATE KEY-----\n" +
		"Proc-Type: 4,ENCRYPTED\n" +
		"DEK-Info: AES-128-CBC,0123456789ABCDEF\n\n" +
		"MC4CAQAw\n-----END RSA PRIVATE KEY-----\n")
	if !encryptedPEM(legacy) {
		t.Error("DEK-Info header should be flagged encrypted")
	}
}

func TestTLS_loadMinVersion(t *testing.T) {
	for _, c := range []struct {
		in     string
		ok     bool
		wantTL uint16
	}{
		{"", true, 0},
		{"1.2", true, 0x0303}, // tls.VersionTLS12
		{"1.3", true, 0x0304}, // tls.VersionTLS13
		{"1.1", false, 0},
		{"junk", false, 0},
	} {
		// MinVersion alone (no other TLS fields) shouldn't get short-
		// circuited by the IsZero check: pass allowInsecure to force
		// the non-nil-config path so we can observe the parsed value.
		got, err := TLSConfig{MinVersion: c.in}.Load(context.Background(), nil, true)
		if c.ok && err != nil {
			t.Errorf("MinVersion %q: unexpected err %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("MinVersion %q: want error", c.in)
		}
		if c.ok && c.wantTL != 0 && got.MinVersion != c.wantTL {
			t.Errorf("MinVersion %q: parsed = %#x, want %#x", c.in, got.MinVersion, c.wantTL)
		}
	}
}

func TestTLS_loadCipherSuites_validNames(t *testing.T) {
	// Long form (with TLS_ prefix) and short form (without) should
	// both resolve. Pick a pre-1.3 suite that's still in the
	// stdlib's CipherSuites() list.
	long := "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"
	short := "ECDHE_RSA_WITH_AES_128_GCM_SHA256"
	got, err := TLSConfig{CipherSuites: []string{long, short}}.Load(
		context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CipherSuites) != 2 || got.CipherSuites[0] != got.CipherSuites[1] {
		t.Errorf("cipher suites = %v (long+short should resolve to same id)",
			got.CipherSuites)
	}
}

func TestTLS_loadCipherSuites_unknownName(t *testing.T) {
	_, err := TLSConfig{CipherSuites: []string{"NOPE_NOT_A_SUITE"}}.Load(
		context.Background(), nil, true)
	if err == nil || !strings.Contains(err.Error(), "unknown cipher") {
		t.Errorf("err = %v, want one mentioning unknown cipher", err)
	}
}

func TestTLS_loadServerCAFile_unreadable(t *testing.T) {
	_, err := TLSConfig{ServerCAFile: "/no/such/ca.pem"}.Load(
		context.Background(), nil, false)
	if err == nil || !strings.Contains(err.Error(), "read server CA") {
		t.Errorf("err = %v", err)
	}
}

func TestTLS_loadClientCertFile_unreadable(t *testing.T) {
	dir := t.TempDir()
	_, keyPEM := generateSelfSignedCert(t, "")
	kf := writeFile(t, dir, "key.pem", keyPEM)
	_, err := TLSConfig{
		ClientCertFile: filepath.Join(dir, "missing-cert.pem"),
		ClientKeyFile:  kf,
	}.Load(context.Background(), nil, false)
	if err == nil || !strings.Contains(err.Error(), "read client cert") {
		t.Errorf("err = %v", err)
	}
}

func TestTLS_loadClientKeyFile_unreadable(t *testing.T) {
	dir := t.TempDir()
	certPEM, _ := generateSelfSignedCert(t, "")
	cf := writeFile(t, dir, "cert.pem", certPEM)
	_, err := TLSConfig{
		ClientCertFile: cf,
		ClientKeyFile:  filepath.Join(dir, "missing-key.pem"),
	}.Load(context.Background(), nil, false)
	if err == nil || !strings.Contains(err.Error(), "read client key") {
		t.Errorf("err = %v", err)
	}
}

func TestTLS_loadMismatchedKeypair(t *testing.T) {
	// Generate a cert from one keypair and a key from a different one.
	dir := t.TempDir()
	certPEM, _ := generateSelfSignedCert(t, "")
	_, otherKeyPEM := generateSelfSignedCert(t, "")
	cf := writeFile(t, dir, "cert.pem", certPEM)
	kf := writeFile(t, dir, "key.pem", otherKeyPEM)
	_, err := TLSConfig{ClientCertFile: cf, ClientKeyFile: kf}.Load(
		context.Background(), nil, false)
	if err == nil || !strings.Contains(err.Error(), "assemble keypair") {
		t.Errorf("err = %v", err)
	}
}

func TestDecryptPKCS8PEM_noPEMBlock(t *testing.T) {
	_, err := decryptPKCS8PEM([]byte("not pem"), "anything")
	if err == nil || !strings.Contains(err.Error(), "no PEM block") {
		t.Errorf("err = %v", err)
	}
}

func TestDecryptPKCS8PEM_wrongType(t *testing.T) {
	plain := []byte("-----BEGIN PRIVATE KEY-----\nMC4CAQAw\n-----END PRIVATE KEY-----\n")
	_, err := decryptPKCS8PEM(plain, "anything")
	if err == nil || !strings.Contains(err.Error(), "ENCRYPTED PRIVATE KEY") {
		t.Errorf("err = %v", err)
	}
}

func TestTLSConfig_IsZero(t *testing.T) {
	if !(TLSConfig{}).IsZero() {
		t.Error("default-constructed config should be zero")
	}
	cases := []TLSConfig{
		{ClientCertFile: "x"},
		{ClientKeyFile: "x"},
		{ServerCAFile: "x"},
	}
	for i, c := range cases {
		if c.IsZero() {
			t.Errorf("case %d: %+v should not be zero", i, c)
		}
	}
	// MinVersion + CipherSuites are part of the loadable surface but
	// not part of "is mTLS configured", so IsZero ignores them.
	if !(TLSConfig{MinVersion: "1.3"}).IsZero() {
		t.Error("MinVersion alone is not mTLS material; IsZero should be true")
	}
}
