package version

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{name: "equal with tag prefix", left: "1.1.3", right: "v1.1.3", want: 0},
		{name: "patch comparison", left: "1.1.9", right: "1.1.10", want: -1},
		{name: "minor comparison", left: "1.10.0", right: "1.2.99", want: 1},
		{name: "major comparison", left: "1.1.3", right: "2.0.0", want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Compare(tt.left, tt.right)
			if err != nil {
				t.Fatalf("Compare(%q, %q) returned error: %v", tt.left, tt.right, err)
			}
			if got != tt.want {
				t.Fatalf("Compare(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
			}
		})
	}
}

func TestCompareRejectsNonReleaseVersions(t *testing.T) {
	for _, invalid := range []string{"master", "v1.1", "v1.1.3-rc.1", "v1.1.x", "v01.1.3", ""} {
		if _, err := Compare(VERSION, invalid); err == nil {
			t.Fatalf("Compare accepted invalid version %q", invalid)
		}
	}
}
