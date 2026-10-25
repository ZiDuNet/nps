package common

import (
	"bytes"
	"testing"
)

func TestReadUDPDatagramSupportsLargePayload(t *testing.T) {
	payload := bytes.Repeat([]byte("nps"), 12*1024)
	datagram := NewUDPDatagram(NewUDPHeader(0, 0, &Addr{Type: ipV4, Host: "198.51.100.20", Port: 5353}), payload)
	var raw bytes.Buffer
	if err := datagram.Write(&raw); err != nil {
		t.Fatalf("write UDP datagram: %v", err)
	}
	decoded, err := ReadUDPDatagram(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatalf("read UDP datagram: %v", err)
	}
	if decoded.Header == nil || decoded.Header.Addr == nil || decoded.Header.Addr.String() != "198.51.100.20:5353" {
		t.Fatalf("decoded address = %#v", decoded.Header)
	}
	if !bytes.Equal(decoded.Data, payload) {
		t.Fatalf("decoded payload length = %d, want %d", len(decoded.Data), len(payload))
	}
}

func TestReadUDPDatagramRejectsTruncatedAddress(t *testing.T) {
	if _, err := ReadUDPDatagram(bytes.NewReader([]byte{0, 0, 0, ipV4, 127})); err == nil {
		t.Fatal("truncated UDP address was accepted")
	}
}
