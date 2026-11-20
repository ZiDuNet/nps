package crypt

import (
	"testing"
	"time"
)

func TestLoginChallengeSignAndVerify(t *testing.T) {
	SetLoginChallengeKey([]byte("0123456789abcdef0123456789abcdef"))
	now := time.Now()
	challenge, err := IssueLoginChallenge(now)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Nonce == "" || challenge.Expires <= now.Unix() || challenge.Signature == "" {
		t.Fatalf("incomplete challenge: %+v", challenge)
	}
	if !VerifyLoginChallenge(challenge.Nonce, challenge.Expires, challenge.Signature, now.Add(time.Minute)) {
		t.Fatal("valid challenge rejected")
	}
}

func TestLoginChallengeRejectsTamperingAndExpiry(t *testing.T) {
	SetLoginChallengeKey([]byte("0123456789abcdef0123456789abcdef"))
	now := time.Now()
	challenge, err := IssueLoginChallenge(now)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyLoginChallenge(challenge.Nonce+"x", challenge.Expires, challenge.Signature, now) {
		t.Fatal("tampered nonce accepted")
	}
	if VerifyLoginChallenge(challenge.Nonce, challenge.Expires+1, challenge.Signature, now) {
		t.Fatal("tampered expiry accepted")
	}
	if VerifyLoginChallenge(challenge.Nonce, challenge.Expires, challenge.Signature, time.Unix(challenge.Expires, 0)) {
		t.Fatal("expired challenge accepted")
	}
}

func TestLoginChallengeRequiresKey(t *testing.T) {
	SetLoginChallengeKey(nil)
	if _, err := IssueLoginChallenge(time.Now()); err == nil {
		t.Fatal("challenge issued without a key")
	}
	if VerifyLoginChallenge("nonce", time.Now().Unix()+60, "signature", time.Now()) {
		t.Fatal("challenge verified without a key")
	}
}
