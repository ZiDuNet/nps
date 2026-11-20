package crypt

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

const loginChallengeTTLSeconds = 300

var (
	loginChallengeKey   []byte
	loginChallengeKeyMu sync.RWMutex
)

// SetLoginChallengeKey installs the persistent HMAC key used to sign Web login
// challenges. It is derived from conf/instance.key when the management panel
// starts and stays stable across restarts.
func SetLoginChallengeKey(key []byte) {
	loginChallengeKeyMu.Lock()
	defer loginChallengeKeyMu.Unlock()
	loginChallengeKey = append([]byte(nil), key...)
}

// LoginChallenge is a short-lived, stateless anti-replay token for the Web
// login form. The nonce is signed together with its expiry so the server can
// validate it without server-side session state and without embedding the nonce
// in cacheable login-page HTML.
type LoginChallenge struct {
	Nonce     string `json:"nonce"`
	Expires   int64  `json:"expires"`
	Signature string `json:"signature"`
}

// IssueLoginChallenge issues a fresh challenge signed with the configured key.
func IssueLoginChallenge(now time.Time) (*LoginChallenge, error) {
	loginChallengeKeyMu.RLock()
	key := append([]byte(nil), loginChallengeKey...)
	loginChallengeKeyMu.RUnlock()
	if len(key) == 0 {
		return nil, fmt.Errorf("login challenge key is not initialized")
	}
	nonce := GetRandomString(16)
	expires := now.Unix() + loginChallengeTTLSeconds
	return &LoginChallenge{
		Nonce:     nonce,
		Expires:   expires,
		Signature: signLoginChallenge(key, nonce, expires),
	}, nil
}

// VerifyLoginChallenge verifies the signature and expiry of a login challenge.
func VerifyLoginChallenge(nonce string, expires int64, signature string, now time.Time) bool {
	if strings.TrimSpace(nonce) == "" || expires <= 0 || strings.TrimSpace(signature) == "" {
		return false
	}
	loginChallengeKeyMu.RLock()
	key := append([]byte(nil), loginChallengeKey...)
	loginChallengeKeyMu.RUnlock()
	if len(key) == 0 {
		return false
	}
	if expires <= now.Unix() {
		return false
	}
	expected := signLoginChallenge(key, nonce, expires)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(signature))) == 1
}

func signLoginChallenge(key []byte, nonce string, expires int64) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%s|%d", nonce, expires)
	return hex.EncodeToString(mac.Sum(nil))
}
