package goroutine

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
)

func TestParseAuthIPRequestUsesPOSTBody(t *testing.T) {
	request := []byte("POST /authIp?pass=query-secret HTTP/1.1\r\n" +
		"Host: example.test\r\nContent-Type: application/x-www-form-urlencoded\r\n" +
		"Content-Length: 12\r\n\r\npass=body%21")
	pass, ok := parseAuthIPRequest(request)
	if !ok || pass != "body!" {
		t.Fatalf("parsed auth request = (%q, %v), want body secret", pass, ok)
	}
}

func TestParseAuthIPRequestRejectsQueryOnlyOrNonPOST(t *testing.T) {
	if pass, ok := parseAuthIPRequest([]byte("GET /authIp?pass=secret HTTP/1.1\r\nHost: example.test\r\n\r\n")); ok || pass != "" {
		t.Fatalf("GET request was accepted as (%q, %v)", pass, ok)
	}
	if pass, ok := parseAuthIPRequest([]byte("POST /authIp?pass=secret HTTP/1.1\r\nHost: example.test\r\n\r\n")); !ok || pass != "" {
		t.Fatalf("query-only POST parsed as (%q, %v), want empty body credential", pass, ok)
	}
	if pass, ok := parseAuthIPRequest([]byte("POST /other HTTP/1.1\r\nHost: example.test\r\n\r\npass=secret")); ok || pass != "" {
		t.Fatalf("other path was accepted as (%q, %v)", pass, ok)
	}
}

func TestInspectAuthIPRequestWaitsForSplitBody(t *testing.T) {
	first := []byte("POST /authIp HTTP/1.1\r\nHost: example.test\r\nContent-Length: 12\r\n\r\npass=")
	if pass, auth, complete := inspectAuthIPRequest(first); !auth || complete || pass != "" {
		t.Fatalf("partial request = (%q, %v, %v), want auth/incomplete", pass, auth, complete)
	}
	complete := append(first, []byte("correct")...)
	if pass, auth, done := inspectAuthIPRequest(complete); !auth || !done || pass != "correct" {
		t.Fatalf("complete request = (%q, %v, %v), want body password", pass, auth, done)
	}
}

func TestCopyBufferWithFlowsDirection(t *testing.T) {
	for _, test := range []struct {
		name      string
		direction FlowDirection
		wantIn    int64
		wantOut   int64
	}{
		{name: "inbound", direction: FlowInbound, wantIn: 11},
		{name: "outbound", direction: FlowOutbound, wantOut: 11},
	} {
		t.Run(test.name, func(t *testing.T) {
			flow := &file.Flow{}
			var dst bytes.Buffer
			if err := CopyBufferWithFlowsDirection(&dst, strings.NewReader("hello world"), flow, nil, nil, nil, "", test.direction); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if dst.String() != "hello world" {
				t.Fatalf("copied body = %q", dst.String())
			}
			inlet, export, _ := flow.Snapshot()
			if inlet != test.wantIn || export != test.wantOut {
				t.Fatalf("flow = (%d, %d), want (%d, %d)", inlet, export, test.wantIn, test.wantOut)
			}
		})
	}
}

func TestCopyBufferWithFlowsDirectionDeduplicatesOwnershipFlow(t *testing.T) {
	flow := &file.Flow{}
	task := &file.Tunnel{Flow: flow}
	var dst bytes.Buffer
	if err := CopyBufferWithFlowsDirection(&dst, strings.NewReader("payload"), flow, []*file.Flow{flow}, task, nil, "", FlowInbound); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	inlet, export, _ := flow.Snapshot()
	if inlet != int64(len("payload")) || export != 0 {
		t.Fatalf("deduplicated flow = (%d, %d), want (%d, 0)", inlet, export, len("payload"))
	}
}
