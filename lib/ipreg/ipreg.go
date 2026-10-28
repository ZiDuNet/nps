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
	"sort"
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

// Registration is a read-only snapshot of an active registration.  The
// in-memory store intentionally never exposes the internal map so callers can
// safely render or audit registrations without holding the package lock.
type Registration struct {
	ClientID int
	IP       netip.Addr
	Until    time.Time
}

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
	return len(List())
}

// List returns all active registrations and removes expired entries as part of
// the same operation. Results are sorted by client bucket and address so the
// administrator view remains stable between refreshes.
func List() []Registration {
	now := time.Now()
	mu.Lock()
	defer mu.Unlock()
	return listLocked(now, nil)
}

// ListForClients returns active registrations whose bucket is in clientIDs.
// The global/public bucket is intentionally excluded unless AnyClient is
// present in the supplied set. Dashboard users are given a set containing only
// their own clients, so filtering here prevents a search or pagination race
// from exposing a registration owned by another account.
func ListForClients(clientIDs map[int]struct{}) []Registration {
	now := time.Now()
	mu.Lock()
	defer mu.Unlock()
	return listLocked(now, clientIDs)
}

func listLocked(now time.Time, clientIDs map[int]struct{}) []Registration {
	rows := make([]Registration, 0)
	for clientID, byIP := range reg {
		if clientIDs != nil {
			if _, allowed := clientIDs[clientID]; !allowed {
				continue
			}
		}
		for ip, until := range byIP {
			if !until.After(now) {
				delete(byIP, ip)
				continue
			}
			rows = append(rows, Registration{ClientID: clientID, IP: ip, Until: until})
		}
		if len(byIP) == 0 {
			delete(reg, clientID)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ClientID != rows[j].ClientID {
			return rows[i].ClientID < rows[j].ClientID
		}
		return rows[i].IP.String() < rows[j].IP.String()
	})
	return rows
}

// CountForClients returns the number of active registrations in the supplied
// client buckets.
func CountForClients(clientIDs map[int]struct{}) int {
	return len(ListForClients(clientIDs))
}

// Revoke removes one registration from the requested bucket. A caller can
// revoke a global registration with AnyClient or a client-scoped registration
// with its client ID.
func Revoke(clientID int, ip netip.Addr) bool {
	ip = normalize(ip)
	if !ip.IsValid() {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	byIP := reg[clientID]
	if byIP == nil {
		return false
	}
	if _, ok := byIP[ip]; !ok {
		return false
	}
	delete(byIP, ip)
	if len(byIP) == 0 {
		delete(reg, clientID)
	}
	return true
}

// RevokeString is the string counterpart used by form/API handlers. It
// accepts both plain IPs and IP:port values, matching RegisteredString.
func RevokeString(clientID int, value string) bool {
	ip, ok := ParseIP(value)
	return ok && Revoke(clientID, ip)
}

// ClearExpired removes only registrations whose TTL has elapsed and returns
// the number removed. Calling List already performs this cleanup; this helper
// exists so the admin action can report an explicit result.
func ClearExpired() int {
	mu.Lock()
	defer mu.Unlock()
	return clearExpiredLocked(nil)
}

// ClearExpiredForClients removes expired registrations only from the supplied
// client buckets.
func ClearExpiredForClients(clientIDs map[int]struct{}) int {
	mu.Lock()
	defer mu.Unlock()
	return clearExpiredLocked(clientIDs)
}

func clearExpiredLocked(clientIDs map[int]struct{}) int {
	now := time.Now()
	removed := 0
	for clientID, byIP := range reg {
		if clientIDs != nil {
			if _, allowed := clientIDs[clientID]; !allowed {
				continue
			}
		}
		for ip, until := range byIP {
			if !until.After(now) {
				delete(byIP, ip)
				removed++
			}
		}
		if len(byIP) == 0 {
			delete(reg, clientID)
		}
	}
	return removed
}

// Reset clears registrations.  It is useful for tests and for an explicit
// administrator reset; normal restarts naturally start with an empty store.
func Reset() {
	mu.Lock()
	reg = map[int]map[netip.Addr]time.Time{}
	mu.Unlock()
}

// ResetClients removes every registration in the supplied client buckets and
// returns the number removed. Registrations for other clients, including the
// public bucket, remain untouched.
func ResetClients(clientIDs map[int]struct{}) int {
	mu.Lock()
	defer mu.Unlock()
	removed := 0
	for clientID, byIP := range reg {
		if _, allowed := clientIDs[clientID]; !allowed {
			continue
		}
		removed += len(byIP)
		delete(reg, clientID)
	}
	return removed
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
