package acl

import "testing"

func TestCompileSourceReportsInvalidRulesButKeepsValidPolicy(t *testing.T) {
	tests := []struct {
		name          string
		black         []string
		white         []string
		address       string
		wantAllowed   bool
		wantRuleError bool
	}{
		{name: "invalid allowlist stays empty and denies", white: []string{"not-an-ip"}, address: "198.51.100.8:443", wantRuleError: true},
		{name: "invalid denylist is ignored", black: []string{"not-an-ip"}, address: "198.51.100.8:443", wantAllowed: true, wantRuleError: true},
		{name: "unsupported geodata stays empty and denies", white: []string{"geoip:cn"}, address: "198.51.100.8:443", wantRuleError: true},
		{name: "valid rule beside invalid allowlist entry", white: []string{"198.51.100.0/24", "not-an-ip"}, address: "198.51.100.8:443", wantAllowed: true, wantRuleError: true},
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
