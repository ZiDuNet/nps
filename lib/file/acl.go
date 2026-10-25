package file

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/acl"
	"github.com/astaxie/beego/logs"
)

// SourceLists are the source access lists of a global setting, client,
// tunnel, or host. The fields are persisted using the established JSON names;
// the compiled policy is runtime-only and therefore never changes old JSON
// files when the feature is unused.
type SourceLists struct {
	IpListPriority int
	BlackIpList    []string
	WhiteIpList    []string

	aclMu     sync.RWMutex
	sourceACL *acl.Source
	compiled  atomic.Bool
}

// Client keeps the legacy blacklist as a direct JSON field for compatibility.
// These forwarding methods keep the structured ACL API
// available without forcing callers to know about that storage detail.
func (c *Client) syncSourceListsFromFields() {
	if c == nil {
		return
	}
	c.SourceLists.aclMu.Lock()
	c.SourceLists.IpListPriority = c.IpListPriority
	c.SourceLists.BlackIpList = append([]string(nil), c.BlackIpList...)
	c.SourceLists.WhiteIpList = append([]string(nil), c.WhiteIpList...)
	c.SourceLists.aclMu.Unlock()
}

func (c *Client) syncSourceListsToFields() {
	if c == nil {
		return
	}
	c.SourceLists.aclMu.RLock()
	priority := c.SourceLists.IpListPriority
	black := append([]string(nil), c.SourceLists.BlackIpList...)
	white := append([]string(nil), c.SourceLists.WhiteIpList...)
	c.SourceLists.aclMu.RUnlock()
	c.Lock()
	c.IpListPriority = priority
	c.BlackIpList = black
	c.WhiteIpList = white
	c.Unlock()
}

func (c *Client) SetSourceLists(priority int, black, white []string, scope acl.Scope, owner string) {
	if c == nil {
		return
	}
	c.SourceLists.SetSourceLists(priority, black, white, scope, owner)
	c.syncSourceListsToFields()
}

func (c *Client) SourceACL() *acl.Source {
	if c == nil {
		return nil
	}
	c.syncSourceListsFromFields()
	return c.SourceLists.SourceACL()
}

func (c *Client) RefreshSourceLists(owner string) {
	if c == nil {
		return
	}
	c.syncSourceListsFromFields()
	c.SourceLists.RefreshSourceLists(owner)
}

func (c *Client) CompileSourceLists(scope acl.Scope, owner string) {
	if c == nil {
		return
	}
	c.syncSourceListsFromFields()
	c.SourceLists.CompileSourceLists(scope, owner)
}

// Global has the same forwarding layer as Client because its legacy fields
// predate SourceLists. This preserves old JSON and old Go struct literals.
func (g *Glob) syncSourceListsFromFields() {
	if g == nil {
		return
	}
	g.SourceLists.aclMu.Lock()
	g.SourceLists.IpListPriority = g.IpListPriority
	g.SourceLists.BlackIpList = append([]string(nil), g.BlackIpList...)
	g.SourceLists.WhiteIpList = append([]string(nil), g.WhiteIpList...)
	g.SourceLists.aclMu.Unlock()
}

func (g *Glob) SetSourceLists(priority int, black, white []string, scope acl.Scope, owner string) {
	if g == nil {
		return
	}
	g.SourceLists.SetSourceLists(priority, black, white, scope, owner)
	g.SourceLists.aclMu.RLock()
	g.IpListPriority = g.SourceLists.IpListPriority
	g.BlackIpList = append([]string(nil), g.SourceLists.BlackIpList...)
	g.WhiteIpList = append([]string(nil), g.SourceLists.WhiteIpList...)
	g.SourceLists.aclMu.RUnlock()
}

func (g *Glob) SourceACL() *acl.Source {
	if g == nil {
		return nil
	}
	g.syncSourceListsFromFields()
	return g.SourceLists.SourceACL()
}

func (g *Glob) RefreshSourceLists(owner string) {
	if g == nil {
		return
	}
	g.syncSourceListsFromFields()
	g.SourceLists.RefreshSourceLists(owner)
}

func (g *Glob) CompileSourceLists(scope acl.Scope, owner string) {
	if g == nil {
		return
	}
	g.syncSourceListsFromFields()
	g.SourceLists.CompileSourceLists(scope, owner)
}

func (l *SourceLists) SetSourceLists(priority int, black, white []string, scope acl.Scope, owner string) {
	if l == nil {
		return
	}
	l.aclMu.Lock()
	l.IpListPriority = acl.NormalizePriority(priority)
	l.BlackIpList = append([]string(nil), black...)
	l.WhiteIpList = append([]string(nil), white...)
	l.aclMu.Unlock()
	l.CompileSourceLists(scope, owner)
}

func (l *SourceLists) CopySourceLists(other *SourceLists) {
	if l == nil || other == nil {
		return
	}
	other.aclMu.RLock()
	l.aclMu.Lock()
	l.IpListPriority = other.IpListPriority
	l.BlackIpList = append([]string(nil), other.BlackIpList...)
	l.WhiteIpList = append([]string(nil), other.WhiteIpList...)
	compiled, source := other.compiled.Load(), other.sourceACL
	l.sourceACL = source
	l.compiled.Store(compiled)
	l.aclMu.Unlock()
	other.aclMu.RUnlock()
}

func (l *SourceLists) CompileSourceLists(scope acl.Scope, owner string) {
	if l == nil {
		return
	}
	l.aclMu.RLock()
	priority := l.IpListPriority
	black := append([]string(nil), l.BlackIpList...)
	white := append([]string(nil), l.WhiteIpList...)
	l.aclMu.RUnlock()
	source, err := acl.CompileSource(scope, priority, black, white)
	if err != nil {
		logs.Warn("Access lists of %s: policy disabled because validation failed: %v", owner, err)
	}
	l.aclMu.Lock()
	l.sourceACL = source
	l.compiled.Store(true)
	l.aclMu.Unlock()
}

func (l *SourceLists) SourceACL() *acl.Source {
	if l == nil {
		return nil
	}
	if !l.compiled.Load() {
		l.CompileSourceLists(acl.ScopeUser, "entry")
	}
	l.aclMu.RLock()
	source := l.sourceACL
	l.aclMu.RUnlock()
	return source
}

// AccessListSnapshot returns a stable copy of the raw source access lists.
// Callers can preserve policies when updating a record from an older form or
// API client that does not submit the newer ACL fields.
func (l *SourceLists) AccessListSnapshot() (priority int, black, white []string) {
	if l == nil {
		return
	}
	l.aclMu.RLock()
	priority = l.IpListPriority
	black = append([]string(nil), l.BlackIpList...)
	white = append([]string(nil), l.WhiteIpList...)
	l.aclMu.RUnlock()
	return
}

// RefreshSourceLists recompiles a policy after an optional geodata update.
// Invalid or unavailable rules remain fail-closed until the configuration is
// corrected or the external data becomes available again.
func (l *SourceLists) RefreshSourceLists(owner string) {
	if l == nil {
		return
	}
	l.CompileSourceLists(acl.ScopeUser, owner)
}

// GlobalDenied evaluates the global policy for an incoming listener address.
func (s *DbUtils) GlobalDenied(addr net.Addr, what string) bool {
	if s == nil || s.JsonDb == nil {
		return false
	}
	global := s.GetGlobal()
	if global == nil {
		return false
	}
	if source := global.SourceACL(); source != nil && !source.Allows(addr) {
		logs.Warn("%s access from %v denied by global access list", what, addr)
		return true
	}
	return false
}

func (s *DbUtils) GlobalDeniedString(addr, what string) bool {
	if s == nil || s.JsonDb == nil {
		return false
	}
	global := s.GetGlobal()
	if global == nil {
		return false
	}
	if source := global.SourceACL(); source != nil && !source.AllowsString(addr) {
		logs.Warn("%s access from %s denied by global access list", what, addr)
		return true
	}
	return false
}

// RefreshAccessLists refreshes all runtime-compiled policies. It is safe to
// call after a Web edit and is intentionally cheap for deployments that only
// use the legacy blacklist fields.
func (s *DbUtils) RefreshAccessLists() {
	if s == nil || s.JsonDb == nil {
		return
	}
	if global := s.GetGlobal(); global != nil {
		global.RefreshSourceLists("global")
	}
	s.JsonDb.Clients.Range(func(_, value interface{}) bool {
		if c, ok := value.(*Client); ok && c != nil {
			c.RefreshSourceLists("client")
		}
		return true
	})
	s.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		if t, ok := value.(*Tunnel); ok && t != nil {
			t.RefreshSourceLists("tunnel")
		}
		return true
	})
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		if h, ok := value.(*Host); ok && h != nil {
			h.RefreshSourceLists("host")
		}
		return true
	})
}

// WatchAccessLists is kept as a small lifecycle helper for callers that want
// a long-running refresh loop. It does not read any files when no external
// geodata provider is configured.
func WatchAccessLists(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			acl.ReloadGeoData()
			if db := GetDb(); db != nil {
				db.RefreshAccessLists()
			}
		}
	}
}

func sourceListSnapshot(l *SourceLists) (priority int, black, white []string) {
	if l == nil {
		return
	}
	l.aclMu.RLock()
	priority = l.IpListPriority
	black = append([]string(nil), l.BlackIpList...)
	white = append([]string(nil), l.WhiteIpList...)
	l.aclMu.RUnlock()
	return
}

func SplitSourceListText(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '\r' || r == '\n' || r == ',' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
