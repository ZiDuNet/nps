package proxy

import (
	"bytes"
	"io"
	"testing"

	"ehang.io/nps/server/traffic"
)

func collectInspectorEvents(t *testing.T, raw string, request traffic.Event) []traffic.Event {
	t.Helper()
	resource := traffic.Resource{Kind: "test", ID: 9001}
	events, closeStream := traffic.Subscribe(resource)
	defer closeStream()
	queue := newRequestQueue()
	request.Resource = resource
	queue.add(request)
	inspector := newResponseInspector(bytes.NewBufferString(raw), resource, queue, make(chan struct{}))
	_, _ = io.ReadAll(inspector)
	var result []traffic.Event
	for {
		select {
		case event := <-events:
			result = append(result, event)
		default:
			return result
		}
	}
}

func TestResponseInspectorCapturesFixedTextBody(t *testing.T) {
	events := collectInspectorEvents(t, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 13\r\nX-Trace: abc\r\n\r\n{\"ok\":true}", traffic.Event{
		RequestID: "req-fixed", Method: "POST", Host: "example.test", Path: "/api",
		RemoteAddr: "198.51.100.10:1234", TargetAddr: "10.0.0.2:8080",
	})
	var body string
	var response traffic.Event
	for _, event := range events {
		if event.Type == "body_chunk" && event.Part == "response" {
			body += event.Body
		}
		if event.Type == "response_headers" {
			response = event
		}
	}
	if response.Status != 200 || response.Headers["X-Trace"] != "abc" {
		t.Fatalf("unexpected response metadata: %#v", response)
	}
	if body != "{\"ok\":true}" {
		t.Fatalf("unexpected response body %q", body)
	}
}

func TestResponseInspectorCapturesChunkedTextBody(t *testing.T) {
	events := collectInspectorEvents(t, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n", traffic.Event{RequestID: "req-chunk", Method: "GET", Path: "/events"})
	var body string
	for _, event := range events {
		if event.Type == "body_chunk" && event.Part == "response" {
			body += event.Body
		}
	}
	if body != "hello world" {
		t.Fatalf("unexpected chunked response body %q", body)
	}
}
