package conn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func testQUICCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "nps-quic-test"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func TestDialQuicAndServeQuicRoundTrip(t *testing.T) {
	const alpn = "nps-test"
	certificate := testQUICCertificate(t)
	listener, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{alpn},
	}, &quic.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverResult := make(chan error, 1)
	requestSeen := make(chan struct{}, 1)
	go func() {
		serverResult <- ServeQuic(ctx, listener, time.Second, func(stream net.Conn) {
			defer stream.Close()
			payload := make([]byte, len("ping"))
			if _, err := io.ReadFull(stream, payload); err != nil {
				return
			}
			if string(payload) != "ping" {
				return
			}
			requestSeen <- struct{}{}
			_, _ = stream.Write([]byte("pong"))
		})
	}()

	clientTLS := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{alpn}}
	client, err := DialQuic(context.Background(), listener.Addr().String(), clientTLS, &quic.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		client.Close()
		t.Fatal(err)
	}
	response := make([]byte, len("pong"))
	if _, err := io.ReadFull(client, response); err != nil {
		client.Close()
		t.Fatal(err)
	}
	if string(response) != "pong" {
		t.Fatalf("response = %q, want pong", response)
	}
	_ = client.Close()

	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("server did not receive the QUIC stream payload")
	}

	cancel()
	_ = listener.Close()
	select {
	case err := <-serverResult:
		if err != nil && err != context.Canceled && !isNetClosedError(err) {
			t.Fatalf("ServeQuic returned unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeQuic did not stop after listener shutdown")
	}
}

func TestDialQuicRejectsMismatchedALPN(t *testing.T) {
	certificate := testQUICCertificate(t)
	listener, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{"nps-server"},
	}, &quic.Config{MaxIdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = DialQuic(ctx, listener.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"nps-client"},
	}, &quic.Config{MaxIdleTimeout: time.Second})
	if err == nil {
		t.Fatal("ALPN mismatch must reject the QUIC session")
	}
}

func isNetClosedError(err error) bool {
	if err == nil {
		return false
	}
	return err == net.ErrClosed || err.Error() == "use of closed network connection"
}
