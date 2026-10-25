// This module is used for port reuse
// Distinguish client, web manager , HTTP and HTTPS according to the difference of protocol
package pmux

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"github.com/astaxie/beego/logs"
)

const (
	HTTP_GET        = 716984
	HTTP_POST       = 807983
	HTTP_HEAD       = 726965
	HTTP_PUT        = 808585
	HTTP_DELETE     = 686976
	HTTP_CONNECT    = 677978
	HTTP_OPTIONS    = 798084
	HTTP_TRACE      = 848265
	CLIENT          = 848384
	ACCEPT_TIME_OUT = 10
	maxHeaderBytes  = 64 * 1024
	maxTLSRecord    = 16 * 1024
	maxClientHello  = 1 << 20
)

type listenerKind int

const (
	listenerClient listenerKind = iota
	listenerClientTLS
	listenerClientWS
	listenerClientWSS
	listenerHTTP
	listenerHTTPS
	listenerManager
	listenerManagerTLS
	listenerKindCount
)

type PortMux struct {
	net.Listener
	port        int
	managerHost string
	clientHost  string
	bindHost    string
	listeners   [listenerKindCount]chan *PortConn
	active      [listenerKindCount]bool
	done        chan struct{}
	wg          sync.WaitGroup
	mu          sync.RWMutex
	isClose     bool
	started     bool
	startErr    error
	closeOnce   sync.Once
	channelOnce sync.Once
}

func NewPortMux(port int, managerHost string) *PortMux {
	return NewPortMuxWithAddress(port, managerHost, "")
}

// NewPortMuxWithAddress creates a port multiplexer bound to bindHost. The
// legacy constructor continues to use the wildcard address for compatibility.
func NewPortMuxWithAddress(port int, managerHost, bindHost string) *PortMux {
	return NewPortMuxWithHosts(port, managerHost, "", bindHost)
}

// NewPortMuxWithHosts creates a shared TCP listener that can distinguish the
// management panel and HTTP proxy by Host, and a TLS Bridge by SNI.
func NewPortMuxWithHosts(port int, managerHost, clientHost, bindHost string) *PortMux {
	pMux := &PortMux{
		managerHost: managerHost,
		clientHost:  clientHost,
		bindHost:    strings.TrimSpace(bindHost),
		port:        port,
		done:        make(chan struct{}),
	}
	for i := range pMux.listeners {
		pMux.listeners[i] = make(chan *PortConn)
	}
	// Keep the historical constructor behavior, but retain the error so callers
	// can inspect it through StartError instead of terminating the process.
	_ = pMux.Start()
	return pMux
}

func (pMux *PortMux) Start() error {
	pMux.mu.Lock()
	if pMux.started {
		err := pMux.startErr
		pMux.mu.Unlock()
		return err
	}
	if pMux.isClose {
		pMux.mu.Unlock()
		return fmt.Errorf("the port pmux has closed")
	}
	// Port multiplexing is based on TCP only
	listenHost := pMux.bindHost
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(listenHost, strconv.Itoa(pMux.port)))
	if err != nil {
		pMux.startErr = err
		pMux.mu.Unlock()
		return err
	}
	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		pMux.startErr = err
		logs.Error(err)
		pMux.mu.Unlock()
		return err
	}
	pMux.Listener = listener
	pMux.started = true
	pMux.startErr = nil
	pMux.wg.Add(1)
	pMux.mu.Unlock()
	go func() {
		defer pMux.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-pMux.done:
					return
				default:
				}
				if ne, ok := err.(net.Error); ok && ne.Temporary() {
					logs.Warn(err)
					continue
				}
				logs.Warn(err)
				pMux.signalClose()
				go pMux.finishClose()
				return
			}
			pMux.wg.Add(1)
			go func(c net.Conn) {
				defer pMux.wg.Done()
				pMux.process(c)
			}(conn)
		}
	}()
	return nil
}

// StartError reports a constructor/startup error without exposing mutable
// listener state. It is useful to fail initialization cleanly in callers that
// historically relied on NewPortMux starting the listener.
func (pMux *PortMux) StartError() error {
	pMux.mu.RLock()
	defer pMux.mu.RUnlock()
	return pMux.startErr
}

func (pMux *PortMux) process(conn net.Conn) {
	// Recognition according to different signs
	// read 3 byte
	// 设置读超时，防止恶意连接阻塞
	conn.SetReadDeadline(time.Now().Add(ACCEPT_TIME_OUT * time.Second))
	buf := make([]byte, 3)
	if n, err := io.ReadFull(conn, buf); err != nil || n != 3 {
		conn.Close()
		return
	}
	// 读完 3 字节后清除超时
	conn.SetReadDeadline(time.Time{})
	var ch chan *PortConn
	var rs []byte
	var readMore = false
	switch common.BytesToNum(buf) {
	case HTTP_CONNECT, HTTP_DELETE, HTTP_GET, HTTP_HEAD, HTTP_OPTIONS, HTTP_POST, HTTP_PUT, HTTP_TRACE: //http and manager
		ch, rs = pMux.classifyHTTP(conn, buf)
		if ch == nil {
			_ = conn.Close()
			return
		}
		readMore = true
	case CLIENT: // client connection
		if pMux.activeListeners()[listenerClient] {
			ch = pMux.listener(listenerClient)
		}
	default: // https
		ch, rs, readMore = pMux.classifyTLS(conn, buf)
		if ch == nil {
			_ = conn.Close()
			return
		}
	}
	if len(rs) == 0 {
		rs = buf
	}
	if ch == nil {
		conn.Close()
		return
	}
	timer := time.NewTimer(ACCEPT_TIME_OUT * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		conn.Close()
	case <-pMux.done:
		conn.Close()
	case ch <- newPortConn(conn, rs, readMore):
	}
}

func (pMux *PortMux) listener(kind listenerKind) chan *PortConn {
	if pMux == nil || kind < 0 || kind >= listenerKindCount {
		return nil
	}
	return pMux.listeners[kind]
}

func (pMux *PortMux) activeListeners() [listenerKindCount]bool {
	if pMux == nil {
		return [listenerKindCount]bool{}
	}
	pMux.mu.RLock()
	defer pMux.mu.RUnlock()
	return pMux.active
}

func (pMux *PortMux) getListener(kind listenerKind) net.Listener {
	if pMux == nil || kind < 0 || kind >= listenerKindCount {
		return NewPortListener(nil, nil)
	}
	pMux.mu.Lock()
	if !pMux.isClose {
		pMux.active[kind] = true
	}
	pMux.mu.Unlock()
	return NewPortListener(pMux.listener(kind), pMux.listenerAddr())
}

func (pMux *PortMux) classifyHTTP(conn net.Conn, prefix []byte) (chan *PortConn, []byte) {
	registered := pMux.activeListeners()
	var httpListener, managerListener, wsListener chan *PortConn
	if registered[listenerHTTP] {
		httpListener = pMux.listener(listenerHTTP)
	}
	if registered[listenerManager] {
		managerListener = pMux.listener(listenerManager)
	}
	if registered[listenerClientWS] {
		wsListener = pMux.listener(listenerClientWS)
	}
	activeCount := 0
	for _, listener := range []chan *PortConn{httpListener, managerListener, wsListener} {
		if listener != nil {
			activeCount++
		}
	}
	if activeCount == 0 {
		return nil, nil
	}
	if activeCount == 1 {
		for _, listener := range []chan *PortConn{httpListener, managerListener, wsListener} {
			if listener != nil {
				return listener, prefix
			}
		}
	}

	if err := conn.SetReadDeadline(time.Now().Add(ACCEPT_TIME_OUT * time.Second)); err != nil {
		return nil, nil
	}
	defer conn.SetReadDeadline(time.Time{})
	buffer := bytes.NewBuffer(make([]byte, 0, len(prefix)+1024))
	_, _ = buffer.Write(prefix)
	r := bufio.NewReaderSize(conn, 4096)
	var host string
	for {
		line := make([]byte, 0, 256)
		for {
			part, err := r.ReadSlice('\n')
			if buffer.Len()+len(part) > maxHeaderBytes {
				logs.Warn("port mux request header too large")
				return nil, nil
			}
			_, _ = buffer.Write(part)
			line = append(line, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				logs.Trace("port mux read HTTP header error: %v", err)
				return nil, nil
			}
			break
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			break
		}
		if parsedHost, ok := parseHostHeader([]byte(trimmed)); ok {
			host = parsedHost
		}
	}
	rest, _ := r.Peek(r.Buffered())
	_, _ = buffer.Write(rest)

	// Read the whole header before dispatch. Returning at the first Host line
	// can truncate headers when bufio.Reader has not prefetched the rest.
	switch {
	case managerListener != nil && sameHost(host, pMux.managerHost):
		return managerListener, buffer.Bytes()
	case wsListener != nil && sameHost(host, pMux.clientHost):
		return wsListener, buffer.Bytes()
	case httpListener != nil:
		return httpListener, buffer.Bytes()
	case managerListener != nil && wsListener == nil && host != "":
		// If the panel is the only active HTTP endpoint, retain the historical
		// catch-all behavior. With multiple endpoints, unknown Host values never
		// fall through to the management panel.
		return managerListener, buffer.Bytes()
	case wsListener != nil && managerListener == nil && host != "":
		return wsListener, buffer.Bytes()
	default:
		return nil, nil
	}
}

func parseHostHeader(line []byte) (string, bool) {
	const prefix = "host:"
	if len(line) < len(prefix) || !bytes.EqualFold(line[:len(prefix)], []byte(prefix)) {
		return "", false
	}
	return strings.TrimSpace(string(line[len(prefix):])), true
}

func sameHost(left, right string) bool {
	left = strings.TrimSuffix(strings.ToLower(common.GetIpByAddr(strings.TrimSpace(left))), ".")
	right = strings.TrimSuffix(strings.ToLower(common.GetIpByAddr(strings.TrimSpace(right))), ".")
	return left != "" && right != "" && left == right
}

func (pMux *PortMux) classifyTLS(conn net.Conn, prefix []byte) (chan *PortConn, []byte, bool) {
	registered := pMux.activeListeners()
	var httpsListener, tlsListener, wssListener, managerTLSListener chan *PortConn
	if registered[listenerHTTPS] {
		httpsListener = pMux.listener(listenerHTTPS)
	}
	if registered[listenerClientTLS] {
		tlsListener = pMux.listener(listenerClientTLS)
	}
	if registered[listenerClientWSS] {
		wssListener = pMux.listener(listenerClientWSS)
	}
	if registered[listenerManagerTLS] {
		managerTLSListener = pMux.listener(listenerManagerTLS)
	}
	var targets []chan *PortConn
	for _, listener := range []chan *PortConn{httpsListener, tlsListener, wssListener, managerTLSListener} {
		if listener != nil {
			targets = append(targets, listener)
		}
	}
	if len(targets) == 0 {
		return nil, nil, false
	}
	if len(targets) == 1 {
		return targets[0], prefix, true
	}
	if tlsListener != nil && wssListener != nil {
		// TLS Bridge and WSS are both encrypted byte streams. The current
		// configuration provides one Bridge SNI, so they cannot share one port
		// without an additional discriminator.
		return nil, nil, false
	}
	if err := conn.SetReadDeadline(time.Now().Add(ACCEPT_TIME_OUT * time.Second)); err != nil {
		return nil, nil, false
	}
	defer conn.SetReadDeadline(time.Time{})
	serverName, raw, err := readClientHelloServerName(conn, prefix)
	if err != nil {
		logs.Trace("port mux could not inspect TLS ClientHello: %v", err)
		return nil, nil, false
	}
	switch {
	case tlsListener != nil && (serverName == "" || sameHost(serverName, pMux.clientHost)):
		return tlsListener, raw, true
	case wssListener != nil && sameHost(serverName, pMux.clientHost):
		return wssListener, raw, true
	case managerTLSListener != nil && sameHost(serverName, pMux.managerHost):
		return managerTLSListener, raw, true
	case tlsListener != nil && serverName == "":
		// TLS clients connecting by IP may not send SNI. Preserve the historical
		// behavior for Bridge clients without routing an unknown named SNI to the
		// management listener.
		return tlsListener, raw, true
	case httpsListener != nil:
		return httpsListener, raw, true
	default:
		return nil, nil, false
	}
}

func readClientHelloServerName(conn net.Conn, prefix []byte) (string, []byte, error) {
	raw := append([]byte(nil), prefix...)
	handshake := make([]byte, 0, 1024)
	expected := 0
	for expected == 0 || len(handshake) < expected {
		header := make([]byte, 5)
		header[0], header[1], header[2] = prefix[0], prefix[1], prefix[2]
		// The first three record header bytes were consumed by PortMux.process.
		// Retain them for the first record, then read later record headers whole.
		if len(raw) == 3 {
			if _, err := io.ReadFull(conn, header[3:]); err != nil {
				raw = append(raw, header[3:]...)
				return "", raw, err
			}
			raw = append(raw, header[3:]...)
		} else {
			if _, err := io.ReadFull(conn, header); err != nil {
				raw = append(raw, header...)
				return "", raw, err
			}
			raw = append(raw, header...)
		}
		if header[0] != 0x16 {
			return "", raw, fmt.Errorf("invalid TLS record type %d", header[0])
		}
		recordLength := int(header[3])<<8 | int(header[4])
		if recordLength <= 0 || recordLength > maxTLSRecord {
			return "", raw, fmt.Errorf("invalid TLS record length %d", recordLength)
		}
		body := make([]byte, recordLength)
		if _, err := io.ReadFull(conn, body); err != nil {
			raw = append(raw, body...)
			return "", raw, err
		}
		raw = append(raw, body...)
		if len(handshake)+len(body) > maxClientHello {
			return "", raw, errors.New("TLS ClientHello is too large")
		}
		handshake = append(handshake, body...)
		if expected == 0 && len(handshake) >= 4 {
			if handshake[0] != 1 {
				return "", raw, errors.New("first TLS handshake is not ClientHello")
			}
			helloLength := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			expected = 4 + helloLength
			if helloLength == 0 || expected > maxClientHello {
				return "", raw, errors.New("invalid TLS ClientHello length")
			}
		}
	}
	clientHello := new(crypt.ClientHelloMsg)
	if !clientHello.Unmarshal(handshake[:expected]) {
		return "", raw, errors.New("invalid TLS ClientHello")
	}
	return clientHello.GetServerName(), raw, nil
}

func (pMux *PortMux) Close() error {
	pMux.signalClose()
	pMux.finishClose()
	return nil
}

func (pMux *PortMux) signalClose() {
	pMux.closeOnce.Do(func() {
		pMux.mu.Lock()
		pMux.isClose = true
		listener := pMux.Listener
		pMux.mu.Unlock()
		close(pMux.done)
		if listener != nil {
			_ = listener.Close()
		}
	})
}

func (pMux *PortMux) finishClose() {
	pMux.wg.Wait()
	pMux.channelOnce.Do(func() {
		for _, listener := range pMux.listeners {
			close(listener)
		}
	})
}

func (pMux *PortMux) listenerAddr() net.Addr {
	pMux.mu.RLock()
	defer pMux.mu.RUnlock()
	if pMux.Listener == nil {
		return nil
	}
	return pMux.Listener.Addr()
}

func (pMux *PortMux) GetClientListener() net.Listener {
	return pMux.getListener(listenerClient)
}

func (pMux *PortMux) GetClientTLSListener() net.Listener {
	return pMux.getListener(listenerClientTLS)
}

func (pMux *PortMux) GetClientWSListener() net.Listener {
	return pMux.getListener(listenerClientWS)
}

func (pMux *PortMux) GetClientWSSListener() net.Listener {
	return pMux.getListener(listenerClientWSS)
}

func (pMux *PortMux) GetHttpListener() net.Listener {
	return pMux.getListener(listenerHTTP)
}

func (pMux *PortMux) GetHttpsListener() net.Listener {
	return pMux.getListener(listenerHTTPS)
}

func (pMux *PortMux) GetManagerListener() net.Listener {
	return pMux.getListener(listenerManager)
}

func (pMux *PortMux) GetManagerTLSListener() net.Listener {
	return pMux.getListener(listenerManagerTLS)
}
