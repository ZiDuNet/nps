package common

import "testing"

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
	if IsAuthIp("203.0.113.7:443", "test", []string{"203.0.113.7"}) {
		t.Fatal("exact IPv4 whitelist entry must allow the request")
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
