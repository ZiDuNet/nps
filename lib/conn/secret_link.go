package conn

import (
	"net"
	"time"
)

const secretLinkProbeTimeout = 250 * time.Millisecond

// ProbeSecretLink reads the optional standard Link record used by recent NPCs.
// Legacy NPCs start sending application bytes immediately after the Secret
// password; a short deadline and TeeConn preserve every probed byte so that
// the raw stream can still be forwarded unchanged.
func ProbeSecretLink(src net.Conn) (link *Link, replay []byte) {
	if src == nil {
		return nil, nil
	}
	tee := NewTeeConn(src)
	_ = tee.SetReadDeadline(time.Now().Add(secretLinkProbeTimeout))
	defer func() {
		_ = tee.SetReadDeadline(time.Time{})
		tee.StopAndClean()
	}()
	parsed, err := NewConn(tee).GetLinkInfo()
	if err != nil || parsed == nil {
		return nil, tee.Buffered()
	}
	tee.ClearBuffered()
	return parsed, nil
}

// WriteSecretLink emits a standard Link record after the legacy 32-byte Secret
// key. The server uses a bounded probe and replays non-Link bytes for older
// Secret clients.
func WriteSecretLink(dst net.Conn, link *Link) error {
	if dst == nil || link == nil {
		return net.ErrClosed
	}
	_, err := NewConn(dst).SendInfo(link, "")
	return err
}
