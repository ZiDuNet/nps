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
