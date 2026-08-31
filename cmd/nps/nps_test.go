package main

import (
	"strings"
	"testing"
)

func TestRotateInsecureConfig(t *testing.T) {
	input := "web_password=123\nauth_key=CHANGE_ME\nauth_crypt_key =213\npublic_vkey=123\n"
	output, secrets := rotateInsecureConfig(input)

	for _, value := range []string{"web_password=123", "auth_key=CHANGE_ME", "auth_crypt_key =213", "public_vkey=123"} {
		if strings.Contains(output, value) {
			t.Fatalf("insecure config value %q remains in output %q", value, output)
		}
	}
	if len(secrets["web_password"]) != 8 || len(secrets["auth_key"]) != 8 || len(secrets["auth_crypt_key"]) != 16 {
		t.Fatalf("generated secret lengths are incorrect: %#v", secrets)
	}
	if !strings.Contains(output, "public_vkey=\n") {
		t.Fatalf("legacy public_vkey should be disabled: %q", output)
	}
}

func TestRotateInsecureConfigPreservesExplicitEmptyValues(t *testing.T) {
	input := "web_password=\nauth_key=\nauth_crypt_key=\npublic_vkey=custom\n"
	output, secrets := rotateInsecureConfig(input)
	if output != input {
		t.Fatalf("explicit empty values should remain unchanged: got %q", output)
	}
	if len(secrets) != 0 {
		t.Fatalf("unexpected rotations for explicit values: %#v", secrets)
	}
}
