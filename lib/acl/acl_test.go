package acl

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompileSourceFailsClosedWhenAnyRuleIsInvalid(t *testing.T) {
	tests := []struct {
		name          string
		black         []string
		white         []string
		address       string
		wantAllowed   bool
		wantRuleError bool
	}{
		{name: "invalid allowlist stays empty and denies", white: []string{"not-an-ip"}, address: "198.51.100.8:443", wantRuleError: true},
		{name: "invalid denylist also denies", black: []string{"not-an-ip"}, address: "198.51.100.8:443", wantRuleError: true},
		{name: "unsupported geodata stays empty and denies", white: []string{"geoip:cn"}, address: "198.51.100.8:443", wantRuleError: true},
		{name: "valid rule beside invalid allowlist entry fails closed", white: []string{"198.51.100.0/24", "not-an-ip"}, address: "198.51.100.8:443", wantRuleError: true},
		{name: "valid allowlist still limits unrelated addresses", white: []string{"198.51.100.0/24", "not-an-ip"}, address: "192.0.2.8:443", wantRuleError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, err := CompileSource(ScopeUser, DenyFirst, test.black, test.white)
			if (err != nil) != test.wantRuleError {
				t.Fatalf("CompileSource() error = %v, wantRuleError %t", err, test.wantRuleError)
			}
			if source == nil {
				t.Fatal("CompileSource() source = nil, want policy for configured rules")
			}
			if got := source.AllowsString(test.address); got != test.wantAllowed {
				t.Fatalf("AllowsString(%q) = %t, want %t", test.address, got, test.wantAllowed)
			}
		})
	}
}

func TestCompileSourceIgnoresCommentsAndEmptyLinesForRuleLimit(t *testing.T) {
	previous := maxRules
	SetMaxRules(1)
	t.Cleanup(func() { SetMaxRules(previous) })

	if source, err := CompileSource(ScopeUser, DenyFirst, []string{"# comment", "198.51.100.0/24", "", "; another comment"}, nil); err != nil || source == nil || !source.AllowsString("203.0.113.8:443") {
		t.Fatalf("comments and empty lines should not consume the limit: source=%v err=%v", source, err)
	}

	source, err := CompileSource(ScopeUser, DenyFirst, []string{"198.51.100.0/24", "203.0.113.0/24"}, nil)
	if err == nil || source == nil || source.AllowsString("192.0.2.8:443") {
		t.Fatalf("exceeding the user rule limit should return a fail-closed policy: source=%v err=%v", source, err)
	}
	if !strings.Contains(err.Error(), "maximum is 1") {
		t.Fatalf("rule limit error = %v, want maximum detail", err)
	}
	if _, err := CompileSource(ScopeUser, DenyFirst, []string{"198.51.100.0/24"}, []string{"203.0.113.0/24"}); err != nil {
		t.Fatalf("blacklist and allowlist have independent per-list limits: %v", err)
	}

	// Global policies are administrator-owned and are not subject to the user
	// quota, even when the same runtime limit is configured.
	global, err := CompileSource(ScopeGlobal, DenyFirst, []string{"198.51.100.0/24", "203.0.113.0/24"}, nil)
	if err != nil || global == nil || global.AllowsString("192.0.2.8:443") == false {
		t.Fatalf("global policy should bypass user rule limit: source=%v err=%v", global, err)
	}
}

func TestCompileSourceSupportsConfiguredGeoIPAndRejectsGeosite(t *testing.T) {
	resetGeoTestData(t)
	path := filepath.Join(t.TempDir(), "geoip.dat")
	writeGeoTestFile(t, path, "cn,198.51.100.0/24\n", time.Time{})
	SetGeoData(path, "")

	source, err := CompileSource(ScopeUser, DenyFirst, nil, []string{"geoip:cn"})
	if err != nil || source == nil {
		t.Fatalf("configured GeoIP rule should compile: source=%v err=%v", source, err)
	}
	if !source.AllowsString("198.51.100.8:443") || source.AllowsString("203.0.113.8:443") {
		t.Fatal("GeoIP allowlist did not match the configured category")
	}

	if source, err := CompileSource(ScopeUser, DenyFirst, nil, []string{"geosite:private"}); err == nil || source == nil || source.AllowsString("198.51.100.8:443") {
		t.Fatalf("Geosite source rules should fail closed: source=%v err=%v", source, err)
	}
}

func TestCompileSourceSupportsIPv4PrefixWildcards(t *testing.T) {
	tests := []struct {
		rule    string
		want    string
		wantErr bool
	}{
		{rule: "192.168.*", want: "192.168.0.0/16"},
		{rule: "10.*", want: "10.0.0.0/8"},
		{rule: "10.1.2.*", want: "10.1.2.0/24"},
		{rule: "192.168.*.*", want: "192.168.0.0/16"},
		{rule: "192.168.256.*", wantErr: true},
		{rule: "2001:db8::*", wantErr: true},
	}
	for _, test := range tests {
		source, err := CompileSource(ScopeUser, DenyFirst, nil, []string{test.rule})
		if (err != nil) != test.wantErr {
			t.Fatalf("CompileSource(%q) error = %v, wantErr %t", test.rule, err, test.wantErr)
		}
		if test.wantErr {
			continue
		}
		if source == nil || len(source.white) != 1 || source.white[0].String() != test.want {
			t.Fatalf("CompileSource(%q) = %#v, want single prefix %q", test.rule, source, test.want)
		}
	}
}

func TestCompileSourceSupportsNegatedGeoIP(t *testing.T) {
	resetGeoTestData(t)
	path := filepath.Join(t.TempDir(), "geoip.dat")
	writeGeoTestFile(t, path, "cn,198.51.100.0/24\n", time.Time{})
	SetGeoData(path, "")

	source, err := CompileSource(ScopeUser, DenyFirst, []string{"geoip:!cn"}, nil)
	if err != nil || source == nil {
		t.Fatalf("CompileSource() error = %v, source = %v", err, source)
	}
	if !source.AllowsString("198.51.100.8:443") {
		t.Fatal("geoip:!cn denylist must allow addresses inside cn")
	}
	if source.AllowsString("203.0.113.8:443") {
		t.Fatal("geoip:!cn denylist must deny addresses outside cn")
	}
	if _, err := CompileSource(ScopeUser, DenyFirst, nil, []string{"geoip:!missing"}); err == nil {
		t.Fatal("negated geoip with missing data must fail closed")
	}
}

func TestCompileSourceEvaluatesValidListsAndPriority(t *testing.T) {
	black := []string{"198.51.100.0/24"}
	white := []string{"198.51.100.8", "203.0.113.0/24"}

	denyFirst, err := CompileSource(ScopeUser, DenyFirst, black, white)
	if err != nil {
		t.Fatal(err)
	}
	if denyFirst.AllowsString("198.51.100.8:443") || !denyFirst.AllowsString("203.0.113.8:443") || denyFirst.AllowsString("192.0.2.8:443") {
		t.Fatal("deny-first policy did not apply the expected allow/deny rules")
	}

	allowFirst, err := CompileSource(ScopeUser, AllowFirst, black, white)
	if err != nil {
		t.Fatal(err)
	}
	if !allowFirst.AllowsString("198.51.100.8:443") || !allowFirst.AllowsString("192.0.2.8:443") || allowFirst.AllowsString("198.51.100.9:443") {
		t.Fatal("allow-first policy did not apply the expected allow/deny rules")
	}
}
