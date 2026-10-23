package crypt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const totpDigits = 6

var totpBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a cryptographically random, base32-encoded
// secret suitable for an authenticator app.
func GenerateTOTPSecret() (string, error) {
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return totpBase32.EncodeToString(secret), nil
}

// ValidateTOTPCode validates a six-digit RFC 6238 code. One adjacent 30-second
// time step is accepted on either side to tolerate small clock differences.
func ValidateTOTPCode(secret, code string) (bool, error) {
	return validateTOTPCodeAt(secret, code, time.Now())
}

func validateTOTPCodeAt(secret, code string, now time.Time) (bool, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false, err
	}
	if len(code) != totpDigits {
		return false, fmt.Errorf("TOTP code must contain exactly %d digits", totpDigits)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false, fmt.Errorf("TOTP code must contain only digits")
		}
	}

	counter := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		if counter+offset < 0 {
			continue
		}
		candidate := fmt.Sprintf("%0*d", totpDigits, totpValue(key, counter+offset))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true, nil
		}
	}
	return false, nil
}

// IsValidTOTPSecret checks whether a secret is a non-empty base32 key.
func IsValidTOTPSecret(secret string) bool {
	key, err := decodeTOTPSecret(secret)
	return err == nil && len(key) >= 10
}

// BuildTOTPURI creates an otpauth URI that can be imported into common
// authenticator applications.
func BuildTOTPURI(issuer, account, secret string) string {
	issuer = strings.TrimSpace(issuer)
	account = strings.TrimSpace(account)
	label := account
	if issuer != "" {
		label = issuer + ":" + account
	}
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(totpDigits))
	query.Set("period", "30")
	if issuer != "" {
		query.Set("issuer", issuer)
	}
	return "otpauth://totp/" + url.PathEscape(label) + "?" + query.Encode()
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	if secret == "" {
		return nil, fmt.Errorf("TOTP secret is empty")
	}
	key, err := totpBase32.DecodeString(secret)
	if err != nil {
		return nil, fmt.Errorf("decode TOTP secret: %w", err)
	}
	return key, nil
}

func totpValue(key []byte, counter int64) int {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (int(digest[offset])&0x7f)<<24 |
		(int(digest[offset+1])&0xff)<<16 |
		(int(digest[offset+2])&0xff)<<8 |
		(int(digest[offset+3]) & 0xff)
	modulus := 1
	for i := 0; i < totpDigits; i++ {
		modulus *= 10
	}
	return value % modulus
}
