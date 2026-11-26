package file

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

const platformPrefixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// GeneratePlatformPrefix returns a random single-label prefix for a platform
// wildcard host. The fixed alphabet keeps the result inside the DNS label
// charset and the fixed length keeps it readable when shown to a user.
func GeneratePlatformPrefix() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "npshost1"
	}
	for i, value := range buf {
		buf[i] = platformPrefixAlphabet[int(value)%len(platformPrefixAlphabet)]
	}
	return string(buf)
}

// PickPlatformDomain resolves the managed wildcard used for a new host.
// reference accepts either the stable ID or the wildcard itself; an empty
// reference picks the first domain that can serve the requested scheme.
func (s *DbUtils) PickPlatformDomain(reference, scheme string) (*PlatformDomain, error) {
	domains := s.GetUsablePlatformDomains()
	if len(domains) == 0 {
		return nil, errors.New("服务端未配置可用的平台域名，请联系管理员在「全局参数 → 证书设置」中添加")
	}
	reference = strings.TrimSpace(reference)
	if reference != "" {
		for i := range domains {
			if strings.EqualFold(domains[i].ID, reference) || strings.EqualFold(domains[i].Wildcard, reference) {
				domain := domains[i]
				return &domain, nil
			}
		}
		return nil, fmt.Errorf("未找到可用的平台域名 %q", reference)
	}
	needsCertificate := !strings.EqualFold(strings.TrimSpace(scheme), "http")
	for i := range domains {
		if !needsCertificate || platformDomainHasCertificate(domains[i]) {
			domain := domains[i]
			return &domain, nil
		}
	}
	return nil, fmt.Errorf("平台域名 %s 未配置证书，只能创建 HTTP 规则", domains[0].Wildcard)
}

func platformDomainHasCertificate(domain PlatformDomain) bool {
	return strings.TrimSpace(domain.CertFilePath) != "" && strings.TrimSpace(domain.KeyFilePath) != ""
}

// AllocatePlatformHost builds a hostname from a platform wildcard and a
// single-label prefix. An empty prefix allocates a random one and retries when
// the candidate is already used on the same path rule.
func (s *DbUtils) AllocatePlatformHost(domainRef, prefix, location, scheme string, excludeHostID int) (string, string, error) {
	domain, err := s.PickPlatformDomain(domainRef, scheme)
	if err != nil {
		return "", "", err
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix != "" {
		host, err := s.ResolvePlatformHost(domain.ID, prefix)
		if err != nil {
			return "", "", err
		}
		available, err := s.IsPlatformHostAvailable(domain.ID, prefix, location, scheme, excludeHostID)
		if err != nil {
			return "", "", err
		}
		if !available {
			return "", "", fmt.Errorf("平台域名 %s 与当前路径规则已被使用", host)
		}
		return host, domain.ID, nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		candidate := GeneratePlatformPrefix()
		host, err := s.ResolvePlatformHost(domain.ID, candidate)
		if err != nil {
			continue
		}
		available, err := s.IsPlatformHostAvailable(domain.ID, candidate, location, scheme, excludeHostID)
		if err != nil {
			return "", "", err
		}
		if available {
			return host, domain.ID, nil
		}
	}
	return "", "", errors.New("无法生成可用的平台子域名，请指定前缀后重试")
}
