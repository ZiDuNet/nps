package crypt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLoginPayloadSupportsOAEPAndPKCS1(t *testing.T) {
	publicPEM, err := GetRSAPublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(publicPEM))
	if block == nil {
		t.Fatal("public key PEM did not decode")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key type = %T, want RSA", parsed)
	}
	payload := LoginPayload{Nonce: "nonce", Timestamp: time.Now().UnixMilli(), Password: "secret"}
	plain, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		seal func() ([]byte, error)
	}{
		{name: "oaep", seal: func() ([]byte, error) {
			return rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, plain, nil)
		}},
		{name: "pkcs1", seal: func() ([]byte, error) {
			//nolint:staticcheck // browser JSEncrypt compatibility path.
			return rsa.EncryptPKCS1v15(rand.Reader, publicKey, plain)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ciphertext, err := test.seal()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := ParseLoginPayload(base64.StdEncoding.EncodeToString(ciphertext))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Nonce != payload.Nonce || decoded.Password != payload.Password || decoded.Timestamp != payload.Timestamp {
				t.Fatalf("decoded payload = %#v, want %#v", decoded, payload)
			}
		})
	}
}

func TestCertificateSelfSignedDetection(t *testing.T) {
	now := time.Now()
	selfKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	selfTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "bridge.example.com"},
		DNSNames:              []string{"bridge.example.com"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	selfDER, err := x509.CreateCertificate(rand.Reader, selfTemplate, selfTemplate, &selfKey.PublicKey, selfKey)
	if err != nil {
		t.Fatal(err)
	}
	if !IsCertificateSelfSigned(tls.Certificate{Certificate: [][]byte{selfDER}}) {
		t.Fatal("self-signed certificate was not detected")
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "bridge.example.com"},
		DNSNames:              []string{"bridge.example.com"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCertificate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if IsCertificateSelfSigned(tls.Certificate{Certificate: [][]byte{leafDER, caDER}}) {
		t.Fatal("CA-issued leaf certificate was treated as self-signed")
	}
}

func TestBridgeCertificateHotReloadKeepsLastGoodPair(t *testing.T) {
	t.Cleanup(InitTls)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "bridge.pem")
	keyPath := filepath.Join(dir, "bridge.key")
	writePair := func(serial int64) {
		t.Helper()
		certPEM, keyPEM := testCertificatePair(t, serial)
		if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePair(101)
	if err := InitTlsFromFiles(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	firstFingerprint := GetCertFingerprint()
	if firstFingerprint == "" {
		t.Fatal("first certificate fingerprint is empty")
	}

	writePair(102)
	lastReloadAttemptV.Store(0)
	secondFingerprint := GetCertFingerprint()
	if secondFingerprint == "" || secondFingerprint == firstFingerprint {
		t.Fatalf("certificate did not reload: first=%s second=%s", firstFingerprint, secondFingerprint)
	}

	if err := os.WriteFile(certPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	lastReloadAttemptV.Store(0)
	if got := GetCertFingerprint(); got != secondFingerprint {
		t.Fatalf("invalid replacement changed active certificate: got=%s want=%s", got, secondFingerprint)
	}
	if BridgeCertificateLastReloadError() == "" {
		t.Fatal("expected invalid replacement to be reported")
	}
}

func testCertificatePair(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "bridge.example.com"},
		DNSNames:              []string{"bridge.example.com"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
