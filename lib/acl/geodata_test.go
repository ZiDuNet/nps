package acl

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeGeoTestFile(t *testing.T, path, content string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write Geo data: %v", err)
	}
	if !modified.IsZero() {
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatalf("set Geo data mtime: %v", err)
		}
	}
}

func resetGeoTestData(t *testing.T) {
	t.Helper()
	SetGeoData("", "")
	t.Cleanup(func() { SetGeoData("", "") })
}

func TestReloadGeoDataRetainsLastValidDataOnRemovalOrCorruption(t *testing.T) {
	resetGeoTestData(t)
	path := filepath.Join(t.TempDir(), "geoip.dat")
	baseTime := time.Now().Add(-time.Hour)
	writeGeoTestFile(t, path, "cn,198.51.100.0/24\n", baseTime)
	SetGeoData(path, "")

	if !geoIPRuleMatches("cn", mustTestAddr(t, "198.51.100.8")) {
		t.Fatal("initial GeoIP data was not loaded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove GeoIP file: %v", err)
	}
	ReloadGeoData()
	if !geoIPRuleMatches("cn", mustTestAddr(t, "198.51.100.8")) {
		t.Fatal("removing the source file discarded the last valid GeoIP data")
	}

	writeGeoTestFile(t, path, "this is not a GeoIP entry\n", baseTime.Add(time.Hour))
	ReloadGeoData()
	if !geoIPRuleMatches("cn", mustTestAddr(t, "198.51.100.8")) {
		t.Fatal("a malformed replacement discarded the last valid GeoIP data")
	}
}

func TestReloadGeoDataUpdatesEachDatasetIndependently(t *testing.T) {
	resetGeoTestData(t)
	dir := t.TempDir()
	ipPath := filepath.Join(dir, "geoip.dat")
	sitePath := filepath.Join(dir, "geosite.dat")
	baseTime := time.Now().Add(-time.Hour)
	writeGeoTestFile(t, ipPath, "cn,198.51.100.0/24\n", baseTime)
	writeGeoTestFile(t, sitePath, "test,example.com\n", baseTime)
	SetGeoData(ipPath, sitePath)

	writeGeoTestFile(t, ipPath, "not valid\n", baseTime.Add(time.Hour))
	writeGeoTestFile(t, sitePath, "test,new-example.com\n", baseTime.Add(2*time.Hour))
	ReloadGeoData()

	if !geoIPRuleMatches("cn", mustTestAddr(t, "198.51.100.8")) {
		t.Fatal("invalid GeoIP update discarded its previous valid dataset")
	}
	if geoSiteMatches("test", "new-example.com") == false || geoSiteMatches("test", "example.com") {
		t.Fatal("valid Geosite update was not applied independently of the invalid GeoIP update")
	}
}

func TestReloadGeoDataRecoversWhenConfiguredFileAppears(t *testing.T) {
	resetGeoTestData(t)
	path := filepath.Join(t.TempDir(), "geoip.dat")
	SetGeoData(path, "")
	if GeoDataConfigured() {
		t.Fatal("missing file unexpectedly configured Geo data")
	}

	writeGeoTestFile(t, path, "cn,203.0.113.0/24\n", time.Now().Add(time.Hour))
	ReloadGeoData()
	if !geoIPRuleMatches("cn", mustTestAddr(t, "203.0.113.8")) {
		t.Fatal("GeoIP data was not loaded after the configured file appeared")
	}
}

func TestSetGeoDataPathChangeDoesNotRetainOldDataset(t *testing.T) {
	resetGeoTestData(t)
	first := filepath.Join(t.TempDir(), "first.dat")
	second := filepath.Join(t.TempDir(), "second.dat")
	writeGeoTestFile(t, first, "cn,198.51.100.0/24\n", time.Time{})
	SetGeoData(first, "")
	if !GeoDataConfigured() {
		t.Fatal("initial GeoIP data was not configured")
	}

	SetGeoData(second, "")
	if GeoDataConfigured() {
		t.Fatal("changing to a missing data path retained entries from the previous path")
	}
}

func mustTestAddr(t *testing.T, value string) (addr netip.Addr) {
	t.Helper()
	addr, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatalf("parse test address %q: %v", value, err)
	}
	return addr
}
