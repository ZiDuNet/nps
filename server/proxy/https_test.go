package proxy

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync/atomic"
	"testing"
)

func TestGetServerNameFromClientHelloRejectsIncompleteRecord(t *testing.T) {
	client, server := net.Pipe()
	result := make(chan struct {
		name string
		raw  []byte
		err  error
	}, 1)
	go func() {
		name, raw, err := getServerNameFromClientHello(server)
		result <- struct {
			name string
			raw  []byte
			err  error
		}{name, raw, err}
	}()

	header := []byte{0x16, 0x03, 0x03, 0x00, 0x2b}
	if _, err := client.Write(append(header, []byte{0x03, 0x03}...)); err != nil {
		t.Fatalf("write partial ClientHello: %v", err)
	}
	_ = client.Close()
	got := <-result
	_ = server.Close()
	if got.err == nil {
		t.Fatal("incomplete ClientHello unexpectedly accepted")
	}
	if !bytes.Equal(got.raw, append(header, []byte{0x03, 0x03}...)) {
		t.Fatalf("raw bytes = %x, want partial record", got.raw)
	}
	if got.name != "" {
		t.Fatalf("server name = %q, want empty on error", got.name)
	}
}

func TestGetServerNameFromClientHelloAcceptsCompleteRecordWithoutSNI(t *testing.T) {
	client, server := net.Pipe()
	result := make(chan struct {
		name string
		raw  []byte
		err  error
	}, 1)
	go func() {
		name, raw, err := getServerNameFromClientHello(server)
		result <- struct {
			name string
			raw  []byte
			err  error
		}{name, raw, err}
	}()

	body := make([]byte, 47)
	body[0] = 1 // ClientHello handshake type.
	body[3] = 43
	body[4], body[5] = 0x03, 0x03 // ClientHello version.
	body[38] = 0                  // Empty session ID.
	binary.BigEndian.PutUint16(body[39:41], 2)
	body[41], body[42] = 0x13, 0x01 // One cipher suite.
	body[43] = 1
	body[44] = 0 // Null compression.
	// The final two bytes declare an empty extension block.
	body[45], body[46] = 0, 0
	header := []byte{0x16, 0x03, 0x03, 0, byte(len(body))}
	want := append(append([]byte{}, header...), body...)
	if _, err := client.Write(want); err != nil {
		t.Fatalf("write complete ClientHello: %v", err)
	}
	got := <-result
	_ = client.Close()
	_ = server.Close()
	if got.err != nil {
		t.Fatalf("complete ClientHello rejected: %v", got.err)
	}
	if got.name != "" {
		t.Fatalf("server name = %q, want empty without SNI", got.name)
	}
	if !bytes.Equal(got.raw, want) {
		t.Fatalf("raw bytes = %x, want %x", got.raw, want)
	}
}

func TestGetServerNameFromClientHelloAcceptsHandshakeAcrossRecords(t *testing.T) {
	client, server := net.Pipe()
	result := make(chan struct {
		name string
		raw  []byte
		err  error
	}, 1)
	go func() {
		name, raw, err := getServerNameFromClientHello(server)
		result <- struct {
			name string
			raw  []byte
			err  error
		}{name, raw, err}
	}()

	handshake := make([]byte, 47)
	handshake[0] = 1
	handshake[3] = 43
	handshake[4], handshake[5] = 0x03, 0x03
	handshake[38] = 0
	binary.BigEndian.PutUint16(handshake[39:41], 2)
	handshake[41], handshake[42] = 0x13, 0x01
	handshake[43] = 1
	handshake[44] = 0
	handshake[45], handshake[46] = 0, 0
	firstHeader := []byte{0x16, 0x03, 0x03, 0, 20}
	secondHeader := []byte{0x16, 0x03, 0x03, 0, byte(len(handshake) - 20)}
	firstRecord := append(append([]byte{}, firstHeader...), handshake[:20]...)
	secondRecord := append(append([]byte{}, secondHeader...), handshake[20:]...)
	want := append(append([]byte{}, firstRecord...), secondRecord...)
	if _, err := client.Write(firstRecord); err != nil {
		t.Fatalf("write first ClientHello record: %v", err)
	}
	if _, err := client.Write(secondRecord); err != nil {
		t.Fatalf("write second ClientHello record: %v", err)
	}

	got := <-result
	_ = client.Close()
	_ = server.Close()
	if got.err != nil {
		t.Fatalf("fragmented ClientHello rejected: %v", got.err)
	}
	if got.name != "" {
		t.Fatalf("server name = %q, want empty without SNI", got.name)
	}
	if !bytes.Equal(got.raw, want) {
		t.Fatalf("raw bytes = %x, want %x", got.raw, want)
	}
}

func TestReleaseCertListenerKeepsSharedCertificateAlive(t *testing.T) {
	server := &HttpsServer{}
	listener := NewHttpsListener(nil)
	server.hostIdCertMap.Store(1, "shared-cert")
	server.hostIdCertMap.Store(2, "shared-cert")
	server.httpsListenerMap.Store("shared-cert", listener)

	server.hostIdCertMap.Delete(1)
	server.releaseCertListener("shared-cert")
	if _, ok := server.httpsListenerMap.Load("shared-cert"); !ok {
		t.Fatal("shared certificate listener was closed while another host still referenced it")
	}
	if atomic.LoadInt32(&listener.closed) != 0 {
		t.Fatal("shared certificate listener was marked closed too early")
	}

	server.hostIdCertMap.Delete(2)
	server.releaseCertListener("shared-cert")
	if _, ok := server.httpsListenerMap.Load("shared-cert"); ok {
		t.Fatal("unreferenced certificate listener was not removed")
	}
	if atomic.LoadInt32(&listener.closed) != 1 {
		t.Fatal("unreferenced certificate listener was not closed")
	}
}
