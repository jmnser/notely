package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeKeyPair writes a self-signed certificate with the given serial and
// its key, and sets both files' modification time to mod.
func writeKeyPair(t *testing.T, dir string, serial int64, mod time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	for f, b := range map[string]*pem.Block{
		certFile: {Type: "CERTIFICATE", Bytes: der},
		keyFile:  {Type: "PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(f, pem.EncodeToMemory(b), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile
}

func serial(t *testing.T, k *keyPair) int64 {
	t.Helper()
	cert, err := k.get(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

func TestKeyPairReloads(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().Add(-time.Minute)
	certFile, keyFile := writeKeyPair(t, dir, 1, start)
	k, err := newKeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := serial(t, k); got != 1 {
		t.Fatalf("serial = %d, want 1", got)
	}

	writeKeyPair(t, dir, 2, start.Add(time.Second))
	if got := serial(t, k); got != 2 {
		t.Fatalf("after renewal: serial = %d, want 2", got)
	}

	// A broken file keeps the last good certificate.
	if err := os.WriteFile(certFile, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := serial(t, k); got != 2 {
		t.Fatalf("broken file: serial = %d, want 2", got)
	}
}

func TestKeyPairMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := newKeyPair(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")); err == nil {
		t.Error("missing files: no error")
	}
}

func TestRunNeedsBothTLSFiles(t *testing.T) {
	if err := Run("127.0.0.1:0", TLS{CertFile: "tls.crt"}, nil); err == nil {
		t.Error("certificate without key: no error")
	}
}
