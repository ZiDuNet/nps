package file

import (
	"net"
	"strings"

	"ehang.io/nps/lib/common"
)

// GlobalIPDenied evaluates the administrator-owned source lists. Rules may be
// exact IPs or CIDR ranges. With both lists configured, priority 0 (deny first)
// requires an address to be on the allowlist and not on the denylist; priority
// 1 (allow first) lets an allowlist match override a denylist match and leaves
// unrelated addresses untouched. A single list keeps the intuitive behavior:
// denylist blocks matches, allowlist permits only matches.
func (s *DbUtils) GlobalIPDenied(ipPort string) bool {
	if s == nil || s.JsonDb == nil {
		return false
	}
	return s.JsonDb.GlobalIPDenied(ipPort)
}

// GlobalIPDenied evaluates the global policy on a standalone JSON database.
// Keeping the method here is useful to listeners and tests that construct a
// database without installing the process-wide DbUtils singleton.
func (s *JsonDb) GlobalIPDenied(ipPort string) bool {
	if s == nil {
		return false
	}
	global := s.getGlobal()
	if global == nil {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(common.GetIpByAddr(ipPort)))
	if ip == nil {
		return false
	}
	global.RLock()
	black := append([]string(nil), global.BlackIpList...)
	white := append([]string(nil), global.WhiteIpList...)
	priority := global.IpListPriority
	global.RUnlock()
	blackMatch := matchesIPRules(ip, black)
	whiteMatch := matchesIPRules(ip, white)
	switch {
	case len(black) == 0 && len(white) == 0:
		return false
	case len(black) == 0:
		return !whiteMatch
	case len(white) == 0:
		return blackMatch
	case priority == 1:
		return blackMatch && !whiteMatch
	default:
		return blackMatch || !whiteMatch
	}
}

func matchesIPRules(ip net.IP, rules []string) bool {
	for _, raw := range rules {
		for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\r' || r == '\n' || r == ',' }) {
			rule := strings.TrimSpace(line)
			if rule == "" {
				continue
			}
			if strings.Contains(rule, "/") {
				if _, network, err := net.ParseCIDR(rule); err == nil && network.Contains(ip) {
					return true
				}
				continue
			}
			if candidate := net.ParseIP(strings.Trim(rule, "[]")); candidate != nil && candidate.Equal(ip) {
				return true
			}
		}
	}
	return false
}
