package controllers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/file"
)

func TestRequestedPlatformDomainsPreservesOmittedLegacyField(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/global/save", strings.NewReader("serverUrl=nps.example.com"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	domains, submitted, err := requestedPlatformDomains(request)
	if err != nil {
		t.Fatal(err)
	}
	if submitted || domains != nil {
		t.Fatalf("omitted platform_domains = %#v, submitted=%v; want no update", domains, submitted)
	}

	request = httptest.NewRequest(http.MethodPost, "/global/save", strings.NewReader("platform_domains=%5B%5D"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	domains, submitted, err = requestedPlatformDomains(request)
	if err != nil {
		t.Fatal(err)
	}
	if !submitted || len(domains) != 0 {
		t.Fatalf("explicit empty array = %#v, submitted=%v; want an intentional clear", domains, submitted)
	}

	request = httptest.NewRequest(http.MethodPost, "/global/save", strings.NewReader("platform_domains="))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, submitted, err = requestedPlatformDomains(request); err == nil || !submitted {
		t.Fatalf("blank submitted field must be rejected, submitted=%v err=%v", submitted, err)
	}
}

func TestInspectPlatformDomainChecksWildcardCoverage(t *testing.T) {
	certPath, keyPath := writeControllerTestCertificate(t, []string{"*.example.com"})
	matched := inspectPlatformDomain(file.PlatformDomain{
		Wildcard:     "*.example.com",
		CertFilePath: certPath,
		KeyFilePath:  keyPath,
	})
	if !matched.Readable || matched.Status != "证书有效" {
		t.Fatalf("matching certificate status = %#v", matched)
	}

	mismatchedCert, mismatchedKey := writeControllerTestCertificate(t, []string{"*.other.example.com"})
	mismatched := inspectPlatformDomain(file.PlatformDomain{
		Wildcard:     "*.example.com",
		CertFilePath: mismatchedCert,
		KeyFilePath:  mismatchedKey,
	})
	if !mismatched.Readable || mismatched.Status != "证书不覆盖该平台泛域名" {
		t.Fatalf("mismatched certificate status = %#v", mismatched)
	}
}

func writeControllerTestCertificate(t *testing.T, dnsNames []string) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		DNSNames:              dnsNames,
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(t.TempDir(), "platform.pem")
	keyPath := filepath.Join(t.TempDir(), "platform.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
