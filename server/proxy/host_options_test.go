package proxy

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestRewriteHostRequestPath(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://example.test/api/items?q=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	rewriteHostRequestPath(request, "/api => /v1/api")
	if got, want := request.URL.RequestURI(), "/v1/api/items?q=1"; got != want {
		t.Fatalf("request URI = %q, want %q", got, want)
	}
}

func TestApplyHeaderChangesAndCORS(t *testing.T) {
	headers := http.Header{"Server": []string{"nps"}, "X-Old": []string{"remove"}}
	applyHeaderChanges(headers, "Cache-Control: no-store\n-X-Old\n-Server")
	if headers.Get("Cache-Control") != "no-store" || headers.Get("X-Old") != "" || headers.Get("Server") != "" {
		t.Fatalf("unexpected headers: %#v", headers)
	}

	raw := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\nServer: backend\r\n\r\n"
	transformed := transformResponseHeaders([]byte(raw), &file.Host{ResponseHeaderChange: "-Server\nX-NPS: active", AutoCORS: true})
	value := string(transformed)
	for _, expected := range []string{"Transfer-Encoding: chunked\r\n", "Access-Control-Allow-Origin: *\r\n"} {
		if !strings.Contains(value, expected) {
			t.Fatalf("transformed response missing %q: %q", expected, value)
		}
	}
	parsed, err := http.ReadResponse(bufio.NewReader(strings.NewReader(value)), nil)
	if err != nil || parsed.Header.Get("X-NPS") != "active" {
		t.Fatalf("response header was not applied: err=%v headers=%#v", err, parsed.Header)
	}
	if strings.Contains(value, "Server: backend") {
		t.Fatalf("response server header was not removed: %q", value)
	}
}

func TestApplyHeaderChangesProtectsResponseFraming(t *testing.T) {
	headers := http.Header{"Content-Length": []string{"5"}, "Transfer-Encoding": []string{"chunked"}, "Connection": []string{"keep-alive"}}
	applyHeaderChanges(headers, "Content-Length: 999\n-Transfer-Encoding\nConnection: close")
	if headers.Get("Content-Length") != "5" || headers.Get("Transfer-Encoding") != "chunked" || headers.Get("Connection") != "keep-alive" {
		t.Fatalf("response framing headers were changed: %#v", headers)
	}
}

func TestResponseHeaderTransformKeepsBodyAndFraming(t *testing.T) {
	raw := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n"
	reader := newResponseHeaderTransform(strings.NewReader(raw), &file.Host{AutoCORS: true})
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "5\r\nhello\r\n0\r\n\r\n") {
		t.Fatalf("chunked body changed: %q", data)
	}
	response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(string(data))), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}
}
