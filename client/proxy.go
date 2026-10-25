package client

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/lib/common"
)

type bufferedNetConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedNetConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// dialBridgeTCP opens a TCP connection directly or through a supported
// outbound proxy. Proxy handshakes are bounded by the caller's context.
func dialBridgeTCP(ctx context.Context, proxyURL, target, localIP string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, fmt.Errorf("invalid Bridge address %q: %w", target, err)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, clientDialTimeout)
		defer cancel()
	}

	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		proxyURL = environmentProxyURL(target)
	}
	if proxyURL == "" {
		dialer := &net.Dialer{LocalAddr: common.BuildTCPBindAddr(localIP)}
		return dialer.DialContext(ctx, "tcp", target)
	}

	parsed, err := parseProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{LocalAddr: common.BuildTCPBindAddr(localIP)}
	proxyConn, err := dialer.DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return nil, fmt.Errorf("connect to outbound proxy %s: %w", parsed.Host, err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = proxyConn.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = proxyConn.SetDeadline(deadline)
	}

	if parsed.Scheme == "https" {
		tlsConn := tls.Client(proxyConn, &tls.Config{ServerName: parsed.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("TLS handshake with outbound proxy: %w", err)
		}
		proxyConn = tlsConn
	}

	var tunneled net.Conn
	switch parsed.Scheme {
	case "http", "https":
		tunneled, err = httpConnectTunnel(proxyConn, parsed, target)
	case "socks5", "socks5h":
		tunneled, err = socks5ConnectTunnel(ctx, proxyConn, parsed, target, parsed.Scheme == "socks5h")
	case "socks4", "socks4a":
		tunneled, err = socks4ConnectTunnel(ctx, proxyConn, parsed, target, parsed.Scheme == "socks4a")
	default:
		err = fmt.Errorf("unsupported outbound proxy scheme %q", parsed.Scheme)
	}
	if err != nil {
		return nil, err
	}
	if err := proxyConn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	closeOnError = false
	return tunneled, nil
}

func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("outbound proxy URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid outbound proxy URL: %w", err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h", "socks4", "socks4a":
	default:
		return nil, fmt.Errorf("unsupported outbound proxy scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("outbound proxy URL must include a host")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("outbound proxy URL must not include a path, query, or fragment")
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "1080"
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("invalid outbound proxy port %q", port)
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(portNumber))
	return u, nil
}

func environmentProxyURL(target string) string {
	proxyURL := strings.TrimSpace(os.Getenv("ALL_PROXY"))
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(os.Getenv("all_proxy"))
	}
	if proxyURL == "" || noProxyMatch(target, firstNonEmpty(os.Getenv("NO_PROXY"), os.Getenv("no_proxy"))) {
		return ""
	}
	return proxyURL
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func noProxyMatch(target, list string) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	for _, raw := range strings.Split(list, ",") {
		entry := strings.TrimSpace(strings.ToLower(raw))
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		entryHost, entryPort := entry, ""
		if candidateHost, candidatePort, splitErr := net.SplitHostPort(entry); splitErr == nil {
			entryHost, entryPort = candidateHost, candidatePort
		} else if strings.Count(entry, ":") == 1 {
			parts := strings.SplitN(entry, ":", 2)
			if _, parseErr := strconv.Atoi(parts[1]); parseErr == nil {
				entryHost, entryPort = parts[0], parts[1]
			}
		}
		if entryPort != "" && entryPort != port {
			continue
		}
		entryHost = strings.Trim(strings.TrimPrefix(entryHost, "."), "[]")
		if _, network, parseErr := net.ParseCIDR(entryHost); parseErr == nil {
			if ip := net.ParseIP(host); ip != nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if ip := net.ParseIP(entryHost); ip != nil {
			if targetIP := net.ParseIP(host); targetIP != nil && ip.Equal(targetIP) {
				return true
			}
			continue
		}
		if host == entryHost || strings.HasSuffix(host, "."+entryHost) {
			return true
		}
	}
	return false
}

func httpConnectTunnel(proxyConn net.Conn, proxyURL *url.URL, target string) (net.Conn, error) {
	request, err := http.NewRequest(http.MethodConnect, "http://"+target, nil)
	if err != nil {
		return nil, err
	}
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		request.SetBasicAuth(proxyURL.User.Username(), password)
		request.Header.Set("Proxy-Authorization", request.Header.Get("Authorization"))
		request.Header.Del("Authorization")
	}
	if err := request.Write(proxyConn); err != nil {
		return nil, fmt.Errorf("send HTTP CONNECT request: %w", err)
	}
	reader := bufio.NewReader(proxyConn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("outbound HTTP proxy returned %s", response.Status)
	}
	return &bufferedNetConn{Conn: proxyConn, reader: reader}, nil
}

func socks5ConnectTunnel(ctx context.Context, proxyConn net.Conn, proxyURL *url.URL, target string, remoteDNS bool) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("invalid SOCKS5 target %q: %w", target, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("invalid SOCKS5 target port %q", portText)
	}

	method := byte(0x00)
	methods := []byte{0x00}
	if proxyURL.User != nil {
		method = 0x02
		methods = []byte{0x02}
	}
	if _, err := proxyConn.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return nil, fmt.Errorf("send SOCKS5 methods: %w", err)
	}
	selection := make([]byte, 2)
	if _, err := io.ReadFull(proxyConn, selection); err != nil {
		return nil, fmt.Errorf("read SOCKS5 method: %w", err)
	}
	if selection[0] != 0x05 || selection[1] == 0xff || selection[1] != method {
		return nil, errors.New("SOCKS5 proxy rejected available authentication methods")
	}
	if method == 0x02 {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		if len(username) > 255 || len(password) > 255 {
			return nil, errors.New("SOCKS5 username and password must be at most 255 bytes")
		}
		auth := []byte{0x01, byte(len(username))}
		auth = append(auth, username...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if _, err := proxyConn.Write(auth); err != nil {
			return nil, fmt.Errorf("send SOCKS5 credentials: %w", err)
		}
		status := make([]byte, 2)
		if _, err := io.ReadFull(proxyConn, status); err != nil {
			return nil, fmt.Errorf("read SOCKS5 authentication response: %w", err)
		}
		if status[0] != 0x01 || status[1] != 0x00 {
			return nil, errors.New("SOCKS5 proxy authentication failed")
		}
	}

	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 0x01)
			request = append(request, ipv4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else if remoteDNS {
		if len(host) > 255 {
			return nil, errors.New("SOCKS5 target hostname is longer than 255 bytes")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	} else {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			if err == nil {
				err = errors.New("no IP address found")
			}
			return nil, fmt.Errorf("resolve SOCKS5 target %q locally: %w", host, err)
		}
		ip := addresses[0].IP
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 0x01)
			request = append(request, ipv4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := proxyConn.Write(request); err != nil {
		return nil, fmt.Errorf("send SOCKS5 CONNECT request: %w", err)
	}
	reader := bufio.NewReader(proxyConn)
	response := make([]byte, 4)
	if _, err := io.ReadFull(reader, response); err != nil {
		return nil, fmt.Errorf("read SOCKS5 CONNECT response: %w", err)
	}
	if response[0] != 0x05 || response[2] != 0x00 {
		return nil, errors.New("invalid SOCKS5 CONNECT response")
	}
	if response[1] != 0x00 {
		return nil, fmt.Errorf("SOCKS5 CONNECT failed with reply %d", response[1])
	}
	var addressLength int
	switch response[3] {
	case 0x01:
		addressLength = net.IPv4len
	case 0x04:
		addressLength = net.IPv6len
	case 0x03:
		length, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("read SOCKS5 bound domain length: %w", err)
		}
		addressLength = int(length)
	default:
		return nil, errors.New("invalid SOCKS5 bound address type")
	}
	if _, err := io.CopyN(io.Discard, reader, int64(addressLength+2)); err != nil {
		return nil, fmt.Errorf("read SOCKS5 bound address: %w", err)
	}
	return &bufferedNetConn{Conn: proxyConn, reader: reader}, nil
}

func socks4ConnectTunnel(ctx context.Context, proxyConn net.Conn, proxyURL *url.URL, target string, remoteDNS bool) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, fmt.Errorf("invalid SOCKS4 target %q: %w", target, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("invalid SOCKS4 target port %q", portText)
	}
	username := ""
	if proxyURL.User != nil {
		username = proxyURL.User.Username()
	}
	if len(username) > 255 {
		return nil, errors.New("SOCKS4 user ID must be at most 255 bytes")
	}

	request := []byte{0x04, 0x01, byte(port >> 8), byte(port)}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.To4() != nil {
		request = append(request, ip.To4()...)
	} else if remoteDNS {
		if len(host) > 255 {
			return nil, errors.New("SOCKS4a target hostname is longer than 255 bytes")
		}
		request = append(request, 0, 0, 0, 1)
	} else {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve SOCKS4 target %q locally: %w", host, err)
		}
		var ipv4 net.IP
		for _, address := range addresses {
			if candidate := address.IP.To4(); candidate != nil {
				ipv4 = candidate
				break
			}
		}
		if ipv4 == nil {
			return nil, fmt.Errorf("SOCKS4 target %q has no IPv4 address", host)
		}
		request = append(request, ipv4...)
	}
	request = append(request, username...)
	request = append(request, 0x00)
	if remoteDNS && net.ParseIP(strings.Trim(host, "[]")) == nil {
		request = append(request, host...)
		request = append(request, 0x00)
	}
	if _, err := proxyConn.Write(request); err != nil {
		return nil, fmt.Errorf("send SOCKS4 CONNECT request: %w", err)
	}
	response := make([]byte, 8)
	if _, err := io.ReadFull(proxyConn, response); err != nil {
		return nil, fmt.Errorf("read SOCKS4 CONNECT response: %w", err)
	}
	if response[0] != 0x00 || response[1] != 0x5a {
		return nil, fmt.Errorf("SOCKS4 CONNECT failed with reply %d", response[1])
	}
	return proxyConn, nil
}
