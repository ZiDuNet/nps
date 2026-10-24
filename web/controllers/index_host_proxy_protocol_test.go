package controllers

import "testing"

func TestParseHostProxyProtocolValue(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		current   int
		submitted bool
		want      int
		wantErr   bool
	}{
		{name: "omitted preserves current", current: 2, want: 2},
		{name: "disabled", value: "0", current: 2, submitted: true, want: 0},
		{name: "v1", value: "1", submitted: true, want: 1},
		{name: "v2", value: "2", submitted: true, want: 2},
		{name: "trim whitespace", value: " 1 ", submitted: true, want: 1},
		{name: "empty value", value: "", current: 2, submitted: true, want: 2, wantErr: true},
		{name: "negative value", value: "-1", current: 2, submitted: true, want: 2, wantErr: true},
		{name: "unsupported value", value: "3", current: 2, submitted: true, want: 2, wantErr: true},
		{name: "non-numeric value", value: "v1", current: 2, submitted: true, want: 2, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseHostProxyProtocolValue(test.value, test.current, test.submitted)
			if got != test.want {
				t.Fatalf("parsed value = %d, want %d", got, test.want)
			}
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}
