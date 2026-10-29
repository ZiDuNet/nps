package common

import (
	"net"
	"path/filepath"
	"testing"
)

func TestResolveConfigPath(t *testing.T) {
	originalRunPath := ConfPath
	ConfPath = filepath.Join(t.TempDir(), "nps-root")
	t.Cleanup(func() { ConfPath = originalRunPath })

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "docker conf remapped", input: "/conf/webssl/fullchain.pem", want: filepath.Join(ConfPath, "conf/webssl/fullchain.pem")},
		{name: "absolute outside docker conf", input: "/etc/certs/fullchain.pem", want: "/etc/certs/fullchain.pem"},
		{name: "relative", input: "conf/webssl/fullchain.pem", want: filepath.Join(ConfPath, "conf/webssl/fullchain.pem")},
		{name: "inline PEM", input: "-----BEGIN CERTIFICATE-----\nABC\n-----END CERTIFICATE-----", want: "-----BEGIN CERTIFICATE-----\nABC\n-----END CERTIFICATE-----"},
		{name: "trimmed", input: "  conf/a.pem  ", want: filepath.Join(ConfPath, "conf/a.pem")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveConfigPath(tc.input); got != tc.want {
				t.Fatalf("ResolveConfigPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestInDoesNotMutateAddressList(t *testing.T) {
	addresses := []string{"10.0.0.3", "10.0.0.1", "10.0.0.2"}
	original := append([]string(nil), addresses...)
	if !in("10.0.0.1", addresses) {
		t.Fatal("expected address to be found")
	}
	for index := range addresses {
		if addresses[index] != original[index] {
			t.Fatalf("address list was reordered: got %#v, want %#v", addresses, original)
		}
	}
}

func TestIPAccessListsMatchExactAddresses(t *testing.T) {
	if !IsBlackIp("203.0.113.7:443", "test", []string{"203.0.113.7"}) {
		t.Fatal("exact IPv4 blacklist entry must match")
	}
	if !IsBlackIp("[2001:db8::7]:443", "test", []string{"2001:db8::7"}) {
		t.Fatal("exact IPv6 blacklist entry must match")
	}
	if IsBlackIp("203.0.113.7:443", "test", []string{"203.0.113.0/24"}) {
		t.Fatal("CIDR blacklist entry must not be treated as an exact IP match")
	}
}

func TestGetIPByAddrPreservesBareIPv6(t *testing.T) {
	for _, test := range []struct {
		address string
		want    string
	}{
		{address: "2001:db8::7", want: "2001:db8::7"},
		{address: "[2001:db8::7]:443", want: "2001:db8::7"},
		{address: "203.0.113.7:443", want: "203.0.113.7"},
	} {
		if got := GetIpByAddr(test.address); got != test.want {
			t.Fatalf("GetIpByAddr(%q) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestGetPortByAddrSupportsIPv6(t *testing.T) {
	for _, test := range []struct {
		address string
		want    int
	}{
		{address: "127.0.0.1:8024", want: 8024},
		{address: "[2001:db8::7]:443", want: 443},
		{address: "2001:db8::7", want: 0},
	} {
		if got := GetPortByAddr(test.address); got != test.want {
			t.Fatalf("GetPortByAddr(%q) = %d, want %d", test.address, got, test.want)
		}
	}
}

func TestIsPortRejectsOutOfRangeValues(t *testing.T) {
	if !IsPort("65535") {
		t.Fatal("65535 should be a valid port")
	}
	if IsPort("65536") {
		t.Fatal("65536 must be rejected")
	}
}

func TestGetServerIpByClientIpPrefersReachablePublicListener(t *testing.T) {
	clientIP := net.ParseIP("198.51.100.20")
	localIP := net.ParseIP("203.0.113.10")
	if got := GetServerIpByClientIp(clientIP, localIP); got != localIP.String() {
		t.Fatalf("server address = %q, want %q", got, localIP.String())
	}
}
