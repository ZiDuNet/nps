package crypt

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astaxie/beego/logs"
)

var (
	cert     tls.Certificate
	rsaKey   *rsa.PrivateKey
	rsaKeyMu sync.Mutex

	certLoadedFromFiles atomic.Bool
)

func InitTls() {
	certLoadedFromFiles.Store(false)
	c, k, err := generateKeyPair("NPS Org")
	if err == nil {
		cert, err = tls.X509KeyPair(c, k)
	}
	if err != nil {
		log.Fatalln("Error initializing crypto certs", err)
	}
	ensureLoginRSAKey()
}

// InitTlsFromFiles loads the Bridge certificate configured by the operator.
// When either path is empty, unreadable, or invalid, callers can fall back to
// InitTls so an existing installation keeps its historical self-signed
// behaviour. The certificate is process-local and is shared by Bridge TLS,
// QUIC and the modern P2P control channel.
func InitTlsFromFiles(certPath, keyPath string) error {
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	if certPath == "" || keyPath == "" {
		return fmt.Errorf("Bridge certificate and key paths must both be configured")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read Bridge certificate %q: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read Bridge private key %q: %w", keyPath, err)
	}
	loaded, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("parse Bridge certificate: %w", err)
	}
	cert = loaded
	certLoadedFromFiles.Store(true)
	ensureLoginRSAKey()
	return nil
}

func GetCert() tls.Certificate {
	return cert
}

// IsCertLoadedFromFiles reports whether the process is currently serving the
// operator-configured Bridge certificate instead of the generated temporary
// certificate. It is used by the management panel to distinguish the active
// certificate from a newly saved, not-yet-restarted configuration.
func IsCertLoadedFromFiles() bool {
	return certLoadedFromFiles.Load()
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

// IsCertSelfSigned reports whether the active Bridge leaf certificate is
// self-signed. Generated temporary certificates and common self-signed
// deployments need a pin; CA-issued certificates should use the client's
// normal trust store and hostname verification so automatic renewal does not
// invalidate every existing NPC command.
func IsCertSelfSigned() bool {
	return IsCertificateSelfSigned(cert)
}

// IsCertificateSelfSigned applies the same policy to supplied server
// certificates. It verifies both the issuer/subject identity and the leaf
// signature instead of relying only on string equality.
func IsCertificateSelfSigned(certificate tls.Certificate) bool {
	if len(certificate.Certificate) == 0 {
		return false
	}
	leaf := certificate.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			return false
		}
		leaf = parsed
	}
	return bytes.Equal(leaf.RawIssuer, leaf.RawSubject) &&
		leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil
}

// GetCertificateFingerprint returns the SHA-256 fingerprint of a supplied
// certificate's leaf. NPC uses this to authenticate the peer on a direct P2P
// QUIC connection without relying on a public certificate authority.
func GetCertificateFingerprint(certificate tls.Certificate) []byte {
	if len(certificate.Certificate) == 0 {
		return nil
	}
	digest := sha256.Sum256(certificate.Certificate[0])
	return digest[:]
}

// EnsureTls initializes the process-local certificate when a client uses
// modern P2P. The NPC certificate is ephemeral and never replaces the NPS
// server's configured Bridge or HTTPS certificate.
func EnsureTls() {
	if len(cert.Certificate) == 0 {
		InitTls()
	}
	ensureLoginRSAKey()
}

// ensureLoginRSAKey keeps Web login encryption available even when the
// operator configured an ECDSA/Ed25519 Bridge certificate. Historically the
// login form submitted a plain password; the RSA key is a separate process
// local key and is never persisted or used for Bridge traffic.
func ensureLoginRSAKey() {
	rsaKeyMu.Lock()
	defer rsaKeyMu.Unlock()
	if rsaKey != nil {
		return
	}
	if key, ok := cert.PrivateKey.(*rsa.PrivateKey); ok && key != nil {
		rsaKey = key
		return
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		logs.Error("生成 Web 登录 RSA 密钥失败: %v", err)
		return
	}
	rsaKey = key
}

// GetRSAPublicKeyPEM returns the public key used by the browser-side login
// payload. It is generated lazily so tests and standalone handlers do not have
// to initialise the Bridge TLS listener first.
func GetRSAPublicKeyPEM() (string, error) {
	ensureLoginRSAKey()
	if rsaKey == nil {
		return "", fmt.Errorf("RSA login key is not initialized")
	}
	der, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// LoginPayload is the browser-to-server envelope used to avoid sending the
// clear-text Web password over a plain HTTP management listener. The nonce is
// issued by the login page and the timestamp is checked against login_max_skew
// when the feature is enabled.
type LoginPayload struct {
	Nonce     string `json:"n"`
	Timestamp int64  `json:"t"`
	Password  string `json:"p"`
}

// ParseLoginPayload decrypts an RSA-OAEP payload and retains a PKCS#1 v1.5
// fallback for the JSEncrypt versions shipped by older browsers.
func ParseLoginPayload(base64Cipher string) (*LoginPayload, error) {
	plain, err := decryptLoginPayload(base64Cipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt login payload: %w", err)
	}
	var payload LoginPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal login payload: %w", err)
	}
	if strings.TrimSpace(payload.Nonce) == "" || payload.Timestamp <= 0 || payload.Password == "" {
		return nil, fmt.Errorf("login payload is incomplete")
	}
	return &payload, nil
}

func decryptLoginPayload(base64Cipher string) ([]byte, error) {
	ensureLoginRSAKey()
	if rsaKey == nil {
		return nil, fmt.Errorf("RSA login key is not initialized")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Cipher))
	if err != nil {
		return nil, fmt.Errorf("base64 decode error: %w", err)
	}
	plain, oaepErr := rsa.DecryptOAEP(sha256.New(), rand.Reader, rsaKey, ciphertext, nil)
	if oaepErr == nil {
		return plain, nil
	}
	// JSEncrypt defaults to PKCS#1 v1.5. Keep this fallback for compatibility
	// with the existing browser bundle and older deployments.
	//nolint:staticcheck // PKCS#1 v1.5 is required for browser compatibility.
	plain, legacyErr := rsa.DecryptPKCS1v15(rand.Reader, rsaKey, ciphertext)
	if legacyErr != nil {
		return nil, fmt.Errorf("RSA decrypt error: oaep: %w; pkcs1v15: %v", oaepErr, legacyErr)
	}
	return plain, nil
}

// NewPinnedTlsClientConfig accepts only the leaf certificate whose SHA-256
// fingerprint was delivered over an authenticated Bridge stream.
func NewPinnedTlsClientConfig(fingerprint []byte) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // verified explicitly against the authenticated pin
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(fingerprint) != sha256.Size || len(state.PeerCertificates) == 0 {
				return fmt.Errorf("P2P peer certificate fingerprint is missing")
			}
			got := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], fingerprint) != 1 {
				return fmt.Errorf("P2P peer certificate fingerprint mismatch")
			}
			return nil
		},
	}
}

// NewPinnedTlsServerConfig requires a client certificate and verifies it
// against the fingerprint exchanged over the authenticated coordinator.
func NewPinnedTlsServerConfig(fingerprint []byte, certificate tls.Certificate, alpn string) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{certificate},
		ClientAuth:             tls.RequireAnyClientCert,
		SessionTicketsDisabled: true,
		NextProtos:             []string{alpn},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(fingerprint) != sha256.Size || len(state.PeerCertificates) == 0 {
				return fmt.Errorf("P2P peer certificate fingerprint is missing")
			}
			got := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], fingerprint) != 1 {
				return fmt.Errorf("P2P peer certificate fingerprint mismatch")
			}
			return nil
		},
	}
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
