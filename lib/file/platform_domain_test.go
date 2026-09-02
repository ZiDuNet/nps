package file

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPlatformDomain(id, wildcard, cert, key string) PlatformDomain {
	return PlatformDomain{ID: id, Wildcard: wildcard, CertFilePath: cert, KeyFilePath: key}
}

func testPlatformDomainWithCertificate(t *testing.T, id, wildcard string) PlatformDomain {
	t.Helper()
	certPath, keyPath := writePlatformDomainCertificate(t, wildcard)
	return testPlatformDomain(id, wildcard, certPath, keyPath)
}

func writePlatformDomainCertificate(t *testing.T, wildcard string) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.TrimPrefix(wildcard, "*.")
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: wildcard},
		DNSNames:              []string{wildcard},
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
	directory := t.TempDir()
	certPath := filepath.Join(directory, "platform.pem")
	keyPath := filepath.Join(directory, "platform.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	if suffix == "" {
		t.Fatal("test wildcard must have a suffix")
	}
	return certPath, keyPath
}

func testPlatformHost(id int, host, platformDomainID string) *Host {
	return &Host{
		Id:               id,
		Host:             host,
		PlatformDomainID: platformDomainID,
		Scheme:           "all",
		Location:         "/",
		Client:           &Client{Id: id, UserId: id},
		Target:           &Target{TargetStr: "127.0.0.1:8080"},
	}
}

func TestPlatformDomainValidationRejectsMalformedAndOverlappingWildcards(t *testing.T) {
	tests := []struct {
		name    string
		domains []PlatformDomain
	}{
		{
			name:    "missing wildcard",
			domains: []PlatformDomain{testPlatformDomain("one", "example.com", "/cert.pem", "/key.pem")},
		},
		{
			name:    "invalid suffix",
			domains: []PlatformDomain{testPlatformDomain("one", "*.bad_domain.com", "/cert.pem", "/key.pem")},
		},
		{
			name: "overlapping wildcards",
			domains: []PlatformDomain{
				testPlatformDomain("one", "*.example.com", "/cert-a.pem", "/key-a.pem"),
				testPlatformDomain("two", "*.api.example.com", "/cert-b.pem", "/key-b.pem"),
			},
		},
		{
			name: "duplicate ids",
			domains: []PlatformDomain{
				testPlatformDomain("one", "*.example.com", "/cert-a.pem", "/key-a.pem"),
				testPlatformDomain("one", "*.other.example.com", "/cert-b.pem", "/key-b.pem"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizePlatformDomains(test.domains); err == nil {
				t.Fatal("expected invalid platform domain configuration to be rejected")
			}
		})
	}

	normalized, err := normalizePlatformDomains([]PlatformDomain{testPlatformDomain("", "*.EXAMPLE.com.", "/cert.pem", "/key.pem")})
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 || normalized[0].ID == "" || normalized[0].Wildcard != "*.example.com" {
		t.Fatalf("platform domain was not normalized: %#v", normalized)
	}
}

func TestPlatformDomainMissingIDIsDeterministic(t *testing.T) {
	input := []PlatformDomain{testPlatformDomain("", "*.example.com", "/cert.pem", "/key.pem")}
	first, err := normalizePlatformDomains(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizePlatformDomains(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].ID == "" || first[0].ID != second[0].ID {
		t.Fatalf("missing platform ID must remain stable: first=%#v second=%#v", first, second)
	}
}

func TestPlatformDomainAllowsHTTPOnlyWithoutCertificate(t *testing.T) {
	domain := testPlatformDomain("http-only", "*.example.com", "", "")
	normalized, err := normalizePlatformDomains([]PlatformDomain{domain})
	if err != nil {
		t.Fatalf("empty certificate pair should be valid: %v", err)
	}
	if len(normalized) != 1 || normalized[0].CertFilePath != "" || normalized[0].KeyFilePath != "" {
		t.Fatalf("empty certificate pair was changed: %#v", normalized)
	}

	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{domain}}); err != nil {
		t.Fatalf("HTTP-only platform domain should save: %v", err)
	}
	if got := utils.GetUsablePlatformDomains(); len(got) != 1 {
		t.Fatalf("HTTP-only platform domain should remain selectable: %#v", got)
	}

	httpHost := testPlatformHost(1, "app.example.com", "http-only")
	httpHost.Scheme = "http"
	if err := utils.NewHost(httpHost); err != nil {
		t.Fatalf("HTTP-only platform host should save: %v", err)
	}
	httpsHost := testPlatformHost(2, "secure.example.com", "http-only")
	httpsHost.Scheme = "https"
	if err := utils.NewHost(httpsHost); err == nil {
		t.Fatal("HTTP-only platform host must reject HTTPS")
	}
}

func TestPlatformDomainCannotLoseCertificateWhileHTTPSHostUsesIt(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	configured := testPlatformDomainWithCertificate(t, "platform-1", "*.example.com")
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{configured}}); err != nil {
		t.Fatal(err)
	}
	host := testPlatformHost(1, "app.example.com", "platform-1")
	if err := utils.NewHost(host); err != nil {
		t.Fatal(err)
	}
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		testPlatformDomain("platform-1", "*.example.com", "", ""),
	}}); err == nil {
		t.Fatal("certificate removal must be rejected while an HTTPS host uses it")
	}

	host.Lock()
	host.Scheme = "http"
	host.Unlock()
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		testPlatformDomain("platform-1", "*.example.com", "", ""),
	}}); err != nil {
		t.Fatalf("certificate removal should be allowed after switching host to HTTP: %v", err)
	}
}

func TestPlatformDomainRejectsUnusableCertificateAtDataBoundary(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	invalid := testPlatformDomain("platform-1", "*.example.com", "/missing/platform.pem", "/missing/platform.key")
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{invalid}}); err == nil {
		t.Fatal("global settings must reject a platform domain with unreadable certificate files")
	}

	// A hand-edited global.json must not reopen the bypass through NewHost.
	db.setGlobal(&Glob{PlatformDomains: []PlatformDomain{invalid}})
	if got := utils.GetUsablePlatformDomains(); len(got) != 0 {
		t.Fatalf("unusable platform domains must not be offered to users: %#v", got)
	}
	if err := utils.NewHost(testPlatformHost(1, "app.example.com", "platform-1")); err == nil {
		t.Fatal("platform host creation must reject an unusable managed certificate")
	}
}

func TestPlatformHostResolutionAndAvailability(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	domain := testPlatformDomainWithCertificate(t, "platform-1", "*.example.com")
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		domain,
	}}); err != nil {
		t.Fatal(err)
	}

	resolved, err := utils.ResolvePlatformHost("platform-1", "Demo-01")
	if err != nil || resolved != "demo-01.example.com" {
		t.Fatalf("ResolvePlatformHost = %q, %v", resolved, err)
	}
	if _, err := utils.ResolvePlatformHost("platform-1", "nested.demo"); err == nil {
		t.Fatal("a multi-label prefix must be rejected")
	}
	if !utils.IsCustomHostInPlatformDomain("demo-01.example.com") || !utils.IsCustomHostInPlatformDomain("nested.demo.example.com") {
		t.Fatal("platform namespace detection did not cover the complete wildcard namespace")
	}

	first := testPlatformHost(1, resolved, "platform-1")
	first.CertFilePath, first.KeyFilePath = "/attacker.pem", "/attacker.key"
	if err := utils.NewHost(first); err != nil {
		t.Fatal(err)
	}
	if first.CertFilePath != domain.CertFilePath || first.KeyFilePath != domain.KeyFilePath {
		t.Fatalf("platform certificate paths were not enforced: %#v", first)
	}
	available, err := utils.IsPlatformHostAvailable("platform-1", "demo-01", 0)
	if err != nil || available {
		t.Fatalf("used platform hostname must be unavailable: available=%v err=%v", available, err)
	}

	second := testPlatformHost(2, resolved, "platform-1")
	if err := utils.NewHost(second); err == nil {
		t.Fatal("platform hostname must be globally unique across routes and clients")
	}
	custom := testPlatformHost(3, "custom.example.com", "")
	if err := utils.NewHost(custom); err == nil {
		t.Fatal("custom host inside a platform domain must be rejected")
	}
}

func TestPlatformDomainReferencesProtectWildcardAndSyncCertificatePaths(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	initialDomain := testPlatformDomainWithCertificate(t, "platform-1", "*.example.com")
	initial := &Glob{PlatformDomains: []PlatformDomain{
		initialDomain,
	}}
	if err := utils.SaveGlobal(initial); err != nil {
		t.Fatal(err)
	}
	host := testPlatformHost(1, "app.example.com", "platform-1")
	if err := utils.NewHost(host); err != nil {
		t.Fatal(err)
	}
	if got := utils.PlatformDomainReferenceCount("platform-1"); got != 1 || !utils.IsPlatformDomainInUse("platform-1") {
		t.Fatalf("unexpected platform domain reference state: %d", got)
	}
	if err := utils.SaveGlobal(&Glob{}); err == nil {
		t.Fatal("referenced platform domain must not be deleted")
	}
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		testPlatformDomain("platform-1", "*.renamed.example.com", "/old.pem", "/old.key"),
	}}); err == nil {
		t.Fatal("referenced platform wildcard must not be renamed")
	}
	updatedDomain := testPlatformDomainWithCertificate(t, "platform-1", "*.example.com")
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		updatedDomain,
	}}); err != nil {
		t.Fatal(err)
	}
	stored, err := utils.GetHostById(1)
	if err != nil {
		t.Fatal(err)
	}
	stored.RLock()
	cert, key := stored.CertFilePath, stored.KeyFilePath
	stored.RUnlock()
	if cert != updatedDomain.CertFilePath || key != updatedDomain.KeyFilePath {
		t.Fatalf("referenced host did not receive updated paths: cert=%q key=%q", cert, key)
	}
}

func TestPlatformDomainRejectsConflictingExistingWildcardRule(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	db.Hosts.Store(1, &Host{Id: 1, Host: "*.example.com", Scheme: "all", Location: "/", Client: &Client{Id: 1}, Target: &Target{TargetStr: "127.0.0.1:8080"}})

	err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		testPlatformDomain("platform-1", "*.example.com", "/platform.pem", "/platform.key"),
	}})
	if err == nil {
		t.Fatal("platform wildcard must not overlap an existing wildcard route")
	}
}

func TestPlatformDomainKeepsExistingExactLegacyHostsCompatible(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	db.Hosts.Store(1, testPlatformHost(1, "legacy.example.com", ""))
	domain := testPlatformDomainWithCertificate(t, "platform-1", "*.example.com")

	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		domain,
	}}); err != nil {
		t.Fatalf("an exact legacy host should retain compatibility: %v", err)
	}
}

func TestReconcilePlatformHostCertificatesAfterInterruptedPersistence(t *testing.T) {
	runPath := t.TempDir()
	source := NewJsonDb(runPath)
	sourceClient := &Client{Id: 1}
	source.Clients.Store(sourceClient.Id, sourceClient)
	source.StoreClientsToJsonFile()

	utils := &DbUtils{JsonDb: source}
	managedDomain := testPlatformDomainWithCertificate(t, "platform-1", "*.example.com")
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		managedDomain,
	}}); err != nil {
		t.Fatal(err)
	}
	staleHost := testPlatformHost(1, "app.example.com", "platform-1")
	staleHost.Client = sourceClient
	staleHost.CertFilePath, staleHost.KeyFilePath = "/old.pem", "/old.key"
	source.Hosts.Store(staleHost.Id, staleHost)
	source.StoreHostToJsonFile()

	loaded := NewJsonDb(runPath)
	loaded.LoadClientFromJsonFile()
	loaded.LoadGlobalFromJsonFile()
	loaded.LoadHostFromJsonFile()
	if !loaded.ReconcilePlatformHostCertificates() {
		t.Fatal("stale platform certificate paths were not reconciled")
	}

	host, err := (&DbUtils{JsonDb: loaded}).GetHostById(1)
	if err != nil {
		t.Fatal(err)
	}
	host.RLock()
	certPath, keyPath := host.CertFilePath, host.KeyFilePath
	host.RUnlock()
	if certPath != managedDomain.CertFilePath || keyPath != managedDomain.KeyFilePath {
		t.Fatalf("reconciled paths = %q, %q; want new platform paths", certPath, keyPath)
	}
}

func TestPlatformDomainKeepsExistingCustomHostsCompatible(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	utils := &DbUtils{JsonDb: db}
	legacy := testPlatformHost(1, "legacy.example.com", "")
	db.Hosts.Store(legacy.Id, legacy)
	domain := testPlatformDomainWithCertificate(t, "platform-1", "*.example.com")
	if err := utils.SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		domain,
	}}); err != nil {
		t.Fatal(err)
	}

	updated := testPlatformHost(1, "legacy.example.com", "")
	updated.Remark = "still custom"
	if err := utils.UpdateHost(updated); err != nil {
		t.Fatalf("unchanged legacy custom host should remain editable: %v", err)
	}
	renamed := testPlatformHost(1, "renamed.example.com", "")
	if err := utils.UpdateHost(renamed); err == nil {
		t.Fatal("renamed custom host inside a platform domain must use platform mode")
	}
}

func TestPlatformDomainJSONCompatibilityAndPersistence(t *testing.T) {
	var legacy Host
	if err := json.Unmarshal([]byte(`{"Id":9,"Host":"legacy.example.com","Scheme":"https"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.PlatformDomainID != "" {
		t.Fatalf("legacy host unexpectedly gained platform-domain ID %q", legacy.PlatformDomainID)
	}

	runPath := t.TempDir()
	db := NewJsonDb(runPath)
	configuredDomain := testPlatformDomainWithCertificate(t, "stable-id", "*.example.com")
	if err := (&DbUtils{JsonDb: db}).SaveGlobal(&Glob{PlatformDomains: []PlatformDomain{
		configuredDomain,
	}}); err != nil {
		t.Fatal(err)
	}
	loaded := NewJsonDb(runPath)
	loaded.LoadGlobalFromJsonFile()
	global := loaded.getGlobal()
	if global == nil || len(global.PlatformDomains) != 1 {
		t.Fatalf("platform domain was not restored: %#v", global)
	}
	persistedDomain := global.PlatformDomains[0]
	if persistedDomain.ID != "stable-id" || persistedDomain.Wildcard != "*.example.com" || persistedDomain.CertFilePath != configuredDomain.CertFilePath || persistedDomain.KeyFilePath != configuredDomain.KeyFilePath {
		t.Fatalf("restored platform domain changed: %#v", persistedDomain)
	}
}
