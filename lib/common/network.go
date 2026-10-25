package common

import (
	"net"
	"strings"
)

// EffectiveHTTPClientIP returns the address that an HTTP reverse proxy should
// use for access-control decisions.  The direct peer is always the fallback.
// When allowForwarded is enabled, the first valid X-Real-IP value wins,
// followed by the first valid X-Forwarded-For value.  This deliberately keeps
// the policy opt-in while avoiding a second trusted-proxy allowlist that is
// easy to misconfigure in Docker + Nginx deployments.
func EffectiveHTTPClientIP(remoteAddr, xRealIP, forwardedFor string, allowForwarded bool) string {
	peer := GetIpByAddr(strings.TrimSpace(remoteAddr))
	if peer == "" {
		peer = strings.TrimSpace(remoteAddr)
	}
	if !allowForwarded {
		return peer
	}
	for _, candidate := range []string{xRealIP, firstForwardedValue(forwardedFor)} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if ip := net.ParseIP(strings.Trim(candidate, "[]")); ip != nil {
			return ip.String()
		}
	}
	return peer
}

func firstForwardedValue(value string) string {
	if index := strings.IndexByte(value, ','); index >= 0 {
		return value[:index]
	}
	return value
}

// BuildTCPBindAddr returns an optional source address for outgoing TCP
// connections. Invalid or empty values leave source selection to the OS.
func BuildTCPBindAddr(localIP string) net.Addr {
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(localIP), "[]"))
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return nil
	}
	return &net.TCPAddr{IP: ip}
}

// BuildUDPBindAddr returns an optional source address for outgoing UDP
// connections. Invalid or empty values leave source selection to the OS.
func BuildUDPBindAddr(localIP string) *net.UDPAddr {
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(localIP), "[]"))
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return nil
	}
	return &net.UDPAddr{IP: ip}
}

// IsPublicHost reports whether addr is a domain name or a globally routable
// IP address. Private and reserved IP literals are intentionally excluded so
// local_ip_forward never changes routes to LAN services.
func IsPublicHost(addr string) bool {
	host := strings.TrimSpace(GetIpByAddr(addr))
	if host == "" {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return true
	}
	return IsPublicIPStrict(ip)
}

// IsPublicIPStrict rejects private, special-purpose and documentation
// addresses in addition to the ranges rejected by IsGlobalUnicast.
func IsPublicIPStrict(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		// Carrier-grade NAT: 100.64.0.0/10.
		if ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
			return false
		}
		// TEST-NET-1/2/3, benchmarking and deprecated 6to4 relay ranges.
		if (ipv4[0] == 192 && ipv4[1] == 0 && ipv4[2] == 2) ||
			(ipv4[0] == 198 && ipv4[1] == 51 && ipv4[2] == 100) ||
			(ipv4[0] == 203 && ipv4[1] == 0 && ipv4[2] == 113) ||
			(ipv4[0] == 198 && (ipv4[1] == 18 || ipv4[1] == 19)) ||
			(ipv4[0] == 192 && ipv4[1] == 88 && ipv4[2] == 99) {
			return false
		}
		return true
	}
	// IPv6 documentation range: 2001:db8::/32.
	return len(ip) < 4 || ip[0] != 0x20 || ip[1] != 0x01 || ip[2] != 0x0d || ip[3] != 0xb8
}
