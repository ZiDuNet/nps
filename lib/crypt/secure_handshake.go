package crypt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"hash"

	"golang.org/x/crypto/blake2b"
)

const (
	secureHandshakeChallengeSize = 32
	secureHandshakeProofSize     = sha256.Size
	secureHandshakeTimestampSize = 8
	secureHandshakePayloadSize   = secureHandshakeTimestampSize + secureHandshakeProofSize
)

var secureHandshakeMarker = func() [32]byte {
	var marker [32]byte
	copy(marker[:], "NPS-SECURE-HANDSHAKE-V1")
	return marker
}()

// SecureHandshakeMarker identifies the challenge-response protocol before
// the client sends any credential-derived material. The fixed size preserves
// the original 32-byte server preamble expected by legacy clients.
func SecureHandshakeMarker() []byte {
	marker := secureHandshakeMarker
	return marker[:]
}

// SecureHandshakeKeyID is a non-secret lookup identifier for the client
// VerifyKey. Authentication still requires the challenge-response proof.
func SecureHandshakeKeyID(verifyKey string) []byte {
	digest := blake2b.Sum256([]byte(verifyKey))
	return digest[:]
}

// NewSecureHandshakeChallenge returns a cryptographically random, one-use
// challenge. It is generated separately for every Bridge connection.
func NewSecureHandshakeChallenge() ([]byte, error) {
	challenge := make([]byte, secureHandshakeChallengeSize)
	if _, err := rand.Read(challenge); err != nil {
		return nil, err
	}
	return challenge, nil
}

// SecureHandshakeClientPayload builds the timestamp and HMAC sent by an NPC
// after receiving a server challenge.
func SecureHandshakeClientPayload(verifyKey, coreVersion, productVersion string, timestamp int64, challenge []byte) ([]byte, error) {
	if verifyKey == "" || len(challenge) != secureHandshakeChallengeSize {
		return nil, errors.New("invalid secure handshake input")
	}
	payload := make([]byte, secureHandshakePayloadSize)
	binary.BigEndian.PutUint64(payload[:secureHandshakeTimestampSize], uint64(timestamp))
	proof := secureHandshakeClientProof(verifyKey, coreVersion, productVersion, payload[:secureHandshakeTimestampSize], challenge)
	copy(payload[secureHandshakeTimestampSize:], proof)
	return payload, nil
}

// VerifySecureHandshakeClientPayload checks size, timestamp freshness, and
// the challenge-bound client proof.
func VerifySecureHandshakeClientPayload(verifyKey, coreVersion, productVersion string, payload, challenge []byte, now, window int64) error {
	if verifyKey == "" || len(payload) != secureHandshakePayloadSize || len(challenge) != secureHandshakeChallengeSize {
		return errors.New("invalid secure handshake payload")
	}
	ts := binary.BigEndian.Uint64(payload[:secureHandshakeTimestampSize])
	if ts > uint64(^uint64(0)>>1) || !timestampWithinWindow(int64(ts), now, window) {
		return errors.New("secure handshake timestamp outside allowed window")
	}
	want := secureHandshakeClientProof(verifyKey, coreVersion, productVersion, payload[:secureHandshakeTimestampSize], challenge)
	if subtle.ConstantTimeCompare(payload[secureHandshakeTimestampSize:], want) != 1 {
		return errors.New("secure handshake proof mismatch")
	}
	return nil
}

// SecureHandshakeServerProof lets the NPC authenticate the server response
// before it sends the connection type and establishes a control/data channel.
func SecureHandshakeServerProof(verifyKey string, clientPayload, challenge []byte) ([]byte, error) {
	if verifyKey == "" || len(clientPayload) != secureHandshakePayloadSize || len(challenge) != secureHandshakeChallengeSize {
		return nil, errors.New("invalid secure handshake response input")
	}
	mac := hmac.New(sha256.New, []byte(verifyKey))
	writeHandshakeField(mac, []byte("nps-server-handshake-v1"))
	writeHandshakeField(mac, clientPayload[secureHandshakeTimestampSize:])
	writeHandshakeField(mac, challenge)
	return mac.Sum(nil), nil
}

// VerifySecureHandshakeServerProof validates the server's challenge-bound
// response using constant-time comparison.
func VerifySecureHandshakeServerProof(verifyKey string, clientPayload, challenge, proof []byte) error {
	want, err := SecureHandshakeServerProof(verifyKey, clientPayload, challenge)
	if err != nil {
		return err
	}
	if len(proof) != secureHandshakeProofSize || subtle.ConstantTimeCompare(proof, want) != 1 {
		return errors.New("secure handshake server proof mismatch")
	}
	return nil
}

func secureHandshakeClientProof(verifyKey, coreVersion, productVersion string, timestamp, challenge []byte) []byte {
	mac := hmac.New(sha256.New, []byte(verifyKey))
	writeHandshakeField(mac, []byte("nps-client-handshake-v1"))
	writeHandshakeField(mac, timestamp)
	writeHandshakeField(mac, []byte(coreVersion))
	writeHandshakeField(mac, []byte(productVersion))
	writeHandshakeField(mac, challenge)
	return mac.Sum(nil)
}

func writeHandshakeField(mac hash.Hash, field []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(field)))
	_, _ = mac.Write(size[:])
	_, _ = mac.Write(field)
}

func timestampWithinWindow(timestamp, now, window int64) bool {
	if window < 0 {
		return false
	}
	if window == 0 {
		return true
	}
	if timestamp >= now {
		return uint64(timestamp)-uint64(now) <= uint64(window)
	}
	return uint64(now)-uint64(timestamp) <= uint64(window)
}
