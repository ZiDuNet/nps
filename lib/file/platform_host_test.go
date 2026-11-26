package file

import (
	"strings"
	"testing"
)

func TestPickPlatformDomainRequiresCertificateForHTTPS(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Global = &Glob{PlatformDomains: []PlatformDomain{
		testPlatformDomain("http-only", "*.plain.example.com", "", ""),
	}}
	utils := &DbUtils{JsonDb: db}

	if _, err := utils.PickPlatformDomain("", "all"); err == nil {
		t.Fatal("expected HTTPS platform host to require a certificate")
	}
	domain, err := utils.PickPlatformDomain("", "http")
	if err != nil {
		t.Fatalf("HTTP platform host should be allowed: %v", err)
	}
	if domain.ID != "http-only" {
		t.Fatalf("picked %q, want http-only", domain.ID)
	}
}

func TestPickPlatformDomainAcceptsWildcardReference(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Global = &Glob{PlatformDomains: []PlatformDomain{
		testPlatformDomainWithCertificate(t, "pd1", "*.example.com"),
	}}
	utils := &DbUtils{JsonDb: db}

	domain, err := utils.PickPlatformDomain("*.EXAMPLE.com", "https")
	if err != nil {
		t.Fatal(err)
	}
	if domain.ID != "pd1" {
		t.Fatalf("picked %q, want pd1", domain.ID)
	}
	if _, err := utils.PickPlatformDomain("*.missing.example.com", "http"); err == nil {
		t.Fatal("expected an unknown wildcard to be rejected")
	}
}

func TestAllocatePlatformHostGeneratesRandomPrefixAndAvoidsConflicts(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Global = &Glob{PlatformDomains: []PlatformDomain{
		testPlatformDomainWithCertificate(t, "pd1", "*.example.com"),
	}}
	utils := &DbUtils{JsonDb: db}

	host, domainID, err := utils.AllocatePlatformHost("", "", "/", "http", 0)
	if err != nil {
		t.Fatal(err)
	}
	if domainID != "pd1" {
		t.Fatalf("domain id = %q, want pd1", domainID)
	}
	prefix := strings.TrimSuffix(host, ".example.com")
	if len(prefix) != 8 {
		t.Fatalf("generated prefix = %q, want 8 characters", prefix)
	}

	db.Hosts.Store(1, &Host{Id: 1, Host: host, Scheme: "http", Location: "/"})
	if _, _, err := utils.AllocatePlatformHost("pd1", prefix, "/", "http", 0); err == nil {
		t.Fatal("expected an explicit prefix conflict to be rejected")
	}

	for attempt := 0; attempt < 5; attempt++ {
		other, _, err := utils.AllocatePlatformHost("pd1", "", "/", "http", 0)
		if err != nil {
			t.Fatal(err)
		}
		if other == host {
			t.Fatalf("random allocation reused the taken host %q", host)
		}
	}
}

func TestAllocatePlatformHostRejectsMissingPlatformDomains(t *testing.T) {
	db := NewJsonDb(t.TempDir())
	db.Global = &Glob{}
	utils := &DbUtils{JsonDb: db}

	if _, _, err := utils.AllocatePlatformHost("", "", "/", "http", 0); err == nil {
		t.Fatal("expected a missing platform domain to be rejected")
	}
}
