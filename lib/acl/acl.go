// Package acl contains the dependency-free access-list evaluator used by the
// server. It intentionally accepts the simple IP/CIDR syntax used by older
// NPS releases and the richer source/destination syntax supported by this release.
// Invalid or unsupported rules are reported to the caller and never broaden
// an allow-list accidentally.
package acl

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
)

// Scope identifies who is allowed to write a rule list.  Global lists may be
// extended by an administrator; user lists are bounded by MaxRules.
type Scope uint8

const (
	ScopeGlobal Scope = iota
	ScopeUser
)

const (
	DenyFirst  = 0
	AllowFirst = 1
)

const DefaultMaxRules = 1000

var maxRules = DefaultMaxRules

// SetMaxRules changes the maximum number of rules accepted in user-owned
// lists. A value of zero disables the limit.
func SetMaxRules(n int) {
	if n < 0 {
		n = 0
	}
	maxRules = n
}

// NormalizePriority maps unknown values to the historical deny-first mode.
func NormalizePriority(priority int) int {
	if priority == AllowFirst {
		return AllowFirst
	}
	return DenyFirst
}

// RuleError reports malformed rules. Compilers return the error alongside a
// fail-closed policy so callers can log or reject invalid persisted input.
type RuleError struct {
	Errors []error
}

func (e *RuleError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return strings.Join(parts, "; ")
}

func (e *RuleError) Unwrap() []error { return e.Errors }

// Source is a compiled source (visitor IP) policy.
type Source struct {
	priority int
	black    []netip.Prefix
	white    []netip.Prefix
	hasBlack bool
	hasWhite bool
	invalid  bool
	mu       sync.RWMutex
}

// CompileSource builds an IP/CIDR source policy. Host names and Geosite rules
// are rejected deliberately: source policy is evaluated before an HTTP request
// exists, so accepting a DNS name would make the policy unstable. GeoIP rules
// are supported when the corresponding dataset is configured.
func CompileSource(scope Scope, priority int, black, white []string) (*Source, error) {
	blackLines, whiteLines := splitLines(black), splitLines(white)
	if !hasActiveRule(blackLines) && !hasActiveRule(whiteLines) {
		return nil, nil
	}
	s := &Source{
		priority: NormalizePriority(priority),
		hasBlack: hasActiveRule(blackLines),
		hasWhite: hasActiveRule(whiteLines),
	}
	var errs []error
	s.black, errs = compileIPRules(scope, "black", blackLines, errs)
	s.white, errs = compileIPRules(scope, "white", whiteLines, errs)
	if len(errs) == 0 {
		return s, nil
	}
	// Persisted policies are untrusted input too. If any configured entry is
	// invalid, deny the entire policy rather than accidentally broadening a
	// blacklist or weakening an allowlist by skipping just the bad entry.
	s.invalid = true
	return s, &RuleError{Errors: errs}
}

func hasActiveRule(lines []string) bool {
	for _, raw := range lines {
		rule := strings.TrimSpace(raw)
		if rule != "" && !strings.HasPrefix(rule, "#") && !strings.HasPrefix(rule, ";") {
			return true
		}
	}
	return false
}

func compileIPRules(scope Scope, name string, rules []string, errs []error) ([]netip.Prefix, []error) {
	lines := splitLines(rules)
	count := activeRuleCount(lines)
	if scope == ScopeUser && maxRules > 0 && count > maxRules {
		errs = append(errs, fmt.Errorf("%s list has %d rules, maximum is %d", name, count, maxRules))
		return nil, errs
	}
	seen := make(map[string]struct{}, len(lines))
	out := make([]netip.Prefix, 0, len(lines))
	for index, raw := range lines {
		rule := strings.TrimSpace(raw)
		if rule == "" || strings.HasPrefix(rule, "#") || strings.HasPrefix(rule, ";") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(rule), "geoip:") {
			category := strings.TrimSpace(rule[len("geoip:"):])
			prefixes := geoPrefixes(category)
			if len(prefixes) == 0 {
				errs = append(errs, fmt.Errorf("%s rule %d %q requires geoip data", name, index+1, rule))
				continue
			}
			for _, prefix := range prefixes {
				key := prefix.String()
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, prefix)
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(rule), "geosite:") {
			errs = append(errs, fmt.Errorf("%s rule %d %q is only valid for destination/domain rules", name, index+1, rule))
			continue
		}
		prefix, err := parseIPPrefix(rule)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s rule %d %q: %w", name, index+1, rule, err))
			continue
		}
		key := prefix.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, prefix)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Bits() < out[j].Bits() || (out[i].Bits() == out[j].Bits() && out[i].String() < out[j].String())
	})
	return out, errs
}

func activeRuleCount(lines []string) int {
	count := 0
	for _, raw := range lines {
		rule := strings.TrimSpace(raw)
		if rule == "" || strings.HasPrefix(rule, "#") || strings.HasPrefix(rule, ";") {
			continue
		}
		count++
	}
	return count
}

func splitLines(rules []string) []string {
	var lines []string
	for _, raw := range rules {
		lines = append(lines, strings.FieldsFunc(raw, func(r rune) bool { return r == '\r' || r == '\n' || r == ',' })...)
	}
	return lines
}

func parseIPPrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Prefix{}, errors.New("empty rule")
	}
	// Strip an optional port from host:port and [v6]:port forms.
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	}
	if strings.Contains(raw, "/") {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(strings.Trim(raw, "[]"))
	if err != nil {
		return netip.Prefix{}, err
	}
	if addr.Is4() {
		return netip.PrefixFrom(addr, 32), nil
	}
	return netip.PrefixFrom(addr, 128), nil
}

func addressOf(addr net.Addr) (netip.Addr, bool) {
	if addr == nil {
		return netip.Addr{}, false
	}
	text := strings.TrimSpace(addr.String())
	if host, _, err := net.SplitHostPort(text); err == nil {
		text = host
	} else {
		text = strings.Trim(text, "[]")
	}
	parsed, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Addr{}, false
	}
	return parsed.Unmap(), true
}

func addressFromString(text string) (netip.Addr, bool) {
	return addressOf(stringAddr(text))
}

type stringAddr string

func (s stringAddr) Network() string { return "tcp" }
func (s stringAddr) String() string  { return string(s) }

func (s *Source) matches(addr netip.Addr) (black, white bool) {
	if s == nil {
		return false, false
	}
	s.mu.RLock()
	for _, rule := range s.black {
		if rule.Contains(addr) {
			black = true
			break
		}
	}
	for _, rule := range s.white {
		if rule.Contains(addr) {
			white = true
			break
		}
	}
	s.mu.RUnlock()
	return black, white
}

// Allows evaluates one level. Nil means no policy and therefore allows.
func (s *Source) Allows(addr net.Addr) bool {
	parsed, ok := addressOf(addr)
	if !ok {
		return false
	}
	return s.allowsAddr(parsed)
}

func (s *Source) allowsAddr(addr netip.Addr) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	hasBlack, hasWhite := s.hasBlack, s.hasWhite
	priority := s.priority
	invalid := s.invalid
	s.mu.RUnlock()
	if invalid {
		return false
	}
	black, white := s.matches(addr)
	switch {
	case !hasBlack && !hasWhite:
		return true
	case !hasBlack:
		return white
	case !hasWhite:
		return !black
	case priority == AllowFirst:
		return white || !black
	default:
		return !black && white
	}
}

// AllowsString evaluates an address represented as host:port or an IP.
func (s *Source) AllowsString(addr string) bool {
	parsed, ok := addressFromString(addr)
	return ok && s.allowsAddr(parsed)
}

// Decide contains the level and matched rule useful for diagnostic logs.
type Decision struct {
	Allowed bool
	Level   int
	Name    string
	Rule    string
}

func (d Decision) String() string {
	status := "allowed"
	if !d.Allowed {
		status = "denied"
	}
	if d.Name == "" {
		return status
	}
	if d.Rule == "" {
		return fmt.Sprintf("%s (%s)", status, d.Name)
	}
	return fmt.Sprintf("%s (%s: %s)", status, d.Name, d.Rule)
}

// Allows applies all levels in order. Every configured level must allow.
func Allows(addr net.Addr, global, client, entry *Source) bool {
	if global != nil && !global.Allows(addr) {
		return false
	}
	if client != nil && !client.Allows(addr) {
		return false
	}
	return entry == nil || entry.Allows(addr)
}

func AllowsString(addr string, global, client, entry *Source) bool {
	if global != nil && !global.AllowsString(addr) {
		return false
	}
	if client != nil && !client.AllowsString(addr) {
		return false
	}
	return entry == nil || entry.AllowsString(addr)
}

func Decide(addr net.Addr, names []string, global, client, entry *Source) Decision {
	levels := []*Source{global, client, entry}
	for i, source := range levels {
		if source == nil || source.Allows(addr) {
			continue
		}
		name := ""
		if i < len(names) {
			name = names[i]
		}
		return Decision{Allowed: false, Level: i, Name: name}
	}
	return Decision{Allowed: true, Level: -1}
}

func DecideString(addr string, names []string, global, client, entry *Source) Decision {
	levels := []*Source{global, client, entry}
	for i, source := range levels {
		if source == nil || source.AllowsString(addr) {
			continue
		}
		name := ""
		if i < len(names) {
			name = names[i]
		}
		return Decision{Allowed: false, Level: i, Name: name}
	}
	return Decision{Allowed: true, Level: -1}
}

// NormalizeSource returns canonical IP/CIDR rules and reports rejected lines.
func NormalizeSource(scope Scope, rules []string) ([]string, error) {
	src, err := CompileSource(scope, DenyFirst, rules, nil)
	if src == nil {
		return nil, err
	}
	src.mu.RLock()
	result := make([]string, 0, len(src.black)+len(src.white))
	for _, p := range src.black {
		result = append(result, p.String())
	}
	for _, p := range src.white {
		result = append(result, p.String())
	}
	src.mu.RUnlock()
	sort.Strings(result)
	return result, err
}

// Dest evaluates optional destination ACL rules for proxy-capable tunnels.
type Dest struct {
	mode            int
	rules           []string
	geoRequirements []geoRequirement
	invalid         bool
}

type geoRequirement struct {
	kind     string
	category string
}

func CompileDest(mode int, rules string) (*Dest, error) {
	if mode != 1 && mode != 2 {
		return nil, nil
	}
	lines := splitLines([]string{rules})
	count := activeRuleCount(lines)
	dest := &Dest{mode: mode}
	var errs []error
	if maxRules > 0 && count > maxRules {
		errs = append(errs, fmt.Errorf("destination list has %d rules, maximum is %d", count, maxRules))
		dest.invalid = true
		return dest, joinErrors(errs)
	}
	valid := make([]string, 0, len(lines))
	var geoRequirements []geoRequirement
	for index, raw := range lines {
		rule := strings.ToLower(strings.TrimSpace(raw))
		if rule == "" || strings.HasPrefix(rule, "#") || strings.HasPrefix(rule, ";") {
			continue
		}
		if !validDestinationRule(rule) {
			errs = append(errs, fmt.Errorf("destination rule %d %q is invalid", index+1, raw))
			continue
		}
		valid = append(valid, rule)
		if requirement, ok := destinationGeoRequirement(rule); ok {
			geoRequirements = append(geoRequirements, requirement)
		}
	}
	dest.rules = valid
	dest.geoRequirements = geoRequirements
	if len(errs) > 0 {
		dest.invalid = true
	}
	return dest, joinErrors(errs)
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return &RuleError{Errors: errs}
}

func validDestinationRule(rule string) bool {
	if strings.HasPrefix(rule, "geoip:") || strings.HasPrefix(rule, "geosite:") {
		requirement, ok := destinationGeoRequirement(rule)
		return ok && geoCategoryExists(requirement.kind, requirement.category)
	}
	if !strings.ContainsAny(rule, ".:/\\ \t") {
		// Bare destination tokens are keyword matches.
		return rule != ""
	}
	if index := strings.IndexByte(rule, ':'); index > 0 {
		kind := rule[:index]
		switch kind {
		case "full", "domain", "keyword":
			value := strings.TrimSpace(rule[index+1:])
			return value != "" && !strings.ContainsAny(value, " \t/\\")
		default:
			// Address literals such as IPv6 may contain colons, but an
			// unrecognized rule-kind prefix should not silently become a broad
			// substring match.
			if _, err := parseIPPrefix(rule); err != nil {
				return false
			}
		}
	}
	if rule == "" {
		return false
	}
	if _, err := parseIPPrefix(rule); err == nil {
		return true
	}
	return strings.ContainsAny(rule, ".*_-") && !strings.ContainsAny(rule, " \t/\\")
}

func destinationGeoRequirement(rule string) (geoRequirement, bool) {
	index := strings.IndexByte(rule, ':')
	if index <= 0 {
		return geoRequirement{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(rule[:index]))
	category := strings.ToLower(strings.TrimSpace(rule[index+1:]))
	if kind != "geoip" && kind != "geosite" {
		return geoRequirement{}, false
	}
	if kind == "geoip" && strings.HasPrefix(category, "!") {
		category = strings.TrimSpace(strings.TrimPrefix(category, "!"))
	}
	if category == "" || strings.ContainsAny(category, " \t\r\n,;|") || strings.HasPrefix(category, "!") {
		return geoRequirement{}, false
	}
	return geoRequirement{kind: kind, category: category}, true
}

func hostPart(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	if host, _, err := net.SplitHostPort(text); err == nil {
		text = host
	}
	return strings.TrimSuffix(strings.Trim(text, "[]"), ".")
}

func (d *Dest) Allows(addr string) bool {
	if d == nil || d.mode == 0 {
		return true
	}
	if d.invalid {
		return false
	}
	for _, requirement := range d.geoRequirements {
		if !geoCategoryExists(requirement.kind, requirement.category) {
			return false
		}
	}
	host := hostPart(addr)
	matched := false
	for _, raw := range d.rules {
		rule := raw
		kind := ""
		if index := strings.IndexByte(rule, ':'); index > 0 {
			kind, rule = rule[:index], rule[index+1:]
		}
		if strings.HasPrefix(raw, "geosite:") {
			matched = geoSiteRuleMatches(strings.TrimPrefix(raw, "geosite:"), host)
			if matched {
				break
			}
			continue
		}
		if kind == "geoip" {
			if addr, ok := addressFromString(host); ok && geoIPRuleMatches(rule, addr) {
				matched = true
				break
			}
			continue
		}
		if prefix, err := parseIPPrefix(host); err == nil {
			if prefix.Addr().String() == rule || (strings.Contains(rule, "/") && prefixMatches(rule, prefix.Addr())) {
				matched = true
				break
			}
		} else {
			switch kind {
			case "full":
				matched = host == rule
			case "domain":
				matched = host == rule || strings.HasSuffix(host, "."+strings.TrimPrefix(rule, "."))
			case "keyword":
				matched = strings.Contains(host, rule)
			default:
				if strings.HasPrefix(rule, "*.") {
					suffix := strings.TrimPrefix(rule, "*.")
					matched = host != suffix && strings.HasSuffix(host, "."+suffix)
				} else if strings.HasPrefix(rule, "*") {
					domain := strings.TrimPrefix(rule, "*")
					domain = strings.TrimPrefix(domain, ".")
					matched = host == domain || strings.HasSuffix(host, "."+domain)
				} else {
					// Bare destination names use keyword matching; use full: or
					// domain: for narrower matching.
					matched = strings.Contains(host, rule)
				}
			}
			if matched {
				break
			}
		}
	}
	if d.mode == 1 {
		return matched
	}
	return !matched
}

func prefixMatches(rule string, addr netip.Addr) bool {
	prefix, err := netip.ParsePrefix(rule)
	return err == nil && prefix.Contains(addr)
}

func NormalizeDest(raw string) (string, error) {
	d, err := CompileDest(1, raw)
	if d == nil {
		return "", err
	}
	return strings.Join(d.rules, "\n"), err
}
