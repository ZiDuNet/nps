package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/goroutine"
	"ehang.io/nps/lib/rate"
	"github.com/astaxie/beego/logs"
)

type HTTPError struct {
	error
	HTTPCode int
}

type HttpReverseProxy struct {
	proxy                 *ReverseProxy
	server                *httpServer
	responseHeaderTimeout time.Duration
}

// reverseProxyState is captured before handing a request to the transport.
// Host/client settings can be edited from the console while a request is in
// flight, so transport callbacks must not reread the mutable model objects.
type reverseProxyState struct {
	req        *http.Request
	host       *file.Host
	targetAddr string
	client     *file.Client
	clientID   int
	clientRate *rate.Rate
	clientFlow *file.Flow
	config     file.Config
	localProxy bool
}

type reverseProxyContextKey struct{}

func stateFromContext(ctx context.Context) (*reverseProxyState, error) {
	if ctx == nil {
		return nil, errors.New("reverse proxy context is nil")
	}
	state, ok := ctx.Value(reverseProxyContextKey{}).(*reverseProxyState)
	if !ok || state == nil || state.req == nil || state.host == nil || state.client == nil || state.targetAddr == "" {
		return nil, errors.New("reverse proxy request state is incomplete")
	}
	return state, nil
}

type flowConn struct {
	io.ReadWriteCloser
	fakeAddr net.Addr
	host     *file.Host
	flowIn   int64
	flowOut  int64
	once     sync.Once
}

func (rp *HttpReverseProxy) reserveClientConnection(client *file.Client) error {
	if client == nil {
		return errors.New("client is nil")
	}
	if rp.server != nil {
		return rp.server.CheckFlowAndConnNum(client)
	}
	return checkClientConnection(client)
}

func (rp *HttpReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if rw == nil || req == nil {
		return
	}
	var (
		host *file.Host
		err  error
	)
	if host, err = file.GetDb().GetInfoByHost(req.Host, req); err != nil {
		rw.WriteHeader(http.StatusNotFound)
		rw.Write([]byte(req.Host + " not found"))
		return
	}
	client, target, _, validHost := snapshotHostProxyParts(host)
	if !validHost {
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte("502 Bad Gateway"))
		return
	}
	config, ok := snapshotClientConfig(client)
	if !ok {
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte("502 Bad Gateway"))
		return
	}
	client.RLock()
	clientID, clientRate, clientFlow := client.Id, client.Rate, client.Flow
	client.RUnlock()
	target.RLock()
	localProxy := target.LocalProxy
	target.RUnlock()
	remoteAddr := req.RemoteAddr
	if IsGlobalBlackIp(remoteAddr) || isClientBlackBlocked(client, remoteAddr) {
		http.Error(rw, "IP address is blocked", http.StatusForbidden)
		return
	}
	if config.U != "" && config.P != "" && !common.CheckAuth(req, config.U, config.P) {
		rw.WriteHeader(http.StatusUnauthorized)
		rw.Write([]byte("Unauthorized"))
		return
	}
	if isIPWhiteBlocked(client, req.RemoteAddr) {
		// A WebSocket upgrade has no useful HTML challenge response. Reject it
		// before reserving a client slot or opening the remote tunnel.
		http.Error(rw, "IP address is not authorized", http.StatusForbidden)
		return
	}
	targetAddr, err := target.GetRandomTarget()
	if err != nil {
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte("502 Bad Gateway"))
		return
	}
	if err := rp.reserveClientConnection(client); err != nil {
		host.RLock()
		hostID := host.Id
		host.RUnlock()
		logs.Warn("client id %d, host id %d, error %s, when websocket connection", clientID, hostID, err.Error())
		http.Error(rw, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	defer client.AddConn()

	state := &reverseProxyState{
		req:        req,
		host:       host,
		targetAddr: targetAddr,
		client:     client,
		clientID:   clientID,
		clientRate: clientRate,
		clientFlow: clientFlow,
		config:     config,
		localProxy: localProxy,
	}
	req = req.WithContext(context.WithValue(req.Context(), reverseProxyContextKey{}, state))

	if rp.proxy == nil {
		http.Error(rw, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}
	rp.proxy.ServeHTTP(rw, req, host)
}

func (c *flowConn) Read(p []byte) (n int, err error) {
	n, err = c.ReadWriteCloser.Read(p)
	return n, err
}

func (c *flowConn) Write(p []byte) (n int, err error) {
	n, err = c.ReadWriteCloser.Write(p)
	return n, err
}

func (c *flowConn) Close() error {
	//c.once.Do(func() { c.host.Flow.Add(c.flowIn, c.flowOut) })
	if c == nil || c.ReadWriteCloser == nil {
		return nil
	}
	return c.ReadWriteCloser.Close()
}

func (c *flowConn) LocalAddr() net.Addr { return c.fakeAddr }

func (c *flowConn) RemoteAddr() net.Addr { return c.fakeAddr }

func (*flowConn) SetDeadline(t time.Time) error { return nil }

func (*flowConn) SetReadDeadline(t time.Time) error { return nil }

func (*flowConn) SetWriteDeadline(t time.Time) error { return nil }

func NewHttpReverseProxy(s *httpServer) *HttpReverseProxy {
	rp := &HttpReverseProxy{
		server:                s,
		responseHeaderTimeout: 30 * time.Second,
	}
	local, _ := net.ResolveTCPAddr("tcp", "127.0.0.1")
	proxy := NewReverseProxy(&httputil.ReverseProxy{
		Director: func(r *http.Request) {
			//host := r.Context().Value("host").(*file.Host)
			//common.ChangeHostAndHeader(r, host.HostChange, host.HeaderChange, "")
		},
		Transport: &http.Transport{
			ResponseHeaderTimeout: rp.responseHeaderTimeout,
			DisableKeepAlives:     true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				state, stateErr := stateFromContext(ctx)
				if stateErr != nil || s == nil || s.bridge == nil {
					return nil, NewHTTPError(http.StatusBadGateway, "proxy request state is unavailable")
				}
				lk := conn.NewLink("http", state.targetAddr, state.config.Crypt, state.config.Compress, state.req.RemoteAddr, state.localProxy, "")
				target, err := s.bridge.SendLinkInfo(state.clientID, lk, nil)
				if err != nil {
					logs.Notice("connect to target %s error %s", lk.Host, err)
					return nil, NewHTTPError(http.StatusBadGateway, "Cannot connect to the server")
				}
				if target == nil {
					return nil, NewHTTPError(http.StatusBadGateway, "server returned an empty connection")
				}
				connClient := conn.GetConn(target, lk.Crypt, lk.Compress, state.clientRate, true)
				return &flowConn{
					ReadWriteCloser: connClient,
					fakeAddr:        local,
					host:            state.host,
				}, nil
			},
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			logs.Warn("do http proxy request error: %v", err)
			rw.WriteHeader(http.StatusNotFound)
		},
	})
	proxy.WebSocketDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		state, stateErr := stateFromContext(ctx)
		if stateErr != nil || s == nil || s.bridge == nil {
			return nil, NewHTTPError(http.StatusBadGateway, "proxy request state is unavailable")
		}
		lk := conn.NewLink("tcp", state.targetAddr, state.config.Crypt, state.config.Compress, state.req.RemoteAddr, state.localProxy, "")
		target, err := s.bridge.SendLinkInfo(state.clientID, lk, nil)
		if err != nil {
			logs.Notice("connect to target %s error %s", lk.Host, err)
			return nil, NewHTTPError(http.StatusBadGateway, "Cannot connect to the target")
		}
		if target == nil {
			return nil, NewHTTPError(http.StatusBadGateway, "server returned an empty connection")
		}
		connClient := conn.GetConn(target, lk.Crypt, lk.Compress, state.clientRate, true)
		return &flowConn{
			ReadWriteCloser: connClient,
			fakeAddr:        local,
			host:            state.host,
		}, nil
	}
	rp.proxy = proxy
	return rp
}

func NewHTTPError(code int, errmsg string) error {
	return &HTTPError{
		error:    errors.New(errmsg),
		HTTPCode: code,
	}
}

type ReverseProxy struct {
	*httputil.ReverseProxy
	WebSocketDialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func IsWebsocketRequest(req *http.Request) bool {
	containsHeader := func(name, value string) bool {
		items := strings.Split(req.Header.Get(name), ",")
		for _, item := range items {
			if value == strings.ToLower(strings.TrimSpace(item)) {
				return true
			}
		}
		return false
	}
	return containsHeader("Connection", "upgrade") && containsHeader("Upgrade", "websocket")
}

func NewReverseProxy(orp *httputil.ReverseProxy) *ReverseProxy {
	rp := &ReverseProxy{
		ReverseProxy:         orp,
		WebSocketDialContext: nil,
	}
	rp.ErrorHandler = rp.errHandler
	return rp
}

func (p *ReverseProxy) errHandler(rw http.ResponseWriter, r *http.Request, e error) {
	if e == io.EOF {
		rw.WriteHeader(521)
		//rw.Write(getWaitingPageContent())
	} else {
		if httperr, ok := e.(*HTTPError); ok {
			rw.WriteHeader(httperr.HTTPCode)
		} else {
			rw.WriteHeader(http.StatusNotFound)
		}
		rw.Write([]byte("error: " + e.Error()))
	}
}

func (p *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request, host *file.Host) {
	if p == nil || p.ReverseProxy == nil || rw == nil || req == nil {
		return
	}
	if IsWebsocketRequest(req) {
		p.serveWebSocket(rw, req, host)
		return
	}
	http.Error(rw, "websocket upgrade required", http.StatusBadRequest)
}

func (p *ReverseProxy) serveWebSocket(rw http.ResponseWriter, req *http.Request, host *file.Host) {
	if p.WebSocketDialContext == nil {
		rw.WriteHeader(500)
		return
	}
	targetConn, err := p.WebSocketDialContext(req.Context(), "tcp", "")
	if err != nil {
		p.errHandler(rw, req, err)
		return
	}
	if targetConn == nil {
		p.errHandler(rw, req, NewHTTPError(http.StatusBadGateway, "empty target connection"))
		return
	}
	defer targetConn.Close()

	p.Director(req)

	hijacker, ok := rw.(http.Hijacker)
	if !ok {
		rw.WriteHeader(500)
		return
	}
	conn, _, errHijack := hijacker.Hijack()
	if errHijack != nil {
		rw.WriteHeader(500)
		return
	}
	defer conn.Close()

	if err := req.Write(targetConn); err != nil {
		return
	}

	flow := (*file.Flow)(nil)
	if state, stateErr := stateFromContext(req.Context()); stateErr == nil {
		flow = state.clientFlow
	}
	joinWithFlow(conn, targetConn, flow)
}

func Join(c1 io.ReadWriteCloser, c2 io.ReadWriteCloser, host *file.Host) (inCount int64, outCount int64) {
	if c1 == nil || c2 == nil {
		return
	}
	var flow *file.Flow
	if host != nil {
		host.RLock()
		client := host.Client
		host.RUnlock()
		if client != nil {
			client.RLock()
			flow = client.Flow
			client.RUnlock()
		}
	}
	return joinWithFlow(c1, c2, flow)
}

func joinWithFlow(c1 io.ReadWriteCloser, c2 io.ReadWriteCloser, flow *file.Flow) (inCount int64, outCount int64) {
	if c1 == nil || c2 == nil {
		return
	}
	var wait sync.WaitGroup
	pipe := func(to io.ReadWriteCloser, from io.ReadWriteCloser, count *int64) {
		defer to.Close()
		defer from.Close()
		defer wait.Done()
		goroutine.CopyBuffer(to, from, flow, nil, nil, "")
		//*count, _ = io.Copy(to, from)
	}

	wait.Add(2)

	go pipe(c1, c2, &inCount)
	go pipe(c2, c1, &outCount)
	wait.Wait()
	return
}
