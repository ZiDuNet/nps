package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDialBridgeTCPHTTPProxyUsesProxyAuthorization(t *testing.T) {
	target := mustListenTCP(t)
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	proxy := mustListenTCP(t)
	defer proxy.Close()
	requestSeen := make(chan *http.Request, 1)
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		requestSeen <- request
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		_, _ = io.Copy(conn, conn)
	}()

	connection, err := dialBridgeTCP(context.Background(), "http://user:pass@"+proxy.Addr().String(), target.Addr().String(), "")
	if err != nil {
		t.Fatalf("dialBridgeTCP through HTTP proxy: %v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatalf("write through proxy: %v", err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil || string(response) != "ping" {
		t.Fatalf("echo through proxy = %q, %v", response, err)
	}
	request := <-requestSeen
	if request.Method != http.MethodConnect || request.Host != target.Addr().String() {
		t.Fatalf("unexpected CONNECT request: %s %s", request.Method, request.Host)
	}
	if got := request.Header.Get("Proxy-Authorization"); got == "" {
		t.Fatal("Proxy-Authorization header was not sent")
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Fatalf("unexpected origin Authorization header %q", got)
	}
}

func TestDialBridgeTCPSOCKS5HUsesRemoteDNS(t *testing.T) {
	proxy := mustListenTCP(t)
	defer proxy.Close()
	requestHost := make(chan string, 1)
	go func() {
		connection, err := proxy.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		greeting := make([]byte, 3)
		if _, err := io.ReadFull(reader, greeting); err != nil || string(greeting) != string([]byte{5, 1, 0}) {
			return
		}
		_, _ = connection.Write([]byte{5, 0})
		request := make([]byte, 5)
		if _, err := io.ReadFull(reader, request); err != nil || request[0] != 5 || request[1] != 1 || request[3] != 3 {
			return
		}
		host := make([]byte, int(request[4]))
		if _, err := io.ReadFull(reader, host); err != nil {
			return
		}
		port := make([]byte, 2)
		if _, err := io.ReadFull(reader, port); err != nil || binary.BigEndian.Uint16(port) != 443 {
			return
		}
		requestHost <- string(host)
		_, _ = connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
		_, _ = io.Copy(connection, connection)
	}()

	connection, err := dialBridgeTCP(context.Background(), "socks5h://"+proxy.Addr().String(), "unresolvable.invalid:443", "")
	if err != nil {
		t.Fatalf("dialBridgeTCP through SOCKS5H: %v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatalf("write through SOCKS5H proxy: %v", err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil || string(response) != "ping" {
		t.Fatalf("SOCKS5H echo = %q, %v", response, err)
	}
	if host := <-requestHost; host != "unresolvable.invalid" {
		t.Fatalf("SOCKS5H target hostname = %q", host)
	}
}

func TestDialBridgeTCPSOCKS4aUsesRemoteDNS(t *testing.T) {
	proxy := mustListenTCP(t)
	defer proxy.Close()
	requestHost := make(chan string, 1)
	go func() {
		connection, err := proxy.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader, header); err != nil || header[0] != 4 || header[1] != 1 || string(header[4:8]) != string([]byte{0, 0, 0, 1}) {
			return
		}
		if _, err := reader.ReadString(0); err != nil { // SOCKS4 user ID terminator
			return
		}
		host, err := reader.ReadString(0)
		if err != nil {
			return
		}
		requestHost <- strings.TrimSuffix(host, "\x00")
		_, _ = connection.Write([]byte{0, 0x5a, 0, 0, 0, 0, 0, 0})
		_, _ = io.Copy(connection, connection)
	}()

	connection, err := dialBridgeTCP(context.Background(), "socks4a://"+proxy.Addr().String(), "unresolvable.invalid:443", "")
	if err != nil {
		t.Fatalf("dialBridgeTCP through SOCKS4a: %v", err)
	}
	defer connection.Close()
	if host := <-requestHost; host != "unresolvable.invalid" {
		t.Fatalf("SOCKS4a target hostname = %q", host)
	}
}

func TestNoProxyMatchSupportsHostPortCIDRAndIPv6(t *testing.T) {
	tests := []struct {
		target string
		list   string
		want   bool
	}{
		{target: "api.example.com:443", list: ".example.com", want: true},
		{target: "example.com:443", list: "example.com:80", want: false},
		{target: "192.168.1.10:8080", list: "192.168.0.0/16", want: true},
		{target: "[2001:db8::1]:443", list: "2001:db8::/32", want: true},
		{target: "[2001:db8::1]:443", list: "[2001:db8::1]:443", want: true},
		{target: "api.example.com:443", list: "*", want: true},
	}
	for _, test := range tests {
		if got := noProxyMatch(test.target, test.list); got != test.want {
			t.Errorf("noProxyMatch(%q, %q) = %v, want %v", test.target, test.list, got, test.want)
		}
	}
}

func mustListenTCP(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}
