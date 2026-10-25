package file

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

const FileAccessTargetPrefix = "file://"

// FileAccessKey returns a stable, per-VKEY identifier for an NPC file share.
// It deliberately does not expose the VKEY and is shared by the NPC and NPS
// when binding a file-access mux to its tunnel record.
func FileAccessKey(vkey, localPath, stripPrefix string) string {
	if strings.TrimSpace(vkey) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(vkey))
	for _, field := range []string{"nps-file-access-v1", localPath, stripPrefix} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(field))
	}
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// ParseFileAccessTarget extracts a well-formed file access key from a target
// URI. Keys are fixed-size lowercase hex values; arbitrary URI targets are
// never accepted as file-channel selectors.
func ParseFileAccessTarget(target string) (string, bool) {
	key := strings.TrimSpace(target)
	if !strings.HasPrefix(strings.ToLower(key), FileAccessTargetPrefix) {
		return "", false
	}
	key = strings.TrimSpace(key[len(FileAccessTargetPrefix):])
	if len(key) != 32 {
		return "", false
	}
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 16 {
		return "", false
	}
	return strings.ToLower(key), true
}
