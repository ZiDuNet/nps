package controllers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"ehang.io/nps/lib/file"
)

const userAPITokenPrefix = "npsu_"

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
