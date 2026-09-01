// This module is used for port reuse
// Distinguish client, web manager , HTTP and HTTPS according to the difference of protocol
package pmux

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
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
)

type PortMux struct {
	net.Listener
	port        int
	managerHost string
	bindHost    string
	clientConn  chan *PortConn
	httpConn    chan *PortConn
	httpsConn   chan *PortConn
	managerConn chan *PortConn
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
	pMux := &PortMux{
		managerHost: managerHost,
		bindHost:    strings.TrimSpace(bindHost),
		port:        port,
		clientConn:  make(chan *PortConn),
		httpConn:    make(chan *PortConn),
		httpsConn:   make(chan *PortConn),
		managerConn: make(chan *PortConn),
		done:        make(chan struct{}),
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
	var buffer bytes.Buffer
	var readMore = false
	switch common.BytesToNum(buf) {
	case HTTP_CONNECT, HTTP_DELETE, HTTP_GET, HTTP_HEAD, HTTP_OPTIONS, HTTP_POST, HTTP_PUT, HTTP_TRACE: //http and manager
		// HTTP 分支刷新超时
		conn.SetReadDeadline(time.Now().Add(ACCEPT_TIME_OUT * time.Second))
		buffer.Reset()
		r := bufio.NewReader(conn)
		buffer.Write(buf)
		for {
			b, isPrefix, err := r.ReadLine()
			if err != nil {
				logs.Warn("read line error", err.Error())
				conn.Close()
				return
			}
			if buffer.Len()+len(b) > maxHeaderBytes {
				logs.Warn("pmux request header too large")
				conn.Close()
				return
			}
			buffer.Write(b)
			if isPrefix {
				continue
			}
			buffer.Write([]byte("\r\n"))
			const hostPrefixLen = len("Host:")
			if len(b) >= hostPrefixLen && strings.EqualFold(string(b[:hostPrefixLen]), "Host:") {
				// Remove host and space effects
				str := strings.TrimSpace(string(b[hostPrefixLen:]))
				// Determine whether it is the same as the manager domain name
				if common.GetIpByAddr(str) == pMux.managerHost {
					ch = pMux.managerConn
				} else {
					ch = pMux.httpConn
				}
				b, _ := r.Peek(r.Buffered())
				buffer.Write(b)
				rs = buffer.Bytes()
				break
			}
		}
		// HTTP 分支读完Header后清除超时
		conn.SetReadDeadline(time.Time{})
	case CLIENT: // client connection
		ch = pMux.clientConn
	default: // https
		readMore = true
		ch = pMux.httpsConn
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
		close(pMux.clientConn)
		close(pMux.httpsConn)
		close(pMux.httpConn)
		close(pMux.managerConn)
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
	return NewPortListener(pMux.clientConn, pMux.listenerAddr())
}

func (pMux *PortMux) GetHttpListener() net.Listener {
	return NewPortListener(pMux.httpConn, pMux.listenerAddr())
}

func (pMux *PortMux) GetHttpsListener() net.Listener {
	return NewPortListener(pMux.httpsConn, pMux.listenerAddr())
}

func (pMux *PortMux) GetManagerListener() net.Listener {
	return NewPortListener(pMux.managerConn, pMux.listenerAddr())
}
