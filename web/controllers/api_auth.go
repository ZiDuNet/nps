package controllers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
)

const userAPITokenPrefix = "npsu_"

var errUserAPITokenEncryptionKeyMissing = errors.New("未配置用户 API 密钥加密主密钥")

// extractUserAPIToken accepts the documented Authorization form and a query
// parameter for clients that cannot set headers. Query tokens should only be
// used over HTTPS because URLs can be copied into access logs.
func extractUserAPIToken(authorization, queryToken string) string {
	authorization = strings.TrimSpace(authorization)
	if len(authorization) >= len("Bearer ") && strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		return strings.TrimSpace(authorization[len("Bearer "):])
	}
	return strings.TrimSpace(queryToken)
}

func generateUserAPIToken() (token, hash string, err error) {
	randomBytes := make([]byte, 32)
	if _, err = rand.Read(randomBytes); err != nil {
		return "", "", err
	}
	token = userAPITokenPrefix + base64.RawURLEncoding.EncodeToString(randomBytes)
	return token, hashUserAPIToken(token), nil
}

func hashUserAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// userAPITokenEncryptionKey derives a purpose-specific AES-256 key from the
// configured secret. Using a distinct domain from the dashboard access key
// keeps credential ciphertexts from being interchangeable across features.
func userAPITokenEncryptionKey() ([]byte, error) {
	secret := strings.TrimSpace(beego.AppConfig.String("auth_crypt_key"))
	if secret == "" {
		secret = strings.TrimSpace(beego.AppConfig.String("auth_key"))
	}
	if secret == "" {
		return nil, errUserAPITokenEncryptionKeyMissing
	}
	sum := sha256.Sum256([]byte("nps-user-api-key-v1:" + secret))
	return sum[:], nil
}

func encryptUserAPIToken(token string) (string, error) {
	encryptionKey, err := userAPITokenEncryptionKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(token), nil)
	payload := append(nonce, sealed...)
	return base64.RawStdEncoding.EncodeToString(payload), nil
}

func decryptUserAPIToken(ciphertext string) (string, error) {
	if strings.TrimSpace(ciphertext) == "" {
		return "", errors.New("用户 API 密钥未加密保存")
	}
	encryptionKey, err := userAPITokenEncryptionKey()
	if err != nil {
		return "", err
	}
	payload, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", errors.New("用户 API 密钥密文无效")
	}
	plain, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func validUserAPIToken(storedHash, token string) bool {
	if strings.TrimSpace(storedHash) == "" || !strings.HasPrefix(token, userAPITokenPrefix) {
		return false
	}
	want := hashUserAPIToken(token)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(want)) == 1
}

func lookupUserByAPIToken(token string) (int, bool) {
	if strings.TrimSpace(token) == "" {
		return 0, false
	}
	var userID int
	file.GetDb().JsonDb.Users.Range(func(_, value interface{}) bool {
		user, ok := value.(*file.User)
		if !ok || user == nil {
			return true
		}
		user.RLock()
		candidateID, storedHash := user.Id, user.APIKeyHash
		user.RUnlock()
		if validUserAPIToken(storedHash, token) && file.GetDb().IsUserActive(candidateID) {
			userID = candidateID
			return false
		}
		return true
	})
	return userID, userID > 0
}
