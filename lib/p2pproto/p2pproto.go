// Package p2pproto contains the small control messages exchanged by modern
// NPC clients before they establish a direct QUIC path.  The messages are
// deliberately length-prefixed using the existing NPS Conn framing so the
// protocol can run over TCP, KCP, WebSocket, QUIC and mux streams alike.
package p2pproto

import (
	"encoding/json"
	"errors"
	"fmt"

	"ehang.io/nps/lib/conn"
)

const (
	TransportMode = "quic"
	maxMessage    = 4 << 10
)

type Request struct {
	Password   string `json:"password"`
	Background bool   `json:"background,omitempty"`
	ServerIP   string `json:"server_ip,omitempty"`
	NodeUUID   string `json:"node_uuid,omitempty"`
}

type Invite struct {
	SessionID string `json:"session_id"`
	Token     string `json:"token"`
}

type Join struct {
	SessionID string `json:"session_id"`
	Token     string `json:"token"`
	ServerIP  string `json:"server_ip,omitempty"`
	Decline   string `json:"decline,omitempty"`
}

const (
	CodeRefused     = "refused"
	CodeUnsupported = "unsupported"
	CodeOffline     = "offline"
	CodeBusy        = "busy"
)

type Reply struct {
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type RefusedError struct {
	Code    string
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("p2p refused by server (%s): %s", e.Code, e.Message)
}

func (r Reply) Err() error {
	if r.Code == "" {
		return nil
	}
	return &RefusedError{Code: r.Code, Message: r.Message}
}

var ErrMessageSize = errors.New("p2p message length out of range")

func Write(c *conn.Conn, v any) error {
	if c == nil || c.Conn == nil {
		return errors.New("p2p control connection is nil")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) == 0 || len(b) > maxMessage {
		return errors.New("p2p message too large")
	}
	if err := c.WriteLenContent(b); err != nil {
		return err
	}
	return nil
}

func Read(c *conn.Conn, v any) error {
	if c == nil || c.Conn == nil {
		return errors.New("p2p control connection is nil")
	}
	n, err := c.GetLen()
	if err != nil {
		return err
	}
	if n <= 0 || n > maxMessage {
		return ErrMessageSize
	}
	b, err := c.GetShortContent(n)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
