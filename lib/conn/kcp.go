package conn

import (
	"net"

	"ehang.io/nps/lib/common"
	"github.com/xtaci/kcp-go"
)

// DialKCPWithLocalIP opens a KCP session while optionally binding its UDP
// socket to the requested local source IP. An empty or invalid local IP uses
// the library's normal route selection.
func DialKCPWithLocalIP(server, localIP string) (*kcp.UDPSession, error) {
	if common.BuildUDPBindAddr(localIP) == nil {
		return kcp.DialWithOptions(server, nil, 10, 3)
	}
	remote, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, err
	}
	network := "udp4"
	if remote.IP == nil || remote.IP.To4() == nil {
		network = "udp"
	}
	packetConn, err := net.ListenUDP(network, common.BuildUDPBindAddr(localIP))
	if err != nil {
		return nil, err
	}
	session, err := kcp.NewConn2(remote, nil, 10, 3, packetConn)
	if err != nil {
		_ = packetConn.Close()
		return nil, err
	}
	return session, nil
}
