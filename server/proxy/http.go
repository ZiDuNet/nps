package proxy

import (
	"bufio"
	"crypto/tls"
	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/cache"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/goroutine"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/server/traffic"
	"ehang.io/nps/web"
	"errors"
	"fmt"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

type httpServer struct {
	BaseServer
	bridgeServer  *bridge.Bridge
	lifecycleMu   sync.Mutex
	started       bool
	closed        bool
	httpPort      int
	httpsPort     int
	httpServer    *http.Server
	httpsServer   *http.Server
	httpListener  net.Listener
	httpsListener net.Listener
	http3Port     int
	http3Packet   net.PacketConn
	http3Server   http3Service
	httpsProxy    *HttpsServer
	acmeManager   *autocert.Manager
	useCache      bool
	addOrigin     bool
	cache         *cache.Cache
	cacheLen      int
}

// http3Service is the lifecycle surface shared by the optional HTTP/3
// listener. Keeping it private lets the rest of the proxy remain independent
// from the concrete quic-go server implementation.
type http3Service interface {
	Close() error
}

const (
	httpResponseWaitTimeout = 5 * time.Second
	httpReadHeaderTimeout   = 10 * time.Second
	httpIdleTimeout         = 60 * time.Second
)

var httpProxyHandshakeTimeout = 10 * time.Second

// hostRequestReadTimeout is the idle window used while waiting for a subsequent
// request on a hijacked Host connection. It is cleared only after the upstream
// response is identified as an SSE stream; finite responses keep this bound.
var hostRequestReadTimeout = httpReadHeaderTimeout

// waitHTTPResponse bounds the hand-off wait used by keep-alive Host changes.
// A client that stops reading can otherwise leave CopyBuffer blocked in c.Write
// forever even after the upstream stream is closed. Closing the client socket
// on timeout makes the response goroutine observable and lets the handler exit.
func waitHTTPResponse(wg *sync.WaitGroup, c *conn.Conn) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(httpResponseWaitTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		logs.Warn("timeout waiting for HTTP upstream response; closing client connection")
		_ = c.Close()
		return false
	}
}

func NewHttp(bridge *bridge.Bridge, c *file.Tunnel, httpPort, httpsPort int, useCache bool, cacheLen int, addOrigin bool, http3Ports ...int) *httpServer {
	http3Port := 0
	if len(http3Ports) > 0 {
		http3Port = http3Ports[0]
	}
	httpServer := &httpServer{
		BaseServer: BaseServer{
			task:   c,
			bridge: bridge,
			Mutex:  sync.Mutex{},
		},
		bridgeServer: bridge,
		httpPort:     httpPort,
		httpsPort:    httpsPort,
		http3Port:    http3Port,
		useCache:     useCache,
		cacheLen:     cacheLen,
		addOrigin:    addOrigin,
	}
	if useCache {
		httpServer.cache = cache.New(cacheLen)
	}
	return httpServer
}

// HTTP3Listening reports whether the shared HTTP/3/Bridge UDP listener is
// active. Configuration alone is not enough: the listener is skipped when the
// HTTPS entry is disabled or when binding the UDP port fails.
func (s *httpServer) HTTP3Listening() bool {
	if s == nil {
		return false
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return !s.closed && s.http3Server != nil
}

func (s *httpServer) Start() error {
	if s == nil {
		return errors.New("http server is nil")
	}
	var err error
	if s.errorContent, err = web.ReadStaticFile("page/error.html"); err != nil {
		s.errorContent = []byte("nps 404")
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return net.ErrClosed
	}
	if s.started {
		s.lifecycleMu.Unlock()
		return errors.New("http server already started")
	}
	s.started = true
	if err := s.initACME(); err != nil {
		logs.Warn("ACME 自动证书未启用: %v", err)
	}
	if s.httpPort > 0 {
		var listener net.Listener
		listener, err = connection.GetHttpListener()
		if err != nil {
			s.closed = true
			s.lifecycleMu.Unlock()
			return err
		}
		httpSrv := s.NewServer(s.httpPort, "http")
		s.httpListener = listener
		s.httpServer = httpSrv
		go func() {
			if serveErr := httpSrv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				logs.Error(serveErr)
			}
		}()
	}
	if s.httpsPort > 0 {
		var listener net.Listener
		listener, err = connection.GetHttpsListener()
		if err != nil {
			httpSrv, httpListener := s.httpServer, s.httpListener
			s.httpServer, s.httpListener = nil, nil
			s.closed = true
			s.lifecycleMu.Unlock()
			if httpSrv != nil {
				_ = httpSrv.Close()
			}
			if httpListener != nil {
				_ = httpListener.Close()
			}
			return err
		}
		httpsSrv := s.NewServer(s.httpsPort, "https")
		httpsProxy := NewHttpsServer(listener, s.bridge, s.useCache, s.cacheLen)
		// Carry the shared HTTP configuration into the custom HTTPS acceptor.
		// The HTTPS path performs SNI inspection before handing a connection to
		// net/http, so it otherwise would not see ACME/HTTP3 settings.
		httpsProxy.bridge = s.bridge
		httpsProxy.task = s.task
		httpsProxy.errorContent = s.errorContent
		httpsProxy.httpPort = s.httpPort
		httpsProxy.httpsPort = s.httpsPort
		httpsProxy.http3Port = s.http3Port
		httpsProxy.addOrigin = s.addOrigin
		httpsProxy.acmeManager = s.acmeManager
		s.httpsListener = listener
		s.httpsServer = httpsSrv
		s.httpsProxy = httpsProxy
		go func() {
			if serveErr := httpsProxy.Start(); serveErr != nil {
				logs.Error(serveErr)
				_ = s.Close()
			}
		}()
	}
	if s.http3Port > 0 {
		if s.httpsPort <= 0 {
			logs.Warn("HTTP/3 反代需要启用 HTTPS 入口，已跳过 UDP %d", s.http3Port)
		} else if packetConn, http3Server, listenErr := s.startHTTP3(); listenErr != nil {
			logs.Warn("HTTP/3 反代启动失败，继续提供 HTTP/HTTPS: %v", listenErr)
		} else {
			s.http3Packet = packetConn
			s.http3Server = http3Server
		}
	}
	s.lifecycleMu.Unlock()
	return nil
}

func (s *httpServer) Close() error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.closed = true
	httpsProxy, httpsListener, httpsServer := s.httpsProxy, s.httpsListener, s.httpsServer
	http3Server := s.http3Server
	httpServer, httpListener := s.httpServer, s.httpListener
	s.httpsProxy, s.httpsListener, s.httpsServer = nil, nil, nil
	s.httpServer, s.httpListener = nil, nil
	s.http3Server, s.http3Packet = nil, nil
	s.lifecycleMu.Unlock()

	var closeErr error
	if httpsProxy != nil {
		closeErr = httpsProxy.Close()
	}
	if http3Server != nil {
		if err := http3Server.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	// The concrete HTTP/3 service owns the UDP packet connection. Do not close
	// it a second time here: quic-go reports the second close as net.ErrClosed,
	// which used to make an otherwise clean shutdown look like a proxy failure.
	if httpsServer != nil {
		if err := httpsServer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if httpsListener != nil {
		if err := httpsListener.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if httpServer != nil {
		if err := httpServer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if httpListener != nil {
		if err := httpListener.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (s *httpServer) handleTunneling(w http.ResponseWriter, r *http.Request) {

	var host *file.Host
	var err error
	host, err = file.GetDb().GetInfoByHost(r.Host, r)
	if err != nil {
		logs.Debug("the url %s %s %s can't be parsed!", r.URL.Scheme, r.Host, r.RequestURI)
		return
	}
	_, _, _, validHost := snapshotHostProxyParts(host)
	if !validHost {
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}
	host.RLock()
	autoHTTPS, hostName := host.AutoHttps, host.Host
	targetIsHTTPS, httpsJustProxy, tlsOffload := host.TargetIsHTTPS, host.HTTPSJustProxy, host.TLSOffload
	redirectURL, compatMode := host.RedirectURL, host.CompatMode
	host.RUnlock()
	if httpsJustProxy {
		// The HTTPS listener handles opaque TLS separately. If a client reaches
		// this HTTP handler with that rule, keep the response deterministic.
		compatMode = true
	}

	// 自动 http 301 https
	if autoHTTPS && r.TLS == nil {
		http.Redirect(w, r, "https://"+hostName+":"+beego.AppConfig.String("https_proxy_port"), http.StatusMovedPermanently)
		return
	}
	if redirect := redirectLocation(r, redirectURL); redirect != "" {
		http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
		return
	}

	if r.ProtoMajor >= 2 || r.Header.Get("Upgrade") != "" || compatMode || targetIsHTTPS || tlsOffload {
		rProxy := NewHttpReverseProxy(s)
		rProxy.ServeHTTP(w, r)
	} else {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
			return
		}
		c, rw, err := hijacker.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}

		s.handleHttp(conn.NewConn(c), r, rw.Reader)
	}

}

func (s *httpServer) handleHttp(c *conn.Conn, r *http.Request, br *bufio.Reader) {
	var (
		host            *file.Host
		target          net.Conn
		err             error
		connClient      io.ReadWriteCloser
		scheme          = r.URL.Scheme
		lk              *conn.Link
		targetAddr      string
		lenConn         *conn.LenConn
		isReset         atomic.Bool
		wg              sync.WaitGroup
		remoteAddr      string
		accountedClient *file.Client
		hostClient      *file.Client
		hostTarget      *file.Target
		hostFlow        *file.Flow
		clientFlow      *file.Flow
		validHost       bool
		clientConfig    file.Config
		hostLocalProxy  bool
		hostName        string
		hostChange      string
		headerChange    string
		pathRewrite     string
		registeredOnly  bool
		failureContent  = s.errorContent
	)
	responseStarted := false
	responseWaitOnExit := false
	waitForResponse := func() bool {
		if responseStarted {
			completed := waitHTTPResponse(&wg, c)
			responseStarted = false
			responseWaitOnExit = false
			return completed
		}
		return true
	}
	releaseClientConn := func() {
		if accountedClient == nil {
			return
		}
		accountedClient.AddConn()
		accountedClient = nil
	}
	defer func() {
		// A request-side EOF does not guarantee that the upstream response has
		// finished. Wait before closing the hijacked connection so the final
		// response bytes (including a terminating chunk) reach the client.
		if responseWaitOnExit {
			waitForResponse()
		}
		releaseClientConn()
		if connClient != nil {
			connClient.Close()
		} else {
			s.writeConnFailContent(c.Conn, failureContent)
		}
		c.Close()
	}()
	firstReq := true
	var responseDone chan struct{}
	var responseStreaming chan struct{}
reset:
	responseDone = make(chan struct{})
	responseStreaming = make(chan struct{})
	requestQueue := newRequestQueue()
	remoteAddr = effectiveHTTPRemoteAddr(r)
	if strings.TrimSpace(remoteAddr) == "" {
		remoteAddr = c.RemoteAddr().String()
	}
	// Keep every downstream decision and the bridge link on the same visitor
	// address.  In a Docker + Nginx deployment Request.RemoteAddr is normally
	// the container bridge peer, while the effective address comes from the
	// explicitly enabled forwarding headers.
	r.RemoteAddr = remoteAddr

	// 判断访问地址是否在全局黑名单内
	if IsGlobalBlackIp(remoteAddr) {
		c.Close()
		return
	}

	if host, err = file.GetDb().GetInfoByHost(r.Host, r); err != nil {
		logs.Notice("the url %s %s %s can't be parsed!, host %s, url %s, remote address %s", r.URL.Scheme, r.Host, r.RequestURI, r.Host, r.URL.Path, remoteAddr)
		c.Close()
		return
	}
	hostClient, hostTarget, hostFlow, validHost = snapshotHostProxyParts(host)
	if !validHost || hostFlow == nil {
		logs.Warn("reject malformed host %q", r.Host)
		c.Close()
		return
	}
	if cfg, ok := snapshotClientConfig(hostClient); ok {
		clientConfig = cfg
	} else {
		c.Close()
		return
	}
	hostClient.RLock()
	clientFlow = hostClient.Flow
	hostClient.RUnlock()
	hostTarget.RLock()
	hostLocalProxy = hostTarget.LocalProxy
	hostTarget.RUnlock()
	host.RLock()
	hostName, hostChange, headerChange = host.Host, host.HostChange, host.HeaderChange
	targetIsHTTPS := host.TargetIsHTTPS
	pathRewrite = host.PathRewrite
	registeredOnly = host.RegisteredOnly
	host.RUnlock()
	if SourceDeniedString(remoteAddr, hostClient, &host.SourceLists, "host "+strconv.Itoa(host.Id)) {
		c.Close()
		return
	}
	if isClientBlackBlocked(hostClient, remoteAddr) {
		c.Close()
		return
	}
	if isRegisteredOnlyBlocked(hostClient, remoteAddr, registeredOnly) {
		logs.Warn("http host access denied: IP %s is not registered", remoteAddr)
		c.Close()
		return
	}

	if err := s.CheckFlowAndConnNum(hostClient); err != nil {
		hostClient.RLock()
		clientID := hostClient.Id
		hostClient.RUnlock()
		logs.Warn("client id %d, host id %d, error %s, when https connection", clientID, host.Id, err.Error())
		c.Close()
		return
	}
	// Hold exactly one connection slot for the host currently serving this
	// keep-alive stream. The slot is released when the host changes or when the
	// stream exits; this avoids accounting against the newly selected host.
	accountedClient = hostClient
	if err = s.authHostRequest(r, c, host, clientConfig.U, clientConfig.P); err != nil {
		logs.Warn("auth error", err, r.RemoteAddr)
		return
	}
	if targetAddr, err = hostTarget.GetRandomTarget(); err != nil {
		logs.Warn(err.Error())
		return
	}

	hostClient.RLock()
	clientID := hostClient.Id
	clientRate := hostClient.Rate
	hostClient.RUnlock()
	lk = conn.NewLink("http", targetAddr, clientConfig.Crypt, clientConfig.Compress, r.RemoteAddr, hostLocalProxy, hostProxyProtocolVersion(host))
	if target, err = s.bridge.SendLinkInfo(clientID, lk, nil); err != nil {
		logs.Notice("connect to target %s error %s", lk.Host, err)
		return
	}
	if target == nil {
		logs.Warn("bridge returned nil HTTP target for %s", lk.Host)
		return
	}
	connClient = conn.GetConnWithUserLimits(target, lk.Crypt, lk.Compress, clientRate, true, nil, host, file.UserRateLimiterForClient(hostClient))
	if targetIsHTTPS {
		sni := hostName
		if hostChange != "" {
			sni = hostChange
		}
		tlsConn, tlsErr := conn.GetTlsConn(readWriteCloserAsNetConn(connClient), sni)
		if tlsErr != nil {
			logs.Warn("HTTPS backend handshake failed for host %s target %s: %v", hostName, targetAddr, tlsErr)
			return
		}
		connClient = tlsConn
	}
	currentHost := host

	// Read response bytes from the client-side target connection.
	isReset.Store(false)
	wg.Add(1)
	responseStarted = true
	responseWaitOnExit = r.Close || strings.EqualFold(strings.TrimSpace(r.Header.Get("Connection")), "close")
	go func(targetConn io.ReadWriteCloser, requestHost *file.Host, streaming, done chan struct{}) {
		defer targetConn.Close()
		defer func() {
			if !isReset.Load() {
				c.Close()
			}
			wg.Done()
			close(done)
		}()

		probe := newResponseInspector(newResponseHeaderTransform(targetConn, requestHost), traffic.Resource{Kind: "host", ID: currentHost.Id}, requestQueue, streaming)
		if _, err1 := goroutine.CopyBufferWithFlowsDirection(c, probe, hostFlow, []*file.Flow{clientFlow}, nil, requestHost, "", goroutine.FlowOutbound); err1 != nil {
			return
		}
	}(connClient, currentHost, responseStreaming, responseDone)

	for {
		var (
			resource     traffic.Resource
			requestID    string
			requestEvent traffic.Event
			requestBody  *traffic.BodyCapture
		)
		//if the cache start and the request is in the cache list, return the cache
		if s.useCache {
			if v, ok := s.cache.Get(filepath.Join(hostName, r.URL.Path)); ok {
				n, err := c.Write(v.([]byte))
				if err != nil {
					break
				}
				logs.Trace("%s request, method %s, host %s, url %s, remote address %s, return cache", r.URL.Scheme, r.Method, r.Host, r.URL.Path, c.RemoteAddr().String())
				host.RLock()
				hostFlowForCache := host.Flow
				host.RUnlock()
				s.FlowAddHostOutbound(host, int64(n))
				if clientFlow != nil && clientFlow != hostFlowForCache {
					clientFlow.Add(0, int64(n))
				}
				//if return cache and does not create a new conn with client and Connection is not set or close, close the connection.
				if strings.ToLower(r.Header.Get("Connection")) == "close" || strings.ToLower(r.Header.Get("Connection")) == "" {
					break
				}
				goto readReq
			}
		}

		//change the host and header and set proxy setting
		rewriteHostRequestPath(r, pathRewrite)
		common.ChangeHostAndHeader(r, hostChange, headerChange, c.Conn.RemoteAddr().String())

		logs.Info("%s request, method %s, host %s, url %s, remote address %s, target %s", r.URL.Scheme, r.Method, r.Host, r.URL.Path, remoteAddr, lk.Host)
		resource = traffic.Resource{Kind: "host", ID: host.Id}
		requestID = traffic.NewRequestID()
		requestEvent = traffic.Event{
			Type:         "request_start",
			Time:         time.Now().UTC(),
			Resource:     resource,
			RequestID:    requestID,
			Method:       r.Method,
			Scheme:       r.URL.Scheme,
			Host:         r.Host,
			Path:         r.URL.RequestURI(),
			RemoteAddr:   c.RemoteAddr().String(),
			ForwardedFor: remoteAddr,
			ListenerAddr: c.LocalAddr().String(),
			TargetAddr:   targetAddr,
			ContentType:  r.Header.Get("Content-Type"),
			Headers:      traffic.SafeHeaders(r.Header),
		}
		requestBody = traffic.NewBodyCapture(resource, requestID, "request", r.Header.Get("Content-Type"), r.Header.Get("Content-Disposition"))
		if traffic.HasSubscribers(resource) {
			requestEvent.BodySkipped = !requestBody.Active() && r.ContentLength != 0
		}
		traffic.Publish(requestEvent)
		requestQueue.add(requestEvent)

		//write
		lenConn = conn.NewLenConn(connClient)
		//lenConn = conn.LenConn
		if err = c.SetReadDeadline(time.Now().Add(httpProxyHandshakeTimeout)); err != nil {
			return
		}
		if firstReq {
			if err = writeRequestRaw(lenConn, r, br, requestBody); err != nil {
				logs.Error(err)
				break
			}
		} else {
			if r.Body != nil && requestBody.Active() {
				r.Body = &captureReadCloser{ReadCloser: r.Body, capture: requestBody}
			}
			if err = r.Write(lenConn); err != nil {
				logs.Error(err)
				break
			}
			requestBody.End()
		}
		// Keep the header deadline active while writeRequestRaw forwards the
		// request body. A client that drips a body must not hold a hijacked
		// connection forever.
		_ = c.SetReadDeadline(time.Time{})
		firstReq = false
		if clientFlow != nil {
			clientFlow.Add(int64(lenConn.Len), 0)
		}
		s.FlowAddHostInbound(host, int64(lenConn.Len))
		traffic.Publish(traffic.Event{
			Type:       "request_sent",
			Time:       time.Now().UTC(),
			Resource:   resource,
			RequestID:  requestID,
			Method:     r.Method,
			Host:       r.Host,
			Path:       r.URL.RequestURI(),
			TargetAddr: targetAddr,
			BytesIn:    int64(lenConn.Len),
			Complete:   true,
		})

	readReq:
		//read req from connection
		if err := c.SetReadDeadline(time.Now().Add(hostRequestReadTimeout)); err != nil {
			return
		}
		r, err = http.ReadRequest(br)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Prefer a completed upstream response when both channels become
				// ready together. Otherwise a finite response could randomly enter
				// the streaming branch and keep the handler blocked on a new request.
				select {
				case <-responseDone:
					return
				default:
				}
				select {
				case <-responseDone:
					return
				case <-responseStreaming:
					select {
					case <-responseDone:
						return
					default:
					}
					// An explicit SSE response owns this connection. Continue
					// waiting for a client request or disconnect without an
					// artificial deadline. Other response types retain the
					// bounded idle timeout above.
					if deadlineErr := c.SetReadDeadline(time.Time{}); deadlineErr != nil {
						return
					}
					r, err = http.ReadRequest(br)
				default:
					return
				}
			}
			if err != nil {
				return
			}
		}
		r.URL.Scheme = scheme
		if hostTmp, err := file.GetDb().GetInfoByHost(r.Host, r); err != nil {
			logs.Notice("the url %s %s %s can't be parsed!", r.URL.Scheme, r.Host, r.RequestURI)
			break
		} else if host != hostTmp {
			isReset.Store(true)
			connClient.Close()
			if !waitForResponse() {
				return
			}
			releaseClientConn()
			host = hostTmp
			goto reset
		}
	}
	waitForResponse()
}

func writeRequestRaw(w io.Writer, r *http.Request, br *bufio.Reader, capture *traffic.BodyCapture) error {
	bw := bufio.NewWriter(w)
	if _, err := fmt.Fprintf(bw, "%s %s HTTP/1.1\r\n", r.Method, r.URL.RequestURI()); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(bw, "Host: %s\r\n", r.Host); err != nil {
		return err
	}
	chunked := len(r.TransferEncoding) > 0 && r.TransferEncoding[0] == "chunked"
	if chunked {
		if _, err := io.WriteString(bw, "Transfer-Encoding: chunked\r\n"); err != nil {
			return err
		}
	}
	if err := r.Header.Write(bw); err != nil {
		return err
	}
	if _, err := io.WriteString(bw, "\r\n"); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if r.ContentLength > 0 {
		if err := copyFixedBody(w, br, r.ContentLength, capture); err != nil {
			return err
		}
	} else if chunked {
		if err := copyRawChunked(w, br, capture); err != nil {
			return err
		}
	}
	if capture != nil {
		capture.End()
	}
	return nil
}

func copyFixedBody(w io.Writer, br io.Reader, length int64, capture *traffic.BodyCapture) error {
	buf := make([]byte, 32*1024)
	remaining := length
	for remaining > 0 {
		want := int64(len(buf))
		if want > remaining {
			want = remaining
		}
		nr, readErr := io.ReadFull(br, buf[:want])
		if nr > 0 {
			nw, writeErr := w.Write(buf[:nr])
			if nw > 0 && capture != nil {
				capture.Write(buf[:nw])
			}
			if writeErr != nil {
				return writeErr
			}
			if nw != nr {
				return io.ErrShortWrite
			}
			remaining -= int64(nr)
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func copyRawChunked(w io.Writer, br *bufio.Reader, capture *traffic.BodyCapture) error {
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
		sizeStr := strings.TrimSpace(line)
		if i := strings.IndexByte(sizeStr, ';'); i >= 0 {
			sizeStr = sizeStr[:i]
		}
		size, err := strconv.ParseInt(sizeStr, 16, 64)
		if err != nil {
			return err
		}
		if size == 0 {
			for {
				tline, err := br.ReadString('\n')
				if err != nil {
					return err
				}
				if _, err := io.WriteString(w, tline); err != nil {
					return err
				}
				if tline == "\r\n" || tline == "\n" {
					return nil
				}
			}
		}
		if err := copyFixedBody(w, br, size, capture); err != nil {
			return err
		}
		var crlf [2]byte
		if _, err := io.ReadFull(br, crlf[:]); err != nil {
			return err
		}
		if _, err := w.Write(crlf[:]); err != nil {
			return err
		}
	}
}

func (s *httpServer) NewServer(port int, scheme string) *http.Server {
	handler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Scheme = scheme
		s.handleTunneling(w, r)
	}))
	if scheme == "http" && s.acmeManager != nil {
		// ACME HTTP-01 challenges must be answered before Host routing. All
		// non-challenge requests fall through to the normal reverse proxy.
		handler = s.acmeManager.HTTPHandler(handler)
	}
	return &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		Handler:           handler,
	}
}

func (s *httpServer) NewServerWithTls(port int, scheme string, l net.Listener, certFile string, keyFile string) error {

	if certFile == "" || keyFile == "" {
		logs.Error("证书文件为空")
		return nil
	}
	var certFileByte = []byte(certFile)
	var keyFileByte = []byte(keyFile)

	config := &tls.Config{}
	config.Certificates = make([]tls.Certificate, 1)

	var err error
	config.Certificates[0], err = tls.X509KeyPair(certFileByte, keyFileByte)
	if err != nil {
		return err
	}

	s2 := &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.URL.Scheme = scheme
			s.handleTunneling(w, r)
		}),
		TLSConfig: config,
	}

	return s2.ServeTLS(l, "", "")
}
