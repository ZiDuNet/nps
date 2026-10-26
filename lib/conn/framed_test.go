package conn

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type framedPipe struct {
	client, server net.Conn
}

func newFramedPipe(t *testing.T) framedPipe {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return framedPipe{client: client, server: server}
}

func TestFramedConnPreservesDatagramBoundaries(t *testing.T) {
	p := newFramedPipe(t)
	writer := NewFramedConn(p.client)
	reader := NewFramedConn(p.server)
	go func() {
		if _, err := writer.Write([]byte("hello")); err != nil {
			t.Errorf("write hello: %v", err)
		}
		if _, err := writer.Write([]byte("world")); err != nil {
			t.Errorf("write world: %v", err)
		}
		_ = p.client.Close()
	}()

	buf := make([]byte, 64<<10)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("first datagram = %q, want hello", buf[:n])
	}
	n, err = reader.Read(buf)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if string(buf[:n]) != "world" {
		t.Fatalf("second datagram = %q, want world", buf[:n])
	}
}

func TestFramedConnReadSplitsLargeDatagram(t *testing.T) {
	p := newFramedPipe(t)
	writer := NewFramedConn(p.client)
	reader := NewFramedConn(p.server)
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	go func() {
		_, _ = writer.Write(payload)
		_ = p.client.Close()
	}()

	small := make([]byte, 300)
	var total int
	for total < len(payload) {
		n, err := reader.Read(small)
		if err != nil && total < len(payload) {
			t.Fatalf("read after %d bytes: %v", total, err)
		}
		total += n
	}
	if total != len(payload) {
		t.Fatalf("read %d bytes, want %d", total, len(payload))
	}
	// The next read reports EOF once the pending datagram is exhausted.
	if _, err := reader.Read(small); !errors.Is(err, io.EOF) {
		t.Fatalf("read after datagram = %v, want EOF", err)
	}
}

func TestFramedConnRejectsOversizedDatagram(t *testing.T) {
	p := newFramedPipe(t)
	writer := NewFramedConn(p.client)
	if _, err := writer.Write(make([]byte, maxFramedDatagramSize+1)); !errors.Is(err, errFramedDatagramTooLarge) {
		t.Fatalf("oversized write = %v, want %v", err, errFramedDatagramTooLarge)
	}
}

func TestCopyDatagramsRelaysBothDirections(t *testing.T) {
	streamClient, streamServer := net.Pipe()
	t.Cleanup(func() {
		_ = streamClient.Close()
		_ = streamServer.Close()
	})
	udpServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = udpServer.Close() })
	udpClient, err := net.DialUDP("udp", nil, udpServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	t.Cleanup(func() { _ = udpClient.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		CopyDatagrams(streamClient, udpClient, false, false, nil, false, nil, nil)
	}()

	framed := NewFramedConn(streamServer)
	if _, err := framed.Write([]byte("ping")); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	buf := make([]byte, 128)
	_ = udpServer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := udpServer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("udp read: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("udp payload = %q, want ping", buf[:n])
	}
	if _, err := udpServer.WriteToUDP([]byte("pong"), udpClient.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("udp write: %v", err)
	}
	n, err = framed.Read(buf)
	if err != nil {
		t.Fatalf("framed read: %v", err)
	}
	if string(buf[:n]) != "pong" {
		t.Fatalf("framed payload = %q, want pong", buf[:n])
	}
	_ = streamServer.Close()
	_ = udpServer.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CopyDatagrams did not return after both sides closed")
	}
}
