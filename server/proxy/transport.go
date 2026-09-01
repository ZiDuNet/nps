//go:build !windows && !npcgui
// +build !windows,!npcgui

package proxy

import (
	"errors"
	"net"
	"strconv"
	"syscall"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
)

func HandleTrans(c *conn.Conn, s *TunnelModeServer) error {
	if c == nil || s == nil || s.task == nil {
		if c != nil {
			_ = c.Close()
		}
		return errors.New("transparent proxy server is not configured")
	}
	if addr, err := getAddress(c.Conn); err != nil {
		return err
	} else {
		s.task.RLock()
		client, target := s.task.Client, s.task.Target
		s.task.RUnlock()
		if client == nil || target == nil {
			_ = c.Close()
			return errors.New("transparent proxy client or target is not configured")
		}
		if err := s.CheckFlowAndConnNum(client); err != nil {
			_ = c.Close()
			return err
		}
		defer client.AddConn()
		target.RLock()
		localProxy := target.LocalProxy
		target.RUnlock()
		return s.DealClient(c, client, addr, nil, common.CONN_TCP, nil, nil, localProxy, nil, nil)
	}
}

const SO_ORIGINAL_DST = 80

func getAddress(conn net.Conn) (string, error) {
	sysrawconn, f := conn.(syscall.Conn)
	if !f {
		return "", nil
	}
	rawConn, err := sysrawconn.SyscallConn()
	if err != nil {
		return "", nil
	}
	var ip string
	var port uint16
	err = rawConn.Control(func(fd uintptr) {
		addr, err := syscall.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IP, SO_ORIGINAL_DST)
		if err != nil {
			return
		}
		ip = net.IP(addr.Multiaddr[4:8]).String()
		port = uint16(addr.Multiaddr[2])<<8 + uint16(addr.Multiaddr[3])
	})
	return ip + ":" + strconv.Itoa(int(port)), nil
}
