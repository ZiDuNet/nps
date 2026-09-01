package crypt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"github.com/astaxie/beego/logs"
)

var (
	cert tls.Certificate
)

func InitTls() {
	c, k, err := generateKeyPair("NPS Org")
	if err == nil {
		cert, err = tls.X509KeyPair(c, k)
	}
	if err != nil {
		log.Fatalln("Error initializing crypto certs", err)
	}
}

func GetCert() tls.Certificate {
	return cert
}

// GetCertFingerprint returns the SHA-256 fingerprint of the server
// certificate's DER bytes. Clients can pin this value when using the
// self-signed TLS Bridge certificate.
func GetCertFingerprint() string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	digest := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(digest[:])
}

func NewTlsServerConn(conn net.Conn) net.Conn {
	var err error
	if err != nil {
		logs.Error(err)
		os.Exit(0)
		return nil
	}
	config := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	return tls.Server(conn, config)
}

func NewTlsClientConn(conn net.Conn) net.Conn {
	return NewTlsClientConnWithFingerprint(conn)
}

// NewTlsClientConnWithFingerprint wraps an already authenticated bridge data
// stream in TLS. Links carry the server certificate fingerprint over that
// bridge and are pinned here. Missing or malformed pins fail closed, which
// prevents an old peer from silently downgrading the inner TLS layer.
func NewTlsClientConnWithFingerprint(conn net.Conn, fingerprints ...string) net.Conn {
	conf := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(fingerprints) > 0 && strings.TrimSpace(fingerprints[0]) != "" {
		if expected, ok := decodeFingerprint(fingerprints[0]); ok {
			conf.InsecureSkipVerify = true
			conf.VerifyConnection = func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return fmt.Errorf("TLS peer did not provide a certificate")
				}
				got := sha256.Sum256(state.PeerCertificates[0].Raw)
				if subtle.ConstantTimeCompare(got[:], expected) != 1 {
					return fmt.Errorf("TLS server certificate fingerprint mismatch")
				}
				return nil
			}
		} else {
			// A malformed pin must not silently turn into an unauthenticated
			// connection. The handshake will fail closed without a valid pin.
			conf.InsecureSkipVerify = false
			conf.VerifyConnection = func(tls.ConnectionState) error {
				return fmt.Errorf("invalid TLS server certificate fingerprint")
			}
		}
	} else {
		conf.VerifyConnection = func(tls.ConnectionState) error {
			return fmt.Errorf("TLS server certificate fingerprint is required")
		}
	}
	return tls.Client(conn, conf)
}

func decodeFingerprint(value string) ([]byte, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	for _, prefix := range []string{"sha256:", "sha256/"} {
		value = strings.TrimPrefix(value, prefix)
	}
	value = strings.NewReplacer(":", "", "-", "", " ", "").Replace(value)
	if len(value) != sha256.Size*2 {
		return nil, false
	}
	decoded, err := hex.DecodeString(value)
	return decoded, err == nil
}

func generateKeyPair(CommonName string) (rawCert, rawKey []byte, err error) {
	// Create private key and self-signed certificate
	// Adapted from https://golang.org/src/crypto/tls/generate_cert.go

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return
	}
	validFor := time.Hour * 24 * 365 * 10 // ten years
	notBefore := time.Now()
	notAfter := notBefore.Add(validFor)
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"My Company Name LTD."},
			CommonName:   CommonName,
			Country:      []string{"US"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return
	}

	rawCert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	rawKey = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return
}
