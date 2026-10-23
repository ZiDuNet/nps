package crypt

import (
	"encoding/base32"
	"fmt"
	"testing"
	"time"
)

func TestGenerateTOTPSecret(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() error = %v", err)
	}
	if !IsValidTOTPSecret(secret) {
		t.Fatalf("generated secret %q is invalid", secret)
	}
	if len(secret) != 32 {
		t.Fatalf("generated secret length = %d, want 32", len(secret))
	}
}

func TestTOTPMatchesRFC6238SHA1Vector(t *testing.T) {
	key := []byte("12345678901234567890")
	if got := totpValue(key, 1); got != 287082 {
		t.Fatalf("totpValue(counter=1) = %06d, want 287082", got)
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	valid, err := validateTOTPCodeAt(secret, "287082", time.Unix(59, 0))
	if err != nil || !valid {
		t.Fatalf("validateTOTPCodeAt(RFC vector) = (%v, %v), want (true, nil)", valid, err)
	}
}

func TestValidateTOTPCodeAcceptsAdjacentTimeStep(t *testing.T) {
	key := []byte("test-secret-key-123")
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	code := ""
	// 61 seconds maps to the next TOTP counter; validate at 59 seconds to
	// verify that the bounded clock-skew window accepts one neighboring step.
	code = formatTOTP(totpValue(key, 2))
	valid, err := validateTOTPCodeAt(secret, code, time.Unix(59, 0))
	if err != nil || !valid {
		t.Fatalf("validateTOTPCodeAt(adjacent step) = (%v, %v), want (true, nil)", valid, err)
	}
}

func TestValidateTOTPCodeRejectsMalformedInput(t *testing.T) {
	if valid, err := ValidateTOTPCode("not-base32", "123456"); err == nil || valid {
		t.Fatalf("invalid secret returned (%v, %v), want (false, error)", valid, err)
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() error = %v", err)
	}
	for _, code := range []string{"12345", "1234567", "12a456"} {
		if valid, err := ValidateTOTPCode(secret, code); err == nil || valid {
			t.Errorf("ValidateTOTPCode(%q) = (%v, %v), want (false, error)", code, valid, err)
		}
	}
}

func formatTOTP(value int) string {
	return fmt.Sprintf("%06d", value)
}
