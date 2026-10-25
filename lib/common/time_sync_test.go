package common

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestNTPResponseTime(t *testing.T) {
	packet := make([]byte, 48)
	packet[0] = 0x24
	packet[1] = 1
	want := time.Now().UTC().Truncate(time.Second)
	seconds := uint32(want.Unix() + ntpEpochOffset)
	binary.BigEndian.PutUint32(packet[40:44], seconds)
	got, err := ntpResponseTime(packet)
	if err != nil {
		t.Fatal(err)
	}
	if got.Unix() != want.Unix() {
		t.Fatalf("NTP response time = %v, want %v", got, want)
	}
}

func TestNTPResponseRejectsMalformedPacket(t *testing.T) {
	if _, err := ntpResponseTime(make([]byte, 10)); err == nil {
		t.Fatal("short NTP response was accepted")
	}
}
