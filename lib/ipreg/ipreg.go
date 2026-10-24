// Package ipreg stores short-lived visitor IP registrations.
//
// Registrations are intentionally kept in memory.  They are an access
// credential with a bounded lifetime, not configuration data; persisting them
// would make an old address usable after a restart and would also grow the
// deployment's JSON files indefinitely.
package ipreg

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

// AnyClient is the registration bucket used by the administrator/public key.
// An address registered in this bucket is accepted by every client that has
// RegisteredOnly enabled.
const AnyClient = 0

var (
	mu  sync.Mutex
	reg = map[int]map[netip.Addr]time.Time{}
)

// Register adds or extends an address registration.  A shorter request never
// reduces an already active registration.
func Register(clientID int, ip netip.Addr, ttl time.Duration) time.Time {
	ip = normalize(ip)
	if !ip.IsValid() || ttl <= 0 {
		return time.Time{}
	}
	until := time.Now().Add(ttl)
	mu.Lock()
	defer mu.Unlock()
	byIP := reg[clientID]
	if byIP == nil {
		byIP = make(map[netip.Addr]time.Time)
		reg[clientID] = byIP
	}
	if current, ok := byIP[ip]; ok && current.After(until) {
		return current
	}
	byIP[ip] = until
	return until
}

// Registered reports whether an address is currently registered for the
// client or in the administrator/public bucket.
func Registered(clientID int, ip netip.Addr) bool {
	ip = normalize(ip)
	if !ip.IsValid() {
		return false
	}
	now := time.Now()
	mu.Lock()
	defer mu.Unlock()
	return validLocked(reg[clientID], ip, now) || validLocked(reg[AnyClient], ip, now)
}

// RegisteredAddr is Registered for a net.Addr.
func RegisteredAddr(clientID int, addr net.Addr) bool {
	ip, ok := addrIP(addr)
	return ok && Registered(clientID, ip)
}

// RegisteredString accepts either an IP or an IP:port value.
func RegisteredString(clientID int, value string) bool {
	ip, ok := ParseIP(value)
	return ok && Registered(clientID, ip)
}

// ParseIP parses an IP address with or without a port.  It deliberately does
// not resolve host names: registrations are for the observed source address,
// and DNS resolution here would make the policy ambiguous.
func ParseIP(value string) (netip.Addr, bool) {
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return normalize(addrPort.Addr()), true
	}
	if ip, err := netip.ParseAddr(value); err == nil {
		return normalize(ip), true
	}
	return netip.Addr{}, false
}

// Count returns the number of non-expired registrations.
func Count() int {
	now := time.Now()
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for clientID, byIP := range reg {
		for ip, until := range byIP {
			if until.After(now) {
				count++
			} else {
				delete(byIP, ip)
			}
		}
		if len(byIP) == 0 {
			delete(reg, clientID)
		}
	}
	return count
}

// Reset clears registrations.  It is useful for tests and for an explicit
// administrator reset; normal restarts naturally start with an empty store.
func Reset() {
	mu.Lock()
	reg = map[int]map[netip.Addr]time.Time{}
	mu.Unlock()
}

func validLocked(byIP map[netip.Addr]time.Time, ip netip.Addr, now time.Time) bool {
	until, ok := byIP[ip]
	if !ok {
		return false
	}
	if !until.After(now) {
		delete(byIP, ip)
		return false
	}
	return true
}

func normalize(ip netip.Addr) netip.Addr {
	return ip.Unmap().WithZone("")
}

func addrIP(addr net.Addr) (netip.Addr, bool) {
	if addr == nil {
		return netip.Addr{}, false
	}
	switch value := addr.(type) {
	case *net.TCPAddr:
		if value == nil {
			return netip.Addr{}, false
		}
		ip, ok := netip.AddrFromSlice(value.IP)
		return normalize(ip), ok
	case *net.UDPAddr:
		if value == nil {
			return netip.Addr{}, false
		}
		ip, ok := netip.AddrFromSlice(value.IP)
		return normalize(ip), ok
	default:
		return ParseIP(addr.String())
	}
}
