package conn

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const websocketStreamReadLimit = 64 << 20

// WebSocketStream adapts binary WebSocket messages to the byte-stream
// semantics expected by the Bridge protocol. Each Write is one binary frame;
// Read transparently joins consecutive binary frames and hides frame
// boundaries from the protocol implementation.
type WebSocketStream struct {
	ws       *websocket.Conn
	remote   net.Addr
	reader   io.Reader
	readMu   sync.Mutex
	writeMu  sync.Mutex
	close    sync.Once
	closeErr error
}

func NewWebSocketStream(ws *websocket.Conn) (*WebSocketStream, error) {
	return NewWebSocketStreamWithRemote(ws, nil)
}

// NewWebSocketStreamWithRemote is equivalent to NewWebSocketStream but lets a
// trusted HTTP reverse proxy supply the peer address visible to the Bridge
// protocol. The underlying socket remains the source of truth when remote is
// nil, preserving direct WebSocket behavior.
func NewWebSocketStreamWithRemote(ws *websocket.Conn, remote net.Addr) (*WebSocketStream, error) {
	if ws == nil || ws.NetConn() == nil {
		return nil, errors.New("websocket connection is nil")
	}
	ws.SetReadLimit(websocketStreamReadLimit)
	return &WebSocketStream{ws: ws, remote: remote}, nil
}

func (s *WebSocketStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for {
		if s.reader == nil {
			messageType, reader, err := s.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if messageType != websocket.BinaryMessage {
				return 0, errors.New("bridge websocket accepts binary messages only")
			}
			s.reader = reader
		}
		n, err := s.reader.Read(p)
		if errors.Is(err, io.EOF) {
			s.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (s *WebSocketStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	writer, err := s.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := writer.Write(p)
	closeErr := writer.Close()
	if writeErr != nil {
		return n, writeErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (s *WebSocketStream) Close() error {
	s.close.Do(func() {
		s.closeErr = s.ws.Close()
	})
	return s.closeErr
}

func (s *WebSocketStream) LocalAddr() net.Addr { return s.ws.NetConn().LocalAddr() }
func (s *WebSocketStream) RemoteAddr() net.Addr {
	if s.remote != nil {
		return s.remote
	}
	return s.ws.NetConn().RemoteAddr()
}

func (s *WebSocketStream) SetDeadline(t time.Time) error {
	return s.ws.NetConn().SetDeadline(t)
}

func (s *WebSocketStream) SetReadDeadline(t time.Time) error {
	return s.ws.NetConn().SetReadDeadline(t)
}

func (s *WebSocketStream) SetWriteDeadline(t time.Time) error {
	return s.ws.NetConn().SetWriteDeadline(t)
}

var _ net.Conn = (*WebSocketStream)(nil)
