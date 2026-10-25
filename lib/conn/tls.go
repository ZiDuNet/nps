package conn

import (
	"crypto/tls"
	"net"
	"strings"
)

// GetTlsConn establishes an inner TLS connection over an NPS bridge stream.
// Backend certificates are intentionally not verified here: the NPS host rule
// controls the destination and deployments commonly use private/self-signed
// certificates for the inner hop. The SNI is still sent for virtual hosting.
func GetTlsConn(c net.Conn, sni string) (net.Conn, error) {
	if c == nil {
		return nil, net.ErrClosed
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(sni)); err == nil {
		sni = host
	} else {
		sni = strings.Trim(strings.TrimSpace(sni), "[]")
	}
	tlsConn := tls.Client(c, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return tlsConn, nil
}
