package client

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestTLSClientConfigPinsCertificate(t *testing.T) {
	certificate := testTLSCertificate(t)
	digest := sha256.Sum256(certificate.Certificate[0])
	fingerprint := hex.EncodeToString(digest[:])
	config, err := tlsClientConfig("127.0.0.1:8025", TLSOptions{Fingerprint: fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if !config.InsecureSkipVerify || config.VerifyConnection == nil {
		t.Fatal("pinned TLS configuration must use explicit certificate verification")
	}

	serverConn, clientConn := net.Pipe()
	server := tls.Server(serverConn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	client := tls.Client(clientConn, config)
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Handshake() }()
	if err := client.Handshake(); err != nil {
		t.Fatalf("pinned client handshake failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("pinned server handshake failed: %v", err)
	}
	_ = client.Close()
	_ = server.Close()
}

func TestTLSClientConfigRejectsInvalidFingerprint(t *testing.T) {
	if _, err := tlsClientConfig("127.0.0.1:8025", TLSOptions{Fingerprint: "not-a-fingerprint"}); err == nil {
		t.Fatal("invalid TLS fingerprint must be rejected before dialing")
	}
}

func TestTLSClientConfigOnlySkipsVerificationWhenExplicit(t *testing.T) {
	verified, err := tlsClientConfig("127.0.0.1:8025", TLSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if verified.InsecureSkipVerify {
		t.Fatal("TLS verification must stay enabled by default")
	}
	legacy, err := tlsClientConfig("127.0.0.1:8025", TLSOptions{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.InsecureSkipVerify {
		t.Fatal("explicit insecure TLS option was not applied")
	}
}

func TestTLSClientConfigReadsCAFile(t *testing.T) {
	if _, err := tlsClientConfig("127.0.0.1:8025", TLSOptions{CAFile: t.TempDir() + "/missing.pem"}); err == nil {
		t.Fatal("missing TLS CA file must be reported")
	}
}

func testTLSCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		DNSNames:     []string{"127.0.0.1"},
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
