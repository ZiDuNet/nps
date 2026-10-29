package goroutine

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"ehang.io/nps/lib/file"
)

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
			if _, err := CopyBufferWithFlowsDirection(&dst, strings.NewReader("hello world"), flow, nil, nil, nil, "", test.direction); err != nil && err != io.EOF {
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
	if _, err := CopyBufferWithFlowsDirection(&dst, strings.NewReader("payload"), flow, []*file.Flow{flow}, task, nil, "", FlowInbound); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	inlet, export, _ := flow.Snapshot()
	if inlet != int64(len("payload")) || export != 0 {
		t.Fatalf("deduplicated flow = (%d, %d), want (%d, 0)", inlet, export, len("payload"))
	}
}
