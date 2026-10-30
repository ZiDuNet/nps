package version

import (
	"fmt"
	"strconv"
	"strings"
)

const VERSION = "1.1.39"

// SecureHandshakeMinVersion is the first NPC release that uses the secure
// Bridge handshake. Clients at or below 1.1.28 remain on the legacy handshake
// so that upgrading NPS does not disconnect already deployed clients.
const SecureHandshakeMinVersion = "1.1.29"

// UsesSecureHandshake reports whether a stable product release uses the
// timestamped, challenge-response Bridge handshake. Invalid release strings
// are not silently classified as legacy by the server.
func UsesSecureHandshake(release string) bool {
	comparison, err := Compare(release, SecureHandshakeMinVersion)
	return err == nil && comparison >= 0
}

// SupportsLegacyHandshake reports whether a stable product release is within
// the compatibility window for the original Bridge handshake.
func SupportsLegacyHandshake(release string) bool {
	comparison, err := Compare(release, SecureHandshakeMinVersion)
	return err == nil && comparison < 0
}

// ClientUpdateMinVersion is the first client release that understands the
// server-pushed self-update signal. Older clients must be updated manually
// once before remote updates can be used.
const ClientUpdateMinVersion = "1.1.23"

// HealthConfigMinVersion is the first NPC release that understands the
// additive HEALTH_CONFIG control message. Servers must not send that flag to
// older clients because their control reader would interpret the payload as a
// different message and desynchronise the connection.
const HealthConfigMinVersion = "1.1.29"

// ModernP2PMinVersion is the first ZiDuNet NPC release that understands the
// hole-punch control protocol. Older clients continue to use the historical
// UDP/KCP P2P path and are never sent modern control frames.
const ModernP2PMinVersion = "1.1.29"

// KeyedFileMinVersion is the first NPC release that identifies each file
// access mux independently. Older clients continue to use the legacy shared
// file channel and handshake without an extra payload.
const KeyedFileMinVersion = "1.1.30"

func SupportsKeyedFiles(release string) bool {
	comparison, err := Compare(release, KeyedFileMinVersion)
	return err == nil && comparison >= 0
}

func SupportsModernP2P(release string) bool {
	comparison, err := Compare(release, ModernP2PMinVersion)
	return err == nil && comparison >= 0
}

// GitHubRepository is the canonical source for release metadata and binaries.
const GitHubRepository = "ZiDuNet/nps"

// Compare compares stable semantic versions such as 1.1.3 and v1.1.3.
// It deliberately rejects branch names and prerelease tags so an updater never
// installs an arbitrary GitHub Release such as the historical "master" release.
func Compare(a, b string) (int, error) {
	left, err := parseStableSemver(a)
	if err != nil {
		return 0, err
	}
	right, err := parseStableSemver(b)
	if err != nil {
		return 0, err
	}

	for i := range left {
		if left[i] < right[i] {
			return -1, nil
		}
		if left[i] > right[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func parseStableSemver(raw string) ([3]int, error) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return [3]int{}, fmt.Errorf("版本号 %q 必须是 vX.Y.Z", raw)
	}

	var parsed [3]int
	for i, part := range parts {
		if part == "" {
			return [3]int{}, fmt.Errorf("版本号 %q 必须是 vX.Y.Z", raw)
		}
		if len(part) > 1 && part[0] == '0' {
			return [3]int{}, fmt.Errorf("版本号 %q 不能包含前导零", raw)
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return [3]int{}, fmt.Errorf("版本号 %q 必须是 vX.Y.Z", raw)
			}
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, fmt.Errorf("解析版本号 %q: %w", raw, err)
		}
		parsed[i] = number
	}
	return parsed, nil
}

// Compulsory minimum version, Minimum downward compatibility to this version
func GetVersion() string {
	return "0.26.0"
}
