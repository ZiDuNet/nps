package proxy

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
)

func TestSecretTargetConnType(t *testing.T) {
	cases := []struct {
		name     string
		taskType string
		linkType string
		want     string
	}{
		{name: "legacy default", taskType: "", linkType: "udp", want: common.CONN_TCP},
		{name: "tcp forced", taskType: "tcp", linkType: "udp", want: common.CONN_TCP},
		{name: "udp forced", taskType: "udp", linkType: "tcp", want: common.CONN_UDP},
		{name: "all with udp link", taskType: "all", linkType: "udp", want: common.CONN_UDP},
		{name: "all with tcp link", taskType: "all", linkType: "tcp", want: common.CONN_TCP},
		{name: "unknown falls back", taskType: "bogus", linkType: "udp", want: common.CONN_TCP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := secretTargetConnType(tc.taskType, tc.linkType); got != tc.want {
				t.Fatalf("secretTargetConnType(%q, %q) = %q, want %q", tc.taskType, tc.linkType, got, tc.want)
			}
		})
	}
}

func TestSecretTaskNeedsLink(t *testing.T) {
	cases := []struct {
		taskType string
		want     bool
	}{
		{taskType: "", want: false},
		{taskType: "tcp", want: false},
		{taskType: "udp", want: true},
		{taskType: "all", want: true},
		{taskType: "bogus", want: false},
	}
	for _, tc := range cases {
		if got := secretTaskNeedsLink(tc.taskType); got != tc.want {
			t.Fatalf("secretTaskNeedsLink(%q) = %v, want %v", tc.taskType, got, tc.want)
		}
	}
}

type secretTestBridge struct {
	mu    sync.Mutex
	link  *conn.Link
	peer  net.Conn
	ready chan struct{}
}

func (b *secretTestBridge) SendLinkInfo(_ int, link *conn.Link, _ *file.Tunnel) (net.Conn, error) {
	b.mu.Lock()
	copied := *link
	b.link = &copied
	b.mu.Unlock()
	server, client := net.Pipe()
	b.peer = client
	close(b.ready)
	return server, nil
}

func TestSecretServerForwardsFramedUDPStream(t *testing.T) {
	task := &file.Tunnel{
		Mode:       "secret",
		TargetType: "udp",
		Client: &file.Client{
			Id:     7,
			Status: true,
			Cnf:    &file.Config{},
			Flow:   &file.Flow{},
			Rate:   nil,
		},
		Target: &file.Target{TargetStr: "10.0.0.1:53"},
		Flow:   &file.Flow{},
	}
	bridge := &secretTestBridge{ready: make(chan struct{})}
	server := NewSecretServer(bridge, task, false, true, false)

	visitor, serverSide := net.Pipe()
	t.Cleanup(func() {
		_ = visitor.Close()
		_ = serverSide.Close()
	})
	handleDone := make(chan error, 1)
	go func() {
		handleDone <- server.HandleSecret(serverSide)
	}()

	visitorConn := conn.NewConn(visitor)
	link := &conn.Link{
		ConnType: "udp",
		Host:     "10.0.0.1:53",
		Option:   conn.Options{NeedAck: true, Timeout: 2 * time.Second},
	}
	if _, err := visitorConn.SendInfo(link, ""); err != nil {
		t.Fatalf("send link: %v", err)
	}
	if err := conn.ReadACK(visitor, 2*time.Second); err != nil {
		t.Fatalf("read ack: %v", err)
	}

	select {
	case <-bridge.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not receive the link")
	}
	bridge.mu.Lock()
	relayed := bridge.link
	bridge.mu.Unlock()
	if relayed == nil || relayed.ConnType != common.CONN_UDP {
		t.Fatalf("relayed link type = %+v, want udp", relayed)
	}
	if !relayed.DatagramFramed {
		t.Fatal("relayed UDP link must carry the datagram framing flag")
	}

	peerResult := make(chan string, 1)
	go func() {
		buf := make([]byte, 128)
		_ = bridge.peer.SetReadDeadline(time.Now().Add(3 * time.Second))
		peerFramed := conn.NewFramedConn(bridge.peer)
		n, err := peerFramed.Read(buf)
		if err != nil {
			peerResult <- "err:" + err.Error()
			return
		}
		peerResult <- string(buf[:n])
	}()

	framed := conn.NewFramedConn(visitor)
	if _, err := framed.Write([]byte("dns-query")); err != nil {
		t.Fatalf("write datagram: %v", err)
	}
	select {
	case got := <-peerResult:
		if !bytes.Equal([]byte(got), []byte("dns-query")) {
			t.Fatalf("peer payload = %q, want dns-query", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for peer payload")
	}
	_ = visitor.Close()
	_ = serverSide.Close()
	select {
	case err := <-handleDone:
		if err != nil {
			t.Fatalf("HandleSecret: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HandleSecret did not return")
	}
}

func TestSecretServerAccountsClientAndTaskFlow(t *testing.T) {
	clientFlow := &file.Flow{}
	client := &file.Client{
		Id:     8,
		Status: true,
		Cnf:    &file.Config{},
		Flow:   clientFlow,
	}
	task := &file.Tunnel{
		Mode:     "secret",
		Client:   client,
		Target:   &file.Target{TargetStr: "10.0.0.1:80"},
		Flow:     &file.Flow{},
		Password: "secret-test-password",
	}
	bridge := &secretTestBridge{ready: make(chan struct{})}
	server := NewSecretServer(bridge, task, false, false, false)

	visitor, serverSide := net.Pipe()
	t.Cleanup(func() {
		_ = visitor.Close()
		_ = serverSide.Close()
	})
	done := make(chan error, 1)
	go func() {
		done <- server.HandleSecret(serverSide)
	}()

	select {
	case <-bridge.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge was not called")
	}

	payload := []byte("payload")
	writeDone := make(chan error, 1)
	go func() {
		_, err := visitor.Write(payload)
		writeDone <- err
	}()
	buf := make([]byte, len(payload))
	_ = bridge.peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(bridge.peer, buf); err != nil {
		t.Fatalf("read peer payload: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("peer payload = %q, want %q", buf, payload)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write visitor payload: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("visitor write did not complete")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if in, _, _ := clientFlow.Snapshot(); in == int64(len(payload)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if in, out, _ := clientFlow.Snapshot(); in != int64(len(payload)) || out != 0 {
		t.Fatalf("client flow = (%d, %d), want (%d, 0)", in, out, len(payload))
	}
	if in, out, _ := task.Flow.Snapshot(); in != int64(len(payload)) || out != 0 {
		t.Fatalf("task flow = (%d, %d), want (%d, 0)", in, out, len(payload))
	}
	_ = bridge.peer.Close()
	_ = visitor.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleSecret: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleSecret did not return")
	}
}
