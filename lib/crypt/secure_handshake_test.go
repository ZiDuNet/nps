package crypt

import (
	"bytes"
	"testing"
)

func TestSecureHandshakeChallengeResponse(t *testing.T) {
	const (
		verifyKey      = "example-client-key"
		coreVersion    = "0.26.0"
		productVersion = "1.1.29"
	)
	challenge := bytes.Repeat([]byte{0x5a}, secureHandshakeChallengeSize)
	payload, err := SecureHandshakeClientPayload(verifyKey, coreVersion, productVersion, 1_798_000_000, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySecureHandshakeClientPayload(verifyKey, coreVersion, productVersion, payload, challenge, 1_798_000_001, 300); err != nil {
		t.Fatalf("valid client payload was rejected: %v", err)
	}

	serverProof, err := SecureHandshakeServerProof(verifyKey, payload, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySecureHandshakeServerProof(verifyKey, payload, challenge, serverProof); err != nil {
		t.Fatalf("valid server proof was rejected: %v", err)
	}
}

func TestSecureHandshakeRejectsStaleAndTamperedProofs(t *testing.T) {
	const (
		verifyKey   = "example-client-key"
		coreVersion = "0.26.0"
		product     = "1.1.29"
	)
	challenge := bytes.Repeat([]byte{0x21}, secureHandshakeChallengeSize)
	payload, err := SecureHandshakeClientPayload(verifyKey, coreVersion, product, 1_798_000_000, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySecureHandshakeClientPayload(verifyKey, coreVersion, product, payload, challenge, 1_798_001_000, 300); err == nil {
		t.Fatal("stale client payload was accepted")
	}

	tampered := append([]byte(nil), payload...)
	tampered[len(tampered)-1] ^= 0x01
	if err := VerifySecureHandshakeClientPayload(verifyKey, coreVersion, product, tampered, challenge, 1_798_000_000, 300); err == nil {
		t.Fatal("tampered client proof was accepted")
	}
	if err := VerifySecureHandshakeClientPayload("wrong-key", coreVersion, product, payload, challenge, 1_798_000_000, 300); err == nil {
		t.Fatal("proof authenticated with the wrong key")
	}
	if err := VerifySecureHandshakeClientPayload(verifyKey, coreVersion, product, payload, bytes.Repeat([]byte{0x22}, secureHandshakeChallengeSize), 1_798_000_000, 300); err == nil {
		t.Fatal("proof authenticated against a different challenge")
	}
}

func TestSecureHandshakeMarkerAndKeyID(t *testing.T) {
	if got := len(SecureHandshakeMarker()); got != 32 {
		t.Fatalf("secure handshake marker length = %d, want 32", got)
	}
	first := SecureHandshakeKeyID("same-key")
	second := SecureHandshakeKeyID("same-key")
	if len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatalf("key identifier is not a stable 32-byte digest: %x", first)
	}
	if bytes.Equal(first, SecureHandshakeKeyID("different-key")) {
		t.Fatal("different keys have the same identifier")
	}
}
