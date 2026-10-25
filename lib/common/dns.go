package common

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
)

var (
	systemDNSResolver = net.DefaultResolver
	dnsServerIndex    atomic.Uint64
)

// SetCustomDNS configures the process resolver to use one or more explicit
// DNS servers. An empty value restores Go's original system resolver. DNS
// server hostnames are resolved through that original resolver to avoid a
// recursive lookup loop.
func SetCustomDNS(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		net.DefaultResolver = systemDNSResolver
		return nil
	}

	servers := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		server, err := normalizeDNSServer(item)
		if err != nil {
			// A malformed new setting must not leave a resolver installed by a
			// previous configuration active. Callers log this error and continue
			// with the system resolver.
			net.DefaultResolver = systemDNSResolver
			return err
		}
		servers = append(servers, server)
	}
	if len(servers) == 0 {
		net.DefaultResolver = systemDNSResolver
		return fmt.Errorf("DNS server list is empty")
	}

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			start := int(dnsServerIndex.Add(1)-1) % len(servers)
			var lastErr error
			for offset := range servers {
				server := servers[(start+offset)%len(servers)]
				dialer := &net.Dialer{Resolver: systemDNSResolver}
				connection, err := dialer.DialContext(ctx, network, server)
				if err == nil {
					return connection, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
	return nil
}

func normalizeDNSServer(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
			host, port = ip.String(), "53"
		} else if !strings.Contains(value, ":") {
			host, port = value, "53"
		} else {
			return "", fmt.Errorf("invalid DNS server %q: use IP[:port] or hostname[:port]", value)
		}
	}
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("invalid DNS server %q: host is empty", value)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid DNS server %q: port must be between 1 and 65535", value)
	}
	return net.JoinHostPort(host, strconv.Itoa(portNumber)), nil
}
