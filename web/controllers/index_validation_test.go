package controllers

import "testing"

func TestParseSourceListPriorityIsStrict(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		current   int
		submitted bool
		want      int
		wantErr   bool
	}{
		{name: "blacklist first", value: "0", current: 1, submitted: true, want: 0},
		{name: "whitelist first with surrounding space", value: " 1 ", current: 0, submitted: true, want: 1},
		{name: "omitted field preserves existing value", current: 1, want: 1},
		{name: "empty value rejected", value: "", current: 1, submitted: true, want: 1, wantErr: true},
		{name: "text rejected", value: "blacklist", current: 1, submitted: true, want: 1, wantErr: true},
		{name: "out of range rejected", value: "2", current: 1, submitted: true, want: 1, wantErr: true},
		{name: "negative rejected", value: "-1", current: 0, submitted: true, want: 0, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSourceListPriority(test.value, test.current, test.submitted)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("priority = %d, want %d", got, test.want)
			}
		})
	}
}

func TestValidateHostDomainRequestRequiresExactlyOneMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		host      string
		platform  string
		prefix    string
		wantMode  string
		wantError bool
	}{
		{name: "custom domain", mode: "custom", host: "app.example.com", wantMode: "custom"},
		{name: "legacy custom request", host: "app.example.com", wantMode: "custom"},
		{name: "platform domain", mode: "platform", platform: "platform-1", prefix: "app", wantMode: "platform"},
		{name: "legacy platform request", platform: "platform-1", prefix: "app", wantMode: "platform"},
		{name: "custom and platform mixed", mode: "platform", host: "app.example.com", platform: "platform-1", prefix: "app", wantError: true},
		{name: "custom mode with platform fields", mode: "custom", host: "app.example.com", platform: "platform-1", prefix: "app", wantError: true},
		{name: "platform mode with custom domain", mode: "platform", host: "app.example.com", platform: "platform-1", prefix: "app", wantError: true},
		{name: "incomplete platform mode", mode: "platform", platform: "platform-1", wantError: true},
		{name: "unknown mode", mode: "other", host: "app.example.com", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateHostDomainRequest(test.mode, test.host, test.platform, test.prefix)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %t", err, test.wantError)
			}
			if !test.wantError && got != test.wantMode {
				t.Fatalf("mode = %q, want %q", got, test.wantMode)
			}
		})
	}
}

func TestValidateBridgeTargetPermissionRestrictsSystemRoutesToAdmins(t *testing.T) {
	for _, test := range []struct {
		name      string
		isAdmin   bool
		target    string
		wantError bool
	}{
		{name: "admin may use bridge route", isAdmin: true, target: "bridge://web"},
		{name: "admin multi-target bridge route", isAdmin: true, target: "127.0.0.1:8080\nbridge://ipreg"},
		{name: "ordinary user cannot use bridge route", target: "bridge://web", wantError: true},
		{name: "ordinary user cannot hide bridge route in target list", target: "127.0.0.1:8080\n bridge://tcp", wantError: true},
		{name: "ordinary user may use regular nested tunnel", target: "tunnel://42"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateBridgeTargetPermission(test.isAdmin, test.target)
			if (err != nil) != test.wantError {
				t.Fatalf("validateBridgeTargetPermission() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}
