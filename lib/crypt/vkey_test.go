package crypt

import "testing"

func TestGetVkeyLength(t *testing.T) {
	const wantLen = 16
	seen := make(map[string]struct{})
	for i := 0; i < 32; i++ {
		vkey := GetVkey()
		if len(vkey) != wantLen {
			t.Fatalf("GetVkey() returned %d characters, want %d", len(vkey), wantLen)
		}
		for _, r := range vkey {
			if (r < '0' || r > '9') && (r < 'a' || r > 'z') {
				t.Fatalf("GetVkey() returned unexpected character %q", r)
			}
		}
		if _, dup := seen[vkey]; dup {
			t.Fatalf("GetVkey() returned duplicate key %q", vkey)
		}
		seen[vkey] = struct{}{}
	}
}
