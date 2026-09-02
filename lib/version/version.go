package version

import (
	"fmt"
	"strconv"
	"strings"
)

const VERSION = "1.1.6"

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
