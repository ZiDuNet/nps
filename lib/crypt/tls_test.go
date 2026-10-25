package crypt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"
)

func TestParseLoginPayloadSupportsOAEPAndPKCS1(t *testing.T) {
	publicPEM, err := GetRSAPublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(publicPEM))
	if block == nil {
		t.Fatal("public key PEM did not decode")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key type = %T, want RSA", parsed)
	}
	payload := LoginPayload{Nonce: "nonce", Timestamp: time.Now().UnixMilli(), Password: "secret"}
	plain, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		seal func() ([]byte, error)
	}{
		{name: "oaep", seal: func() ([]byte, error) {
			return rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, plain, nil)
		}},
		{name: "pkcs1", seal: func() ([]byte, error) {
			//nolint:staticcheck // browser JSEncrypt compatibility path.
			return rsa.EncryptPKCS1v15(rand.Reader, publicKey, plain)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ciphertext, err := test.seal()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := ParseLoginPayload(base64.StdEncoding.EncodeToString(ciphertext))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Nonce != payload.Nonce || decoded.Password != payload.Password || decoded.Timestamp != payload.Timestamp {
				t.Fatalf("decoded payload = %#v, want %#v", decoded, payload)
			}
		})
	}
}
