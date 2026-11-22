package controllers

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/astaxie/beego"
)

const (
	npsHMACAuthorizationScheme = "NPS-HMAC"
	npsHMACAuthKeyID           = "auth_key"
	npsHMACManagedKeyID        = "managed_admin"
	npsHMACMaxBodyBytes        = 10 << 20
	npsHMACNonceCacheLimit     = 100000
	npsHMACNonceMinBytes       = 16
	npsHMACNonceMaxBytes       = 64
)

var npsHMACNonces = struct {
	sync.Mutex
	entries   map[string]time.Time
	lastSweep time.Time
}{entries: make(map[string]time.Time)}

// apiRequestBodyHash reads and restores a bounded request body. API signatures
// cover the exact bytes on the wire; restoring the body keeps downstream form
// and JSON parsing behavior unchanged.
func apiRequestBodyHash(request *http.Request) (string, error) {
	if request == nil || request.Body == nil {
		return hashEmptyBody(), nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, npsHMACMaxBodyBytes+1))
	request.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	if len(body) > npsHMACMaxBodyBytes {
		return "", errors.New("API 请求正文超过 10 MiB 上限")
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func hashEmptyBody() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

func hasNPSHMACCredentials(request *http.Request) bool {
	if request == nil {
		return false
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(authorization) >= len(npsHMACAuthorizationScheme) &&
		strings.EqualFold(authorization[:len(npsHMACAuthorizationScheme)], npsHMACAuthorizationScheme) {
		return true
	}
	return request.Header.Get("X-NPS-Timestamp") != "" ||
		request.Header.Get("X-NPS-Nonce") != "" ||
		request.Header.Get("X-NPS-Key-Id") != ""
}

func verifyNPSHMACRequest(request *http.Request, bodyHash string, now time.Time) bool {
	if request == nil || bodyHash == "" {
		return false
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], npsHMACAuthorizationScheme) {
		return false
	}
	provided, err := hex.DecodeString(parts[1])
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	timestampText := strings.TrimSpace(request.Header.Get("X-NPS-Timestamp"))
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || timestampText != strconv.FormatInt(timestamp, 10) {
		return false
	}
	window := beego.AppConfig.DefaultInt("api_hmac_time_window", 60)
	if window <= 0 {
		window = 60
	}
	if window > 3600 {
		window = 3600
	}
	nowUnix := now.Unix()
	// Compare against a bounded range instead of subtracting timestamps. A
	// malicious int64 timestamp could overflow `nowUnix - timestamp` and wrap
	// into the accepted window.
	if timestamp < nowUnix-int64(window) || timestamp > nowUnix+int64(window) {
		return false
	}
	nonce := strings.TrimSpace(request.Header.Get("X-NPS-Nonce"))
	nonceBytes, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(nonceBytes) < npsHMACNonceMinBytes || len(nonceBytes) > npsHMACNonceMaxBytes {
		return false
	}
	keyID := strings.TrimSpace(request.Header.Get("X-NPS-Key-Id"))
	secret, ok := npsHMACSecret(keyID)
	if !ok {
		return false
	}
	canonical, err := npsHMACCanonicalRequest(request, bodyHash, timestampText, nonce, keyID)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(expected, provided) != 1 {
		return false
	}
	return consumeNPSHMACNonce(keyID, nonce, time.Unix(timestamp, 0).Add(time.Duration(window+1)*time.Second), now)
}

func npsHMACSecret(keyID string) (string, bool) {
	switch keyID {
	case npsHMACAuthKeyID:
		secret := strings.TrimSpace(beego.AppConfig.String("auth_key"))
		return secret, secret != ""
	case npsHMACManagedKeyID:
		return managedAdminAPIKey()
	default:
		return "", false
	}
}

func npsHMACCanonicalRequest(request *http.Request, bodyHash, timestamp, nonce, keyID string) (string, error) {
	if request == nil || request.URL == nil {
		return "", errors.New("请求 URL 为空")
	}
	path := request.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		// Reject malformed percent escapes and unescaped semicolons rather
		// than signing a partial interpretation that differs across clients.
		return "", errors.New("请求查询串格式无效")
	}
	queryString := query.Encode()
	return strings.Join([]string{
		strings.ToUpper(request.Method),
		path,
		queryString,
		strings.ToLower(bodyHash),
		timestamp,
		nonce,
		keyID,
	}, "\n"), nil
}

func consumeNPSHMACNonce(keyID, nonce string, expiresAt, now time.Time) bool {
	cacheKey := keyID + "\x00" + nonce
	npsHMACNonces.Lock()
	defer npsHMACNonces.Unlock()
	forceSweep := len(npsHMACNonces.entries) >= npsHMACNonceCacheLimit || now.Sub(npsHMACNonces.lastSweep) >= time.Minute
	if forceSweep {
		for key, expiry := range npsHMACNonces.entries {
			if !expiry.After(now) {
				delete(npsHMACNonces.entries, key)
			}
		}
		npsHMACNonces.lastSweep = now
	}
	if expiry, exists := npsHMACNonces.entries[cacheKey]; exists && expiry.After(now) {
		return false
	}
	if len(npsHMACNonces.entries) >= npsHMACNonceCacheLimit {
		return false
	}
	npsHMACNonces.entries[cacheKey] = expiresAt
	return true
}

// signNPSHMAC is also used by tests and local integrations that need to create
// a request compatible with npsctl.
func signNPSHMAC(secret string, request *http.Request, bodyHash, timestamp, nonce, keyID string) (string, error) {
	canonical, err := npsHMACCanonicalRequest(request, bodyHash, timestamp, nonce, keyID)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func newNPSHMACNonce() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
