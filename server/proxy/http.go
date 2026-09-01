package proxy

import (
	"bufio"
	"crypto/subtle"
	"crypto/tls"
	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/cache"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/goroutine"
	"ehang.io/nps/server/connection"
	"ehang.io/nps/web"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
	"html"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func constantTimeSecretEqual(stored, supplied string) bool {
	return subtle.ConstantTimeCompare([]byte(html.UnescapeString(stored)), []byte(supplied)) == 1
}

// formatHTTPFailure builds a complete response for requests that are handled
// directly on the hijacked connection (for example, the IP allowlist page).
// writeConnFailContent intentionally prepends the legacy 404 marker for proxy
// failures, so these responses must bypass that wrapper.
func formatHTTPFailure(status int, contentType string, body []byte) []byte {
	statusText := http.StatusText(status)
	if statusText == "" {
		statusText = "Error"
	}
	return []byte(fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, statusText, contentType, len(body), body))
}

type httpServer struct {
	BaseServer
	lifecycleMu   sync.Mutex
	started       bool
	closed        bool
	httpPort      int
	httpsPort     int
	httpServer    *http.Server
	httpsServer   *http.Server
	httpListener  net.Listener
	httpsListener net.Listener
	httpsProxy    *HttpsServer
	useCache      bool
	addOrigin     bool
	cache         *cache.Cache
	cacheLen      int
}

const (
	httpResponseWaitTimeout = 5 * time.Second
	httpReadHeaderTimeout   = 10 * time.Second
	httpIdleTimeout         = 60 * time.Second
)

var httpProxyHandshakeTimeout = 10 * time.Second

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

func NewHttp(bridge *bridge.Bridge, c *file.Tunnel, httpPort, httpsPort int, useCache bool, cacheLen int, addOrigin bool) *httpServer {
	httpServer := &httpServer{
		BaseServer: BaseServer{
			task:   c,
			bridge: bridge,
			Mutex:  sync.Mutex{},
		},
		httpPort:  httpPort,
		httpsPort: httpsPort,
		useCache:  useCache,
		cacheLen:  cacheLen,
		addOrigin: addOrigin,
	}
	if useCache {
		httpServer.cache = cache.New(cacheLen)
	}
	return httpServer
}

func (s *httpServer) ipWhiteResponse(c *conn.Conn, r *http.Request, client *file.Client) ([]byte, bool) {
	if client == nil {
		return nil, false
	}
	ip := common.GetIpByAddr(c.RemoteAddr().String())
	if r.URL.Path == "/authIp" {
		pass := ""
		if r.Method == http.MethodPost {
			if parseErr := r.ParseForm(); parseErr == nil {
				// Read credentials from the request body only. Query-string
				// credentials are retained by access logs and browser history.
				pass = r.PostForm.Get("pass")
			}
		}
		client.RLock()
		whitePass := client.IpWhitePass
		client.RUnlock()
		var body []byte
		if pass != "" && constantTimeSecretEqual(whitePass, pass) {
			changed := false
			client.Lock()
			alreadyAllowed := false
			for _, existingIP := range client.IpWhiteList {
				if existingIP == ip {
					alreadyAllowed = true
					break
				}
			}
			if !alreadyAllowed {
				client.IpWhiteList = append(client.IpWhiteList, ip)
				changed = true
			}
			client.Unlock()
			if changed {
				file.GetDb().JsonDb.StoreClientsToJsonFile()
			}
			logs.Info("客户端IP白名单认证授权成功:client_id [%d] ip [%s]", client.Id, ip)
			body, _ = json.Marshal(map[string]interface{}{"success": true, "message": "授权成功"})
		} else {
			logs.Error("客户端IP白名单认证失败:client_id [%d] ip [%s]", client.Id, ip)
			body, _ = json.Marshal(map[string]interface{}{"success": false, "message": "密码错误或请求格式错误"})
		}
		return formatHTTPFailure(http.StatusOK, "application/json", body), true
	}

	errorContent, _ := web.ReadStaticFile("page/auth.html")
	authHTML := strings.ReplaceAll(string(errorContent), "${ip}", ipWhiteChallengeIP(c.RemoteAddr().String()))
	return formatHTTPFailure(http.StatusUnauthorized, "text/html; charset=utf-8", []byte(authHTML)), true
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
	httpServer, httpListener := s.httpServer, s.httpListener
	s.httpsProxy, s.httpsListener, s.httpsServer = nil, nil, nil
	s.httpServer, s.httpListener = nil, nil
	s.lifecycleMu.Unlock()

	var closeErr error
	if httpsProxy != nil {
		closeErr = httpsProxy.Close()
	}
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
	host.RUnlock()

	// 自动 http 301 https
	if autoHTTPS && r.TLS == nil {
		http.Redirect(w, r, "https://"+hostName+":"+beego.AppConfig.String("https_proxy_port"), http.StatusMovedPermanently)
		return
	}

	if r.Header.Get("Upgrade") != "" {
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
		failureContent  = s.errorContent
		failureRaw      bool
	)
	releaseClientConn := func() {
		if accountedClient == nil {
			return
		}
		accountedClient.AddConn()
		accountedClient = nil
	}
	defer func() {
		releaseClientConn()
		if connClient != nil {
			connClient.Close()
		} else if failureRaw {
			_, _ = c.Conn.Write(failureContent)
		} else {
			s.writeConnFailContent(c.Conn, failureContent)
		}
		c.Close()
	}()
	firstReq := true
reset:
	remoteAddr = strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if len(remoteAddr) == 0 {
		remoteAddr = c.RemoteAddr().String()
	}

	// 判断访问地址是否在全局黑名单内
	if IsGlobalBlackIp(c.RemoteAddr().String()) {
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
	host.RUnlock()
	if isIPWhiteBlocked(hostClient, c.RemoteAddr().String()) {
		failureContent, failureRaw = s.ipWhiteResponse(c, r, hostClient)
		return
	}
	if isClientBlackBlocked(hostClient, c.RemoteAddr().String()) {
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
	if err = s.auth(r, c, clientConfig.U, clientConfig.P); err != nil {
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
	lk = conn.NewLink("http", targetAddr, clientConfig.Crypt, clientConfig.Compress, r.RemoteAddr, hostLocalProxy, "")
	if target, err = s.bridge.SendLinkInfo(clientID, lk, nil); err != nil {
		logs.Notice("connect to target %s error %s", lk.Host, err)
		return
	}
	if target == nil {
		logs.Warn("bridge returned nil HTTP target for %s", lk.Host)
		return
	}
	connClient = conn.GetConn(target, lk.Crypt, lk.Compress, clientRate, true)
	currentHost := host

	// Read response bytes from the client-side target connection.
	isReset.Store(false)
	wg.Add(1)
	go func(targetConn io.ReadWriteCloser, requestHost *file.Host) {
		defer targetConn.Close()
		defer func() {
			if !isReset.Load() {
				c.Close()
			}
			wg.Done()
		}()

		if err1 := goroutine.CopyBuffer(c, targetConn, hostFlow, nil, requestHost, ""); err1 != nil {
			return
		}
	}(connClient, currentHost)

	for {
		//if the cache start and the request is in the cache list, return the cache
		if s.useCache {
			if v, ok := s.cache.Get(filepath.Join(hostName, r.URL.Path)); ok {
				n, err := c.Write(v.([]byte))
				if err != nil {
					break
				}
				logs.Trace("%s request, method %s, host %s, url %s, remote address %s, return cache", r.URL.Scheme, r.Method, r.Host, r.URL.Path, c.RemoteAddr().String())
				if clientFlow != nil {
					clientFlow.Add(int64(n), int64(n))
				}
				s.FlowAddHost(host, int64(n), int64(n))
				//if return cache and does not create a new conn with client and Connection is not set or close, close the connection.
				if strings.ToLower(r.Header.Get("Connection")) == "close" || strings.ToLower(r.Header.Get("Connection")) == "" {
					break
				}
				goto readReq
			}
		}

		//change the host and header and set proxy setting
		common.ChangeHostAndHeader(r, hostChange, headerChange, c.Conn.RemoteAddr().String())

		logs.Info("%s request, method %s, host %s, url %s, remote address %s, target %s", r.URL.Scheme, r.Method, r.Host, r.URL.Path, remoteAddr, lk.Host)

		//write
		lenConn = conn.NewLenConn(connClient)
		//lenConn = conn.LenConn
		if err = c.SetReadDeadline(time.Now().Add(httpProxyHandshakeTimeout)); err != nil {
			return
		}
		if firstReq {
			if err = writeRequestRaw(lenConn, r, br); err != nil {
				logs.Error(err)
				break
			}
		} else {
			if err = r.Write(lenConn); err != nil {
				logs.Error(err)
				break
			}
		}
		// Keep the header deadline active while writeRequestRaw forwards the
		// request body. A client that drips a body must not hold a hijacked
		// connection forever.
		_ = c.SetReadDeadline(time.Time{})
		firstReq = false
		if clientFlow != nil {
			clientFlow.Add(int64(lenConn.Len), int64(lenConn.Len))
		}
		s.FlowAddHost(host, int64(lenConn.Len), int64(lenConn.Len))

	readReq:
		//read req from connection
		if err := c.SetReadDeadline(time.Now().Add(httpReadHeaderTimeout)); err != nil {
			return
		}
		r, err = http.ReadRequest(br)
		if err != nil {
			//break
			return
		}
		r.URL.Scheme = scheme
		if hostTmp, err := file.GetDb().GetInfoByHost(r.Host, r); err != nil {
			logs.Notice("the url %s %s %s can't be parsed!", r.URL.Scheme, r.Host, r.RequestURI)
			break
		} else if host != hostTmp {
			isReset.Store(true)
			connClient.Close()
			if !waitHTTPResponse(&wg, c) {
				return
			}
			releaseClientConn()
			host = hostTmp
			goto reset
		}
	}
	waitHTTPResponse(&wg, c)
}

func writeRequestRaw(w io.Writer, r *http.Request, br *bufio.Reader) error {
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
		if _, err := io.CopyN(w, br, r.ContentLength); err != nil {
			return err
		}
	} else if chunked {
		if err := copyRawChunked(w, br); err != nil {
			return err
		}
	}
	return nil
}

func copyRawChunked(w io.Writer, br *bufio.Reader) error {
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
		if _, err := io.CopyN(w, br, size); err != nil {
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
	return &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		IdleTimeout:       httpIdleTimeout,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.URL.Scheme = scheme
			s.handleTunneling(w, r)
		}),
		// Disable HTTP/2.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
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
		// Disable HTTP/2.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		TLSConfig:    config,
	}

	return s2.ServeTLS(l, "", "")
}
