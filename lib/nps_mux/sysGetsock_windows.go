//go:build windows
// +build windows

package nps_mux

import "net"

// socketBufferSizeFunc returns a fixed buffer size on Windows. syscall does
// not expose GetsockoptInt here, and the read-side bandwidth estimate only
// needs a sane upper bound.
func socketBufferSizeFunc(c net.Conn) func() (int, error) {
	// https://github.com/golang/sys/blob/master/windows/syscall_windows.go#L1184
	// not support, WTF???
	// Todo
	// return syscall.GetsockoptInt((syscall.Handle)(unsafe.Pointer(fd.Fd())), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	return func() (int, error) { return 15 * 1024 * 1024, nil }
}
