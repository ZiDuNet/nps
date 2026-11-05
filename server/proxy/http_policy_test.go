package proxy

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type deadlineRecordingConn struct {
	mu sync.Mutex

	readData  bytes.Buffer
	writeData bytes.Buffer

	readDeadline  time.Time
	writeDeadline time.Time
}

func (c *deadlineRecordingConn) Read(p []byte) (int, error) {
	return c.readData.Read(p)
}

func (c *deadlineRecordingConn) Write(p []byte) (int, error) {
	return c.writeData.Write(p)
}

func (c *deadlineRecordingConn) Close() error {
	return nil
}

func (c *deadlineRecordingConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}

func (c *deadlineRecordingConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2}
}

func (c *deadlineRecordingConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.writeDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *deadlineRecordingConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *deadlineRecordingConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.writeDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *deadlineRecordingConn) deadlines() (time.Time, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readDeadline, c.writeDeadline
}

func TestBackendTimeoutConnUsesHeaderThenBodyPolicies(t *testing.T) {
	conn := &deadlineRecordingConn{}
	timeout := newBackendTimeoutConn(conn, httpProxyPolicy{
		backendResponseHeader:    time.Minute,
		backendReadStall:         30 * time.Second,
		backendResponseBodyTotal: 2 * time.Minute,
	})

	readDeadline, _ := conn.deadlines()
	if !readDeadline.IsZero() {
		t.Fatalf("backend read deadline = %s before a policy is selected", readDeadline)
	}

	timeout.SetReadPolicy(time.Minute, 0)
	started := time.Now()
	conn.readData.WriteByte('x')
	if _, err := timeout.Read(make([]byte, 1)); err != nil {
		t.Fatalf("Read error = %v", err)
	}
	readDeadline, _ = conn.deadlines()
	if readDeadline.IsZero() || readDeadline.Before(started.Add(55*time.Second)) {
		t.Fatalf("header deadline = %s, want about one minute", readDeadline)
	}

	timeout.SetReadPolicy(30*time.Second, 2*time.Minute)
	started = time.Now()
	conn.readData.WriteByte('y')
	if _, err := timeout.Read(make([]byte, 1)); err != nil {
		t.Fatalf("Read error = %v", err)
	}
	readDeadline, _ = conn.deadlines()
	if readDeadline.IsZero() || readDeadline.Before(started.Add(25*time.Second)) || readDeadline.After(started.Add(35*time.Second)) {
		t.Fatalf("body deadline = %s, want about thirty seconds", readDeadline)
	}

	timeout.ExemptReadTimeout()
	readDeadline, _ = conn.deadlines()
	if !readDeadline.IsZero() {
		t.Fatalf("exempt read deadline = %s, want zero", readDeadline)
	}
}

func TestTimeoutConnClearsReusedWriteDeadline(t *testing.T) {
	conn := &deadlineRecordingConn{}
	timeout := newTimeoutConn(conn, httpProxyPolicy{
		clientResponseStall: 20 * time.Second,
		clientResponseTotal: time.Minute,
	})

	timeout.SetWritePolicy(20*time.Second, time.Minute)
	if _, err := timeout.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	_, writeDeadline := conn.deadlines()
	if writeDeadline.IsZero() {
		t.Fatal("write deadline was not applied")
	}

	timeout.SetWritePolicy(0, 0)
	_, writeDeadline = conn.deadlines()
	if !writeDeadline.IsZero() {
		t.Fatalf("write deadline = %s after disabling policy, want zero", writeDeadline)
	}
}

func TestDeadlineReaderClearsDeadlineOnEOF(t *testing.T) {
	var deadline time.Time
	reader := newDeadlineReader(bytes.NewBufferString("body"), func(value time.Time) error {
		deadline = value
		return nil
	}, time.Minute, 0)

	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	if !deadline.IsZero() {
		t.Fatalf("deadline = %s after EOF, want zero", deadline)
	}
}

func TestStreamingResponseDetection(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		want        bool
	}{
		{name: "regular", status: http.StatusOK, contentType: "text/html"},
		{name: "sse", status: http.StatusOK, contentType: "text/event-stream; charset=utf-8", want: true},
		{name: "grpc", status: http.StatusOK, contentType: "application/grpc+proto", want: true},
		{name: "multipart-stream", status: http.StatusOK, contentType: "multipart/x-mixed-replace; boundary=frame", want: true},
		{name: "upgrade", status: http.StatusSwitchingProtocols, contentType: "application/octet-stream", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: test.status, Header: make(http.Header)}
			response.Header.Set("Content-Type", test.contentType)
			if got := isStreamingResponse(response); got != test.want {
				t.Fatalf("isStreamingResponse() = %t, want %t", got, test.want)
			}
		})
	}
}
