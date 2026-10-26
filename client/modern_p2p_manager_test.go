package client

import (
	"testing"

	"ehang.io/nps/lib/common"
)

func TestNormalizeP2PTargetType(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "", want: common.CONN_TCP},
		{raw: "tcp", want: common.CONN_TCP},
		{raw: "UDP", want: common.CONN_UDP},
		{raw: "all", want: common.CONN_ALL},
		{raw: "ALL", want: common.CONN_ALL},
		{raw: "unknown", want: common.CONN_TCP},
	}
	for _, tc := range cases {
		if got := normalizeP2PTargetType(tc.raw); got != tc.want {
			t.Fatalf("normalizeP2PTargetType(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
