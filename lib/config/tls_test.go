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
