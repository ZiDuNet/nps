package conn

import "testing"

func TestNewKcpListenerRejectsInvalidAddress(t *testing.T) {
	listener, err := NewKcpListener("127.0.0.1:not-a-port")
	if err == nil {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatal("NewKcpListener accepted an invalid address")
	}
	if listener != nil {
		_ = listener.Close()
		t.Fatal("NewKcpListener returned a listener with an error")
	}
}
