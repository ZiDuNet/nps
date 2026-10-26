//go:build !windows
// +build !windows

package nps_mux

import (
	"net"
	"syscall"
)

// socketBufferSizeFunc returns a callback that reads SO_RCVBUF without
// duplicating the descriptor. TCPConn.File switches the socket into blocking
// mode, which makes a concurrent read immune to deadlines and can therefore
// stall Mux.Close indefinitely; SyscallConn avoids both the duplicate and the
// blocking-mode switch.
func socketBufferSizeFunc(c net.Conn) func() (int, error) {
	var raw syscall.RawConn
	var err error
	switch conn := c.(type) {
	case *net.TCPConn:
		raw, err = conn.SyscallConn()
	case *net.UDPConn:
		raw, err = conn.SyscallConn()
	default:
		return func() (int, error) { return 5 * 1024 * 1024, nil }
	}
	if err != nil {
		return func() (int, error) { return 5 * 1024 * 1024, nil }
	}
	return func() (int, error) {
		var size int
		controlErr := raw.Control(func(fd uintptr) {
			size, err = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		})
		if controlErr != nil {
			return 0, controlErr
		}
		return size, err
	}
}
