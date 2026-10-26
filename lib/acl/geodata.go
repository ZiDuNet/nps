package acl

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The upstream data files are intentionally treated as an external provider:
// text exports are supported directly (category,cidr and category,domain),
// while binary files that cannot be decoded safely are ignored with a closed
// policy. Deployments can therefore update data without changing JSON state.
var geoData struct {
	sync.RWMutex
	ip      map[string][]netip.Prefix
	site    map[string][]geoDomain
	path    string
	ipMod   time.Time
	siteMod time.Time
}

type geoDomain struct {
	kind  uint64
	value string
}

func SetGeoData(ipFile, siteFile string) {
	ipFile, siteFile = normalizeGeoPath(ipFile), normalizeGeoPath(siteFile)
	path := ""
	if ipFile != "" || siteFile != "" {
		path = ipFile + "\x00" + siteFile
	}

	ip, ipMod, ipErr := loadGeoIPFile(ipFile)
	site, siteMod, siteErr := loadGeoSiteFile(siteFile)
	geoData.Lock()
	if geoData.path != path {
		// A new configuration must not inherit entries compiled from the old
		// paths. If a configured file is temporarily unavailable, its policy is
		// empty until the first complete version can be loaded.
		geoData.ip, geoData.site = nil, nil
		geoData.ipMod, geoData.siteMod = time.Time{}, time.Time{}
	}
	geoData.path = path
	if ipFile == "" {
		geoData.ip, geoData.ipMod = nil, time.Time{}
	} else if ipErr == nil {
		geoData.ip, geoData.ipMod = ip, ipMod
	}
	if siteFile == "" {
		geoData.site, geoData.siteMod = nil, time.Time{}
	} else if siteErr == nil {
		geoData.site, geoData.siteMod = site, siteMod
	}
	geoData.Unlock()
}

// ReloadGeoData rereads the configured files. It is deliberately cheap enough
// for the minute-level access-list watcher and keeps old compiled entries
// safe when an update is partial or malformed.
func ReloadGeoData() {
	geoData.RLock()
	path := geoData.path
	ipMod, siteMod := geoData.ipMod, geoData.siteMod
	geoData.RUnlock()
	if path == "" {
		return
	}
	parts := strings.SplitN(path, "\x00", 2)
	if len(parts) != 2 {
		return
	}
	ipPath, sitePath := parts[0], parts[1]
	currentIPMod, currentSiteMod := fileModTime(ipPath), fileModTime(sitePath)
	ipChanged, siteChanged := !currentIPMod.Equal(ipMod), !currentSiteMod.Equal(siteMod)
	if !ipChanged && !siteChanged {
		return
	}

	var ip map[string][]netip.Prefix
	var nextIPMod time.Time
	var ipErr error
	if ipChanged && ipPath != "" {
		ip, nextIPMod, ipErr = loadGeoIPFile(ipPath)
	}
	var site map[string][]geoDomain
	var nextSiteMod time.Time
	var siteErr error
	if siteChanged && sitePath != "" {
		site, nextSiteMod, siteErr = loadGeoSiteFile(sitePath)
	}

	geoData.Lock()
	defer geoData.Unlock()
	// A concurrent reconfiguration wins over a slow read from the old paths.
	if geoData.path != path {
		return
	}
	if ipChanged && ipErr == nil {
		geoData.ip, geoData.ipMod = ip, nextIPMod
	}
	if siteChanged && siteErr == nil {
		geoData.site, geoData.siteMod = site, nextSiteMod
	}
}

func normalizeGeoPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func fileModTime(path string) time.Time {
	if strings.TrimSpace(path) == "" {
		return time.Time{}
	}
	info, err := os.Stat(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func GeoDataConfigured() bool {
	geoData.RLock()
	configured := len(geoData.ip) > 0 || len(geoData.site) > 0
	geoData.RUnlock()
	return configured
}

// GeoFiles returns the configured GeoIP and GeoSite file paths. Both may be
// empty when geodata is disabled.
func GeoFiles() (ipPath, sitePath string) {
	geoData.RLock()
	path := geoData.path
	geoData.RUnlock()
	if path == "" {
		return "", ""
	}
	parts := strings.SplitN(path, "\x00", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func geoCategoryExists(kind, category string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" {
		return false
	}
	if kind == "geoip" && category == "private" {
		return true
	}
	geoData.RLock()
	defer geoData.RUnlock()
	switch kind {
	case "geoip":
		return len(geoData.ip[category]) > 0
	case "geosite":
		return len(geoData.site[category]) > 0
	default:
		return false
	}
}

func geoPrefixes(category string) []netip.Prefix {
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "private" {
		return []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
			netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"),
		}
	}
	geoData.RLock()
	result := append([]netip.Prefix(nil), geoData.ip[category]...)
	geoData.RUnlock()
	return result
}

func geoSiteMatches(category, host string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	geoData.RLock()
	domains := append([]geoDomain(nil), geoData.site[category]...)
	geoData.RUnlock()
	for _, domain := range domains {
		domain.value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain.value)), ".")
		matched := false
		switch domain.kind {
		case 0: // plain keyword
			matched = strings.Contains(host, domain.value)
		case 1: // regular expression
			if compiled, err := regexp.Compile(domain.value); err == nil {
				matched = compiled.MatchString(host)
			}
		case 2: // domain suffix
			matched = host == domain.value || strings.HasSuffix(host, "."+strings.TrimPrefix(domain.value, "."))
		case 3: // full
			matched = host == domain.value
		default:
			matched = host == domain.value || strings.HasSuffix(host, "."+strings.TrimPrefix(domain.value, "."))
		}
		if domain.value != "" && matched {
			return true
		}
	}
	return false
}

func loadGeoIPFile(path string) (map[string][]netip.Prefix, time.Time, error) {
	if path == "" {
		return nil, time.Time{}, nil
	}
	data, mod, err := readStableGeoFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if decoded, parseErr := parseGeoIPProto(data); parseErr == nil {
		return decoded, mod, nil
	}
	result := make(map[string][]netip.Prefix)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		parts := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == '\t' || r == ' ' })
		if len(parts) < 2 {
			return nil, time.Time{}, errors.New("invalid GeoIP text entry on line " + strconv.Itoa(lineNumber))
		}
		category := strings.ToLower(strings.TrimSpace(parts[0]))
		if category == "" {
			return nil, time.Time{}, errors.New("empty GeoIP category on line " + strconv.Itoa(lineNumber))
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(parts[1]))
		if err != nil {
			if addr, addrErr := netip.ParseAddr(strings.TrimSpace(parts[1])); addrErr == nil {
				bits := 128
				if addr.Is4() {
					bits = 32
				}
				prefix = netip.PrefixFrom(addr, bits)
			} else {
				return nil, time.Time{}, errors.New("invalid GeoIP prefix on line " + strconv.Itoa(lineNumber))
			}
		}
		result[category] = append(result[category], prefix.Masked())
	}
	if err := scanner.Err(); err != nil {
		return nil, time.Time{}, err
	}
	if len(result) == 0 {
		return nil, time.Time{}, errors.New("GeoIP file has no entries")
	}
	return result, mod, nil
}

func loadGeoSiteFile(path string) (map[string][]geoDomain, time.Time, error) {
	if path == "" {
		return nil, time.Time{}, nil
	}
	data, mod, err := readStableGeoFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if decoded, parseErr := parseGeoSiteProto(data); parseErr == nil {
		return decoded, mod, nil
	}
	result := make(map[string][]geoDomain)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		parts := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == '\t' || r == ' ' })
		if len(parts) < 2 {
			return nil, time.Time{}, errors.New("invalid Geosite text entry on line " + strconv.Itoa(lineNumber))
		}
		category := strings.ToLower(strings.TrimSpace(parts[0]))
		if category == "" {
			return nil, time.Time{}, errors.New("empty Geosite category on line " + strconv.Itoa(lineNumber))
		}
		for _, domain := range parts[1:] {
			domain = strings.TrimSpace(domain)
			if domain == "" {
				return nil, time.Time{}, errors.New("empty Geosite domain on line " + strconv.Itoa(lineNumber))
			}
			result[category] = append(result[category], geoDomain{kind: 2, value: domain})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, time.Time{}, err
	}
	if len(result) == 0 {
		return nil, time.Time{}, errors.New("Geosite file has no entries")
	}
	return result, mod, nil
}

func readStableGeoFile(path string) ([]byte, time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !before.Mode().IsRegular() {
		return nil, time.Time{}, errors.New("Geo data path is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(data) == 0 {
		return nil, time.Time{}, errors.New("Geo data file is empty")
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || int64(len(data)) != after.Size() {
		return nil, time.Time{}, errors.New("Geo data file changed while being read")
	}
	return data, after.ModTime(), nil
}

func geoSiteRuleMatches(rule, host string) bool {
	return geoSiteMatches(rule, host)
}

func geoIPRuleMatches(rule string, addr netip.Addr) bool {
	negative := strings.HasPrefix(rule, "!")
	if negative {
		rule = strings.TrimPrefix(rule, "!")
	}
	matched := false
	for _, prefix := range geoPrefixes(rule) {
		if prefix.Contains(addr.Unmap()) {
			matched = true
			break
		}
	}
	return matched != negative
}

// The v2ray-rules-dat files use a small protobuf schema. These decoders read
// only the GeoIPList/GeoSiteList fields needed by NPS and reject malformed or
// unsupported wire fields instead of interpreting arbitrary binary as text.
func parseGeoIPProto(data []byte) (map[string][]netip.Prefix, error) {
	result := make(map[string][]netip.Prefix)
	fields, err := protoFields(data)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if field.number != 1 || field.wire != 2 {
			continue
		}
		country, cidrs, err := parseGeoIPEntry(field.bytes)
		if err != nil {
			return nil, err
		}
		result[strings.ToLower(country)] = append(result[strings.ToLower(country)], cidrs...)
	}
	if len(result) == 0 {
		return nil, errors.New("no GeoIP entries found")
	}
	return result, nil
}

func parseGeoIPEntry(data []byte) (string, []netip.Prefix, error) {
	fields, err := protoFields(data)
	if err != nil {
		return "", nil, err
	}
	var country string
	var prefixes []netip.Prefix
	for _, field := range fields {
		switch {
		case field.number == 1 && field.wire == 2:
			country = string(field.bytes)
		case field.number == 2 && field.wire == 2:
			if prefix, ok := parseGeoIPCIDR(field.bytes); ok {
				prefixes = append(prefixes, prefix)
			}
		}
	}
	if country == "" || len(prefixes) == 0 {
		return "", nil, errors.New("incomplete GeoIP entry")
	}
	return country, prefixes, nil
}

func parseGeoIPCIDR(data []byte) (netip.Prefix, bool) {
	fields, err := protoFields(data)
	if err != nil {
		return netip.Prefix{}, false
	}
	var ip netip.Addr
	var bits uint64
	for _, field := range fields {
		switch {
		case field.number == 1 && field.wire == 2:
			ip, _ = netip.AddrFromSlice(field.bytes)
		case field.number == 2 && field.wire == 0:
			bits = field.value
		}
	}
	if !ip.IsValid() || bits > uint64(ip.BitLen()) {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(ip.Unmap(), int(bits)).Masked(), true
}

func parseGeoSiteProto(data []byte) (map[string][]geoDomain, error) {
	result := make(map[string][]geoDomain)
	fields, err := protoFields(data)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		if field.number != 1 || field.wire != 2 {
			continue
		}
		category, domains, err := parseGeoSiteEntry(field.bytes)
		if err != nil {
			return nil, err
		}
		result[strings.ToLower(category)] = append(result[strings.ToLower(category)], domains...)
	}
	if len(result) == 0 {
		return nil, errors.New("no Geosite entries found")
	}
	return result, nil
}

func parseGeoSiteEntry(data []byte) (string, []geoDomain, error) {
	fields, err := protoFields(data)
	if err != nil {
		return "", nil, err
	}
	var category string
	var domains []geoDomain
	for _, field := range fields {
		switch {
		case field.number == 1 && field.wire == 2:
			category = string(field.bytes)
		case field.number == 2 && field.wire == 2:
			if domain, ok := parseGeoSiteDomain(field.bytes); ok {
				domains = append(domains, domain)
			}
		}
	}
	if category == "" || len(domains) == 0 {
		return "", nil, errors.New("incomplete Geosite entry")
	}
	return category, domains, nil
}

func parseGeoSiteDomain(data []byte) (geoDomain, bool) {
	fields, err := protoFields(data)
	if err != nil {
		return geoDomain{}, false
	}
	var domain geoDomain
	for _, field := range fields {
		switch {
		case field.number == 1 && field.wire == 0:
			domain.kind = field.value
		case field.number == 2 && field.wire == 2:
			domain.value = string(field.bytes)
		}
	}
	return domain, domain.value != ""
}

type protoField struct {
	number int
	wire   byte
	value  uint64
	bytes  []byte
}

func protoFields(data []byte) ([]protoField, error) {
	var result []protoField
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errors.New("invalid protobuf tag")
		}
		data = data[n:]
		field := protoField{number: int(tag >> 3), wire: byte(tag & 7)}
		if field.number <= 0 {
			return nil, errors.New("invalid protobuf field number")
		}
		switch field.wire {
		case 0:
			field.value, n = binary.Uvarint(data)
			if n <= 0 {
				return nil, errors.New("invalid protobuf varint")
			}
			data = data[n:]
		case 1:
			if len(data) < 8 {
				return nil, errors.New("truncated protobuf fixed64")
			}
			data = data[8:]
		case 2:
			var size uint64
			size, n = binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data)-n) {
				return nil, errors.New("truncated protobuf bytes")
			}
			field.bytes = data[n : n+int(size)]
			data = data[n+int(size):]
		case 5:
			if len(data) < 4 {
				return nil, errors.New("truncated protobuf fixed32")
			}
			data = data[4:]
		default:
			return nil, errors.New("unsupported protobuf wire type")
		}
		result = append(result, field)
	}
	return result, nil
}
