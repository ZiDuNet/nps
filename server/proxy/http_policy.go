package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

// httpProxyPolicy contains the independently tunable HTTP forwarding limits.
// A zero duration means unlimited for timeout fields. The legacy
// http_proxy_response_timeout remains the response-header limit so existing
// deployments keep their current behaviour.
type httpProxyPolicy struct {
	clientUploadStall     time.Duration
	clientResponseStall   time.Duration
	clientUploadTotal     time.Duration
	clientResponseTotal   time.Duration
	backendConnect        time.Duration
	backendTLSHandshake   time.Duration
	backendResponseHeader time.Duration
	backendRequestStart   time.Duration
	backendUploadStall    time.Duration
	backendReadStall      time.Duration

	backendKeepAlive         bool
	backendMaxIdle           int
	backendMaxIdlePerHost    int
	backendIdleTimeout       time.Duration
	backendPoolEntries       int
	backendExpectContinue    time.Duration
	backendResponseBodyTotal time.Duration
}

func loadHTTPProxyPolicy() httpProxyPolicy {
	return httpProxyPolicy{
		clientUploadStall:     configSeconds("http_client_upload_stall_timeout", 60),
		clientResponseStall:   configSeconds("http_client_response_stall_timeout", 60),
		clientUploadTotal:     configSeconds("http_client_upload_total_timeout", 0),
		clientResponseTotal:   configSeconds("http_client_response_total_timeout", 0),
		backendConnect:        configSeconds("http_backend_connect_timeout", 10),
		backendTLSHandshake:   configSeconds("http_backend_tls_handshake_timeout", 10),
		backendResponseHeader: configSeconds("http_proxy_response_timeout", 100),
		backendRequestStart:   configSeconds("http_backend_request_start_timeout", 100),
		backendUploadStall:    configSeconds("http_backend_upload_stall_timeout", 60),
		backendReadStall:      configSeconds("http_backend_read_stall_timeout", 60),
		backendKeepAlive:      beego.AppConfig.DefaultBool("http_backend_keepalive", false),
		backendMaxIdle:        configPositiveInt("http_backend_max_idle_conns", 128),
		backendMaxIdlePerHost: configPositiveInt("http_backend_max_idle_conns_per_host", 16),
		backendIdleTimeout:    configSeconds("http_backend_idle_conn_timeout", 90),
		backendPoolEntries:    configPositiveInt("http_backend_max_pools", 256),
		backendExpectContinue: configSeconds("http_backend_expect_continue_timeout", 1),
		backendResponseBodyTotal: configSeconds(
			"http_backend_response_total_timeout",
			0,
		),
	}
}

func configSeconds(key string, fallback int) time.Duration {
	raw := strings.TrimSpace(beego.AppConfig.String(key))
	if raw == "" {
		raw = strconv.Itoa(fallback)
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		if fallback <= 0 {
			return 0
		}
		return time.Duration(fallback) * time.Second
	}
	if value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Second
}

func configPositiveInt(key string, fallback int) int {
	raw := strings.TrimSpace(beego.AppConfig.String(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func deadlineFor(stall, total time.Duration, started time.Time) time.Time {
	var deadline time.Time
	if stall > 0 {
		deadline = time.Now().Add(stall)
	}
	if total > 0 {
		totalDeadline := started.Add(total)
		if deadline.IsZero() || totalDeadline.Before(deadline) {
			deadline = totalDeadline
		}
	}
	return deadline
}

// timeoutConn applies per-read/write idle limits and optional whole-direction
// limits to the authenticated Bridge stream used as an HTTP backend socket.
type timeoutConn struct {
	net.Conn

	mu            sync.Mutex
	readStall     time.Duration
	writeStall    time.Duration
	readTotal     time.Duration
	writeTotal    time.Duration
	readStarted   time.Time
	writeStarted  time.Time
	readExempt    bool
	policyApplied bool
}

func newTimeoutConn(conn net.Conn, policy httpProxyPolicy) *timeoutConn {
	return &timeoutConn{
		Conn:       conn,
		writeStall: policy.clientResponseStall,
		writeTotal: policy.clientResponseTotal,
	}
}

func newBackendTimeoutConn(conn net.Conn, policy httpProxyPolicy) *timeoutConn {
	return &timeoutConn{
		Conn:       conn,
		writeStall: policy.backendUploadStall,
	}
}

func (c *timeoutConn) SetReadPolicy(stall, total time.Duration) {
	c.mu.Lock()
	c.readStall, c.readTotal, c.readStarted, c.readExempt = stall, total, time.Time{}, false
	c.policyApplied = true
	c.mu.Unlock()
	if stall <= 0 && total <= 0 {
		c.clearReadDeadline()
	}
}

func (c *timeoutConn) SetWritePolicy(stall, total time.Duration) {
	c.mu.Lock()
	c.writeStall, c.writeTotal, c.writeStarted = stall, total, time.Time{}
	c.policyApplied = true
	c.mu.Unlock()
	if stall <= 0 && total <= 0 {
		c.clearWriteDeadline()
	}
}

func (c *timeoutConn) ExemptReadTimeout() {
	c.mu.Lock()
	c.readStall, c.readTotal, c.readExempt = 0, 0, true
	c.mu.Unlock()
	c.clearReadDeadline()
}

func (c *timeoutConn) Read(p []byte) (int, error) {
	c.applyDeadline(false)
	n, err := c.Conn.Read(p)
	if err != nil && !isTimeoutError(err) {
		c.clearReadDeadline()
	}
	return n, err
}

func (c *timeoutConn) Write(p []byte) (int, error) {
	c.applyDeadline(true)
	n, err := c.Conn.Write(p)
	if err != nil && !isTimeoutError(err) {
		c.clearWriteDeadline()
	}
	return n, err
}

func (c *timeoutConn) applyDeadline(write bool) {
	if c == nil || c.Conn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if write {
		if c.writeStall <= 0 && c.writeTotal <= 0 {
			_ = c.Conn.SetWriteDeadline(time.Time{})
			return
		}
		if c.writeStarted.IsZero() {
			c.writeStarted = time.Now()
		}
		_ = c.Conn.SetWriteDeadline(deadlineFor(c.writeStall, c.writeTotal, c.writeStarted))
		return
	}
	if c.readExempt || (c.readStall <= 0 && c.readTotal <= 0) {
		_ = c.Conn.SetReadDeadline(time.Time{})
		return
	}
	if c.readStarted.IsZero() {
		c.readStarted = time.Now()
	}
	_ = c.Conn.SetReadDeadline(deadlineFor(c.readStall, c.readTotal, c.readStarted))
}

func (c *timeoutConn) clearReadDeadline() {
	if c == nil || c.Conn == nil {
		return
	}
	_ = c.Conn.SetReadDeadline(time.Time{})
}

func (c *timeoutConn) clearWriteDeadline() {
	if c == nil || c.Conn == nil {
		return
	}
	_ = c.Conn.SetWriteDeadline(time.Time{})
}

func (c *timeoutConn) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	return c.Conn.Close()
}

func isTimeoutError(err error) bool {
	if value, ok := err.(net.Error); ok {
		return value.Timeout()
	}
	return false
}

// phaseController is retained through a TLS wrapper so RoundTrip can switch
// response-body policy after the response headers are known.
type phaseController interface {
	SetReadPolicy(stall, total time.Duration)
	SetWritePolicy(stall, total time.Duration)
	ExemptReadTimeout()
}

type phaseControlledConn struct {
	net.Conn
	controller phaseController
}

func (c *phaseControlledConn) SetReadPolicy(stall, total time.Duration) {
	c.controller.SetReadPolicy(stall, total)
}

func (c *phaseControlledConn) SetWritePolicy(stall, total time.Duration) {
	c.controller.SetWritePolicy(stall, total)
}

func (c *phaseControlledConn) ExemptReadTimeout() {
	c.controller.ExemptReadTimeout()
}

func controllerFromConn(conn net.Conn) phaseController {
	if controller, ok := conn.(phaseController); ok {
		return controller
	}
	return nil
}

func isStreamingResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return true
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	return strings.HasPrefix(contentType, "text/event-stream") ||
		strings.HasPrefix(contentType, "application/grpc") ||
		strings.HasPrefix(contentType, "multipart/x-mixed-replace")
}

// deadlineBody applies an idle deadline while the HTTP stack reads a client
// request body. net/http exposes the underlying read deadline through
// ResponseController even after the request has entered a handler.
type deadlineBody struct {
	io.ReadCloser
	controller func(time.Time) error
	stall      time.Duration
	total      time.Duration
	started    time.Time
}

func wrapClientRequestBody(body io.ReadCloser, controller func(time.Time) error, policy httpProxyPolicy) io.ReadCloser {
	if body == nil || body == http.NoBody ||
		(policy.clientUploadStall <= 0 && policy.clientUploadTotal <= 0) {
		return body
	}
	return &deadlineBody{
		ReadCloser: body,
		controller: controller,
		stall:      policy.clientUploadStall,
		total:      policy.clientUploadTotal,
		started:    time.Now(),
	}
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	if b.controller != nil {
		_ = b.controller(deadlineFor(b.stall, b.total, b.started))
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil && b.controller != nil {
		_ = b.controller(time.Time{})
	}
	return n, err
}

func (b *deadlineBody) Close() error {
	if b.controller != nil {
		_ = b.controller(time.Time{})
	}
	return b.ReadCloser.Close()
}

// deadlineReader applies the same semantics to the raw HTTP/1 forwarding path,
// which reads request bodies directly from a bufio.Reader.
type deadlineReader struct {
	reader      io.Reader
	setDeadline func(time.Time) error
	stall       time.Duration
	total       time.Duration
	started     time.Time
	clearOnEOF  bool
}

func newDeadlineReader(reader io.Reader, setDeadline func(time.Time) error, stall, total time.Duration) io.Reader {
	if reader == nil || setDeadline == nil || (stall <= 0 && total <= 0) {
		return reader
	}
	return &deadlineReader{
		reader:      reader,
		setDeadline: setDeadline,
		stall:       stall,
		total:       total,
		started:     time.Now(),
		clearOnEOF:  true,
	}
}

func (r *deadlineReader) Read(p []byte) (int, error) {
	_ = r.setDeadline(deadlineFor(r.stall, r.total, r.started))
	n, err := r.reader.Read(p)
	if err != nil && r.clearOnEOF {
		_ = r.setDeadline(time.Time{})
	}
	return n, err
}

// timeoutResponseWriter limits how long a client may stop consuming the
// response. Streaming responses remain exempt from the whole-response limit.
type timeoutResponseWriter struct {
	http.ResponseWriter
	controller  *http.ResponseController
	stall       time.Duration
	total       time.Duration
	started     time.Time
	streaming   bool
	exemptTotal bool
	wroteHeader bool
}

func wrapClientResponseWriter(rw http.ResponseWriter, policy httpProxyPolicy) *timeoutResponseWriter {
	if rw == nil || (policy.clientResponseStall <= 0 && policy.clientResponseTotal <= 0) {
		return nil
	}
	return &timeoutResponseWriter{
		ResponseWriter: rw,
		controller:     http.NewResponseController(rw),
		stall:          policy.clientResponseStall,
		total:          policy.clientResponseTotal,
	}
}

func (w *timeoutResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *timeoutResponseWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	w.markStarted(status)
}

func (w *timeoutResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.markStarted(http.StatusOK)
	}
	if !w.exemptTotal {
		_ = w.controller.SetWriteDeadline(deadlineFor(w.stall, w.total, w.started))
	}
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		_ = w.controller.SetWriteDeadline(time.Time{})
	}
	return n, err
}

func (w *timeoutResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.markStarted(http.StatusOK)
	}
	if !w.exemptTotal {
		_ = w.controller.SetWriteDeadline(deadlineFor(w.stall, w.total, w.started))
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := readerFrom.ReadFrom(reader)
		if err != nil {
			_ = w.controller.SetWriteDeadline(time.Time{})
		}
		return n, err
	}
	return io.Copy(struct{ io.Writer }{w}, reader)
}

func (w *timeoutResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *timeoutResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.clearDeadline()
	return hijacker.Hijack()
}

func (w *timeoutResponseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (w *timeoutResponseWriter) markStarted(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.started = time.Now()
	contentType := strings.ToLower(strings.TrimSpace(w.Header().Get("Content-Type")))
	w.streaming = status == http.StatusSwitchingProtocols ||
		strings.HasPrefix(contentType, "text/event-stream") ||
		strings.HasPrefix(contentType, "application/grpc") ||
		strings.HasPrefix(contentType, "multipart/x-mixed-replace")
	w.exemptTotal = w.streaming && w.total > 0
	if !w.exemptTotal {
		_ = w.controller.SetWriteDeadline(deadlineFor(w.stall, w.total, w.started))
	}
}

func (w *timeoutResponseWriter) clearDeadline() {
	if w != nil && w.controller != nil {
		_ = w.controller.SetWriteDeadline(time.Time{})
	}
}

// httpTransportPool keeps backend pools isolated by client, visitor, Host,
// target and TLS policy. This makes optional keep-alive safe: a connection is
// never returned to a different authenticated forwarding context.
type httpTransportPool struct {
	policy httpProxyPolicy

	mu    sync.Mutex
	pools map[string]*http.Transport
}

func newHTTPTransportPool(policy httpProxyPolicy) *httpTransportPool {
	return &httpTransportPool{policy: policy, pools: make(map[string]*http.Transport)}
}

func (p *httpTransportPool) RoundTrip(req *http.Request) (*http.Response, error) {
	state, err := stateFromContext(req.Context())
	if err != nil {
		return nil, err
	}
	key := httpTransportPoolKey(state)
	transport := p.transportFor(key, state)
	var upstream phaseController
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			upstream = controllerFromConn(info.Conn)
			if upstream != nil {
				upstream.SetWritePolicy(p.policy.backendUploadStall, 0)
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if upstream != nil {
		upstream.SetWritePolicy(0, 0)
		if isStreamingResponse(resp) {
			upstream.ExemptReadTimeout()
		} else {
			upstream.SetReadPolicy(p.policy.backendReadStall, p.policy.backendResponseBodyTotal)
		}
	}
	return resp, nil
}

func (p *httpTransportPool) transportFor(key string, state *reverseProxyState) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if transport := p.pools[key]; transport != nil {
		return transport
	}
	if len(p.pools) >= p.policy.backendPoolEntries {
		for poolKey, transport := range p.pools {
			transport.CloseIdleConnections()
			delete(p.pools, poolKey)
		}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: p.policy.backendResponseHeader,
		DisableKeepAlives:     !p.policy.backendKeepAlive,
		MaxIdleConns:          p.policy.backendMaxIdle,
		MaxIdleConnsPerHost:   p.policy.backendMaxIdlePerHost,
		IdleConnTimeout:       p.policy.backendIdleTimeout,
		TLSHandshakeTimeout:   p.policy.backendTLSHandshake,
		ExpectContinueTimeout: p.policy.backendExpectContinue,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return state.dialHTTPBackend(ctx, network, addr, false)
		},
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return state.dialHTTPBackend(ctx, network, addr, true)
		},
	}
	p.pools[key] = transport
	return transport
}

func (p *httpTransportPool) CloseIdleConnections() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, transport := range p.pools {
		transport.CloseIdleConnections()
	}
}

func httpTransportPoolKey(state *reverseProxyState) string {
	if state == nil || state.host == nil || state.client == nil {
		return ""
	}
	state.host.RLock()
	hostID, targetHTTPS, hostChange, pathRewrite, autoCORS := state.host.Id, state.host.TargetIsHTTPS, state.host.HostChange, state.host.PathRewrite, state.host.AutoCORS
	registeredOnly := state.host.RegisteredOnly
	state.host.RUnlock()
	sni := state.host.Host
	if hostChange != "" {
		sni = hostChange
	}
	return fmt.Sprintf(
		"%d|%d|%s|%s|%t|%t|%t|%t|%s|%s|%s|%d|%s|%s|%s|%s",
		hostID,
		state.clientID,
		state.targetAddr,
		state.req.RemoteAddr,
		state.config.Crypt,
		state.config.Compress,
		state.localProxy,
		targetHTTPS,
		sni,
		hostProxyProtocolVersion(state.host),
		pathRewrite,
		boolInt(registeredOnly),
		state.req.Proto,
		state.req.Method,
		state.req.Header.Get("Upgrade"),
		strconv.FormatBool(autoCORS),
	)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (state *reverseProxyState) dialHTTPBackend(ctx context.Context, _, _ string, tlsBackend bool) (net.Conn, error) {
	if state == nil || state.server == nil || state.server.bridge == nil {
		return nil, NewHTTPError(http.StatusBadGateway, "proxy request state is unavailable")
	}
	policy := state.server.httpPolicy
	var cancel context.CancelFunc
	startLimit := policy.backendConnect
	if policy.backendRequestStart > 0 && (startLimit <= 0 || policy.backendRequestStart < startLimit) {
		startLimit = policy.backendRequestStart
	}
	if startLimit > 0 {
		ctx, cancel = context.WithTimeout(ctx, startLimit)
		defer cancel()
	}
	lk := conn.NewLink(
		"http",
		state.targetAddr,
		state.config.Crypt,
		state.config.Compress,
		state.req.RemoteAddr,
		state.localProxy,
		hostProxyProtocolVersion(state.host),
	)
	target, err := state.server.bridge.SendLinkInfo(state.clientID, lk, &file.Tunnel{RegisteredOnly: state.registeredOnly})
	if err != nil {
		logs.Notice("connect to target %s error %s", lk.Host, err)
		return nil, NewHTTPError(http.StatusBadGateway, "Cannot connect to the server")
	}
	if target == nil {
		return nil, NewHTTPError(http.StatusBadGateway, "server returned an empty connection")
	}
	connClient := conn.GetConnWithUserLimits(
		target,
		lk.Crypt,
		lk.Compress,
		state.clientRate,
		true,
		nil,
		state.host,
		state.userRate,
	)
	baseConn := readWriteCloserAsNetConn(connClient)
	if baseConn == nil {
		_ = connClient.Close()
		return nil, NewHTTPError(http.StatusBadGateway, "backend connection is unavailable")
	}
	phaseConn := newBackendTimeoutConn(&backendTrackedConn{
		Conn: baseConn,
		host: state.host,
		flow: state.clientFlow,
	}, policy)
	phaseConn.SetWritePolicy(policy.backendUploadStall, 0)
	if !tlsBackend {
		return phaseConn, nil
	}
	state.host.RLock()
	sni := state.host.Host
	if state.host.HostChange != "" {
		sni = state.host.HostChange
	}
	state.host.RUnlock()
	tlsConn := tls.Client(phaseConn, &tls.Config{
		ServerName:         normalizeBackendSNI(sni),
		InsecureSkipVerify: true, // The authenticated Bridge chooses the backend.
		MinVersion:         tls.VersionTLS12,
	})
	handshakeCtx := ctx
	var handshakeCancel context.CancelFunc
	if policy.backendTLSHandshake > 0 {
		handshakeCtx, handshakeCancel = context.WithTimeout(ctx, policy.backendTLSHandshake)
		defer handshakeCancel()
	}
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		_ = phaseConn.Close()
		return nil, NewHTTPError(http.StatusBadGateway, "HTTPS backend handshake failed")
	}
	_ = phaseConn.SetDeadline(time.Time{})
	return &phaseControlledConn{Conn: tlsConn, controller: phaseConn}, nil
}

type backendTrackedConn struct {
	net.Conn
	host *file.Host
	flow *file.Flow
}

func (c *backendTrackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		if c.flow != nil {
			c.flow.Add(0, int64(n))
		}
		if c.host != nil {
			c.host.RLock()
			hostFlow := c.host.Flow
			c.host.RUnlock()
			if hostFlow != nil && hostFlow != c.flow {
				hostFlow.Add(0, int64(n))
			}
		}
	}
	return n, err
}

func (c *backendTrackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		if c.flow != nil {
			c.flow.Add(int64(n), 0)
		}
		if c.host != nil {
			c.host.RLock()
			hostFlow := c.host.Flow
			c.host.RUnlock()
			if hostFlow != nil && hostFlow != c.flow {
				hostFlow.Add(int64(n), 0)
			}
		}
	}
	return n, err
}

func normalizeBackendSNI(raw string) string {
	raw = strings.TrimSpace(raw)
	if host, _, err := net.SplitHostPort(raw); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(raw, "[]")
}
