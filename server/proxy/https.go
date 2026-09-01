package proxy

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/cache"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
	"github.com/pkg/errors"
)

type HttpsServer struct {
	httpServer
	listener         net.Listener
	httpsListenerMap sync.Map
	hostIdCertMap    sync.Map
	listenerMu       sync.Mutex
	closeOnce        sync.Once
	closed           atomic.Bool
	closeErr         error
}

var httpsHandshakeTimeout = 10 * time.Second

const (
	tlsRecordHeaderSize = 5
	maxTLSRecordSize    = 16384
	maxClientHelloSize  = 1 << 20
)

func NewHttpsServer(l net.Listener, bridge NetBridge, useCache bool, cacheLen int) *HttpsServer {
	https := &HttpsServer{listener: l}
	https.bridge = bridge
	https.useCache = useCache
	if useCache {
		https.cache = cache.New(cacheLen)
	}
	return https
}

// start https server
func (https *HttpsServer) Start() error {
	if https.listener == nil {
		return errors.New("https listener is nil")
	}
	if https.closed.Load() {
		return errors.New("https server has closed")
	}

	conn.Accept(https.listener, func(c net.Conn) {
		if c == nil {
			return
		}
		if https.closed.Load() {
			_ = c.Close()
			return
		}
		// A client can otherwise hold an accepted socket indefinitely by
		// sending only a partial TLS ClientHello. The deadline is cleared only
		// after a complete, valid record has been parsed below.
		_ = c.SetReadDeadline(time.Now().Add(httpsHandshakeTimeout))
		serverName, rb, parseErr := getServerNameFromClientHello(c)
		if parseErr != nil {
			_ = c.Close()
			logs.Debug("invalid or incomplete TLS ClientHello from %s: %v", c.RemoteAddr(), parseErr)
			return
		}
		_ = c.SetReadDeadline(time.Time{})
		if https.closed.Load() {
			_ = c.Close()
			return
		}
		if serverName == "" {
			serverName = getFallbackServerName()
			logs.Debug("https fallback server name result, remote addr %s, server name %q", c.RemoteAddr().String(), serverName)
		}
		r := buildHttpsRequest(serverName)
		if host, err := file.GetDb().GetInfoByHost(serverName, r); err != nil {
			c.Close()
			logs.Debug("https host lookup failed, server name %q, remote addr %s, error %v", serverName, c.RemoteAddr().String(), err)
			return
		} else {
			hostClient, _, _, ok := snapshotHostProxyParts(host)
			if !ok {
				logs.Warn("reject malformed HTTPS host %q", serverName)
				_ = c.Close()
				return
			}
			host.RLock()
			certFilePath, keyFilePath := host.CertFilePath, host.KeyFilePath
			host.RUnlock()
			if IsGlobalBlackIp(c.RemoteAddr().String()) || isClientBlackBlocked(hostClient, c.RemoteAddr().String()) || isIPWhiteBlocked(hostClient, c.RemoteAddr().String()) {
				logs.Warn("https connection rejected by IP allowlist, host %s, remote addr %s", serverName, c.RemoteAddr().String())
				_ = c.Close()
				return
			}
			if certFilePath == "" || keyFilePath == "" {
				logs.Debug("加载客户端本地证书")
				https.handleHttps2(c, serverName, rb, r)
			} else {
				logs.Debug("使用上传证书")

				// 判断是路径还是证书，-----BEGIN 开头的为证书
				if strings.Contains(certFilePath, "-----BEGIN") || strings.Contains(keyFilePath, "-----BEGIN") {
					logs.Debug("通过上传文件加载证书")
					https.cert(host, c, rb, certFilePath, keyFilePath)
				} else {
					logs.Debug("通过路径加载证书")
					if !common.FileExists(certFilePath) || !common.FileExists(keyFilePath) {
						logs.Error("证书或秘钥文件不存在", keyFilePath, certFilePath)
						https.enqueueCachedCertificate(host.Id, c, rb)
						return
					}

					cert, err := common.ReadAllFromFile(certFilePath)
					if err != nil {
						logs.Error("加载证书失败", err)
						https.enqueueCachedCertificate(host.Id, c, rb)
						return
					}
					key, err := common.ReadAllFromFile(keyFilePath)
					if err != nil {
						logs.Error("加载证书秘钥失败", err)
						https.enqueueCachedCertificate(host.Id, c, rb)
						return
					}

					https.cert(host, c, rb, string(cert), string(key))

				}

			}
		}
	})

	//var err error
	//if https.errorContent, err = common.ReadAllFromFile(filepath.Join(common.GetRunPath(), "web", "static", "page", "error.html")); err != nil {
	//	https.errorContent = []byte("nps 404")
	//}
	//if b, err := beego.AppConfig.Bool("https_just_proxy"); err == nil && b {
	//	conn.Accept(https.listener, func(c net.Conn) {
	//		https.handleHttps(c)
	//	})
	//} else {
	//	//start the default listener
	//	certFile := beego.AppConfig.String("https_default_cert_file")
	//	keyFile := beego.AppConfig.String("https_default_key_file")
	//	if common.FileExists(certFile) && common.FileExists(keyFile) {
	//		l := NewHttpsListener(https.listener)
	//		https.NewHttps(l, certFile, keyFile)
	//		https.httpsListenerMap.Store("default", l)
	//	}
	//	conn.Accept(https.listener, func(c net.Conn) {
	//		serverName, rb := GetServerNameFromClientHello(c)
	//		//if the clientHello does not contains sni ,use the default ssl certificate
	//		if serverName == "" {
	//			serverName = "default"
	//		}
	//		var l *HttpsListener
	//		if v, ok := https.httpsListenerMap.Load(serverName); ok {
	//			l = v.(*HttpsListener)
	//		} else {
	//			r := buildHttpsRequest(serverName)
	//			if host, err := file.GetDb().GetInfoByHost(serverName, r); err != nil {
	//				c.Close()
	//				logs.Notice("the url %s can't be parsed!,remote addr %s", serverName, c.RemoteAddr().String())
	//				return
	//			} else {
	//				if !common.FileExists(host.CertFilePath) || !common.FileExists(host.KeyFilePath) {
	//					//if the host cert file or key file is not set ,use the default file
	//					if v, ok := https.httpsListenerMap.Load("default"); ok {
	//						l = v.(*HttpsListener)
	//					} else {
	//						c.Close()
	//						logs.Error("the key %s cert %s file is not exist", host.KeyFilePath, host.CertFilePath)
	//						return
	//					}
	//				} else {
	//					l = NewHttpsListener(https.listener)
	//					https.NewHttps(l, host.CertFilePath, host.KeyFilePath)
	//					https.httpsListenerMap.Store(serverName, l)
	//				}
	//			}
	//		}
	//		acceptConn := conn.NewConn(c)
	//		acceptConn.Rb = rb
	//		l.acceptConn <- acceptConn
	//	})
	//}
	return nil
}

func getFallbackServerName() string {
	global := file.GetDb().GetGlobal()
	if global == nil {
		return ""
	}
	serverName := strings.TrimSpace(global.ServerUrl)
	if serverName == "" {
		return ""
	}
	if strings.Contains(serverName, "://") {
		if u, err := url.Parse(serverName); err == nil && u.Host != "" {
			serverName = u.Host
		}
	}
	if i := strings.Index(serverName, "/"); i >= 0 {
		serverName = serverName[:i]
	}
	serverName = common.GetIpByAddr(serverName)
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		return ""
	}
	ip := net.ParseIP(serverName)
	if ip != nil && ip.IsUnspecified() {
		return ""
	}
	return serverName
}

func (https *HttpsServer) cert(host *file.Host, c net.Conn, rb []byte, certFileUrl string, keyFileUrl string) {
	if host == nil || c == nil {
		if c != nil {
			_ = c.Close()
		}
		return
	}
	certKey, err := certificateMaterialKey(certFileUrl, keyFileUrl)
	if err != nil {
		logs.Warn("failed to load updated TLS certificate for host id %d, retaining the last valid certificate: %v", host.Id, err)
		https.enqueueCachedCertificate(host.Id, c, rb)
		return
	}

	// Certificate listeners are created lazily from accept goroutines. Serialize
	// creation/replacement with Close so a connection cannot be queued onto a
	// listener after the HTTPS server has shut down.
	https.listenerMu.Lock()
	defer https.listenerMu.Unlock()
	if https.closed.Load() {
		_ = c.Close()
		return
	}
	var l *HttpsListener
	i := 0
	https.hostIdCertMap.Range(func(key, value interface{}) bool {
		i++
		// 如果host Id 不存在，则删除map
		if id, ok := key.(int); ok {
			var err error
			_, err = file.GetDb().GetHostById(id)
			if err != nil {
				// 说明这个host已经不存了，需要释放Listener
				logs.Error(err)
				https.hostIdCertMap.Delete(key)
				https.releaseCertListener(value)
				logs.Debug("Listener 已释放")
			}
		}
		return true
	})

	logs.Debug("当前 Listener 连接数量", i)

	previousCertKey, hasPreviousCert := https.hostIdCertMap.Load(host.Id)
	l = https.listenerForCertificateKey(certKey)
	if l == nil {
		l = NewHttpsListener(https.listener)
		https.NewHttps(l, certFileUrl, keyFileUrl)
		https.httpsListenerMap.Store(certKey, l)
	}

	// Store the new key only after its certificate/key pair has been validated
	// and its listener has been created. This keeps the previous listener live
	// when a renewal temporarily writes an incomplete or invalid file.
	https.hostIdCertMap.Store(host.Id, certKey)
	if hasPreviousCert && previousCertKey != certKey {
		https.releaseCertListener(previousCertKey)
	}

	https.enqueueCertificateListener(l, c, rb)
}

// certificateMaterialKey validates a complete certificate/private-key pair
// before it can replace a listener. The key includes both PEM values so a
// private-key-only update cannot keep serving a listener created with an old
// key.
func certificateMaterialKey(certPEM string, keyPEM string) (string, error) {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return "", err
	}
	if len(pair.Certificate) == 0 {
		return "", errors.New("TLS certificate has no leaf")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return "", err
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return "", errors.New("TLS certificate is not currently valid")
	}
	return certificateFingerprint(certPEM, keyPEM), nil
}

func certificateFingerprint(certPEM string, keyPEM string) string {
	digest := sha256.New()
	for _, material := range []string{certPEM, keyPEM} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(material)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(material))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// enqueueCachedCertificate keeps serving the last known-good certificate when
// its source files cannot be read or no longer form a valid TLS pair.
func (https *HttpsServer) enqueueCachedCertificate(hostID int, c net.Conn, rb []byte) bool {
	if c == nil {
		return false
	}
	https.listenerMu.Lock()
	defer https.listenerMu.Unlock()
	if https.closed.Load() {
		_ = c.Close()
		return false
	}
	certKey, ok := https.hostIdCertMap.Load(hostID)
	if !ok {
		_ = c.Close()
		return false
	}
	l := https.listenerForCertificateKey(certKey)
	if l == nil {
		_ = c.Close()
		return false
	}
	https.enqueueCertificateListener(l, c, rb)
	return true
}

func (https *HttpsServer) listenerForCertificateKey(certKey interface{}) *HttpsListener {
	v, ok := https.httpsListenerMap.Load(certKey)
	if !ok {
		return nil
	}
	l, ok := v.(*HttpsListener)
	if !ok || l == nil || atomic.LoadInt32(&l.closed) != 0 {
		return nil
	}
	return l
}

func (https *HttpsServer) enqueueCertificateListener(l *HttpsListener, c net.Conn, rb []byte) {
	if l == nil || c == nil {
		if c != nil {
			_ = c.Close()
		}
		return
	}
	acceptConn := conn.NewConn(c)
	acceptConn.Rb = rb
	l.enqueue(acceptConn, c)
}

// releaseCertListener closes a certificate listener only when no remaining
// host entry references its certificate key. Multiple virtual hosts may share
// one certificate and therefore one HttpsListener.
func (https *HttpsServer) releaseCertListener(certKey interface{}) {
	if certKey == nil {
		return
	}
	referenced := false
	https.hostIdCertMap.Range(func(_, value interface{}) bool {
		if value == certKey {
			referenced = true
			return false
		}
		return true
	})
	if referenced {
		return
	}
	if oldL, ok := https.httpsListenerMap.Load(certKey); ok {
		if listener, ok := oldL.(*HttpsListener); ok && listener != nil {
			if err := listener.Close(); err != nil {
				logs.Error(err)
			}
		}
		https.httpsListenerMap.Delete(certKey)
	}
}

// handle the https which is just proxy to other client
func (https *HttpsServer) handleHttps2(c net.Conn, hostName string, rb []byte, r *http.Request) {
	if c == nil || r == nil {
		if c != nil {
			_ = c.Close()
		}
		return
	}
	var targetAddr string
	var host *file.Host
	var err error
	if host, err = file.GetDb().GetInfoByHost(hostName, r); err != nil {
		c.Close()
		logs.Debug("the url %s can't be parsed!", hostName)
		return
	}
	hostClient, hostTarget, _, validHost := snapshotHostProxyParts(host)
	if !validHost {
		logs.Warn("reject malformed HTTPS host %q", hostName)
		_ = c.Close()
		return
	}
	clientConfig, configOK := snapshotClientConfig(hostClient)
	if !configOK {
		_ = c.Close()
		return
	}
	if IsGlobalBlackIp(c.RemoteAddr().String()) || isClientBlackBlocked(hostClient, c.RemoteAddr().String()) || isIPWhiteBlocked(hostClient, c.RemoteAddr().String()) {
		logs.Warn("https connection rejected by IP allowlist, host %s, remote addr %s", hostName, c.RemoteAddr().String())
		_ = c.Close()
		return
	}
	if err := https.CheckFlowAndConnNum(hostClient); err != nil {
		hostClient.RLock()
		clientID := hostClient.Id
		hostClient.RUnlock()
		logs.Debug("client id %d, host id %d, error %s, when https connection", clientID, host.Id, err.Error())
		c.Close()
		return
	}
	defer hostClient.AddConn()
	if err = https.auth(r, conn.NewConn(c), clientConfig.U, clientConfig.P); err != nil {
		logs.Warn("auth error", err, r.RemoteAddr)
		return
	}
	if targetAddr, err = hostTarget.GetRandomTarget(); err != nil {
		logs.Warn(err.Error())
		c.Close()
		return
	}
	hostClient.RLock()
	clientID := hostClient.Id
	hostClient.RUnlock()
	hostTarget.RLock()
	localProxy := hostTarget.LocalProxy
	hostTarget.RUnlock()
	logs.Info("new https connection,clientId %d,host %s,remote address %s", clientID, r.Host, c.RemoteAddr().String())
	https.DealClient(conn.NewConn(c), hostClient, targetAddr, rb, common.CONN_TCP, nil, nil, localProxy, nil, host)
}

// close
func (https *HttpsServer) Close() error {
	https.closeOnce.Do(func() {
		https.listenerMu.Lock()
		https.closed.Store(true)
		listeners := make([]*HttpsListener, 0)
		https.httpsListenerMap.Range(func(_, value interface{}) bool {
			if listener, ok := value.(*HttpsListener); ok && listener != nil {
				listeners = append(listeners, listener)
			}
			return true
		})
		parent := https.listener
		https.listenerMu.Unlock()

		for _, listener := range listeners {
			if err := listener.Close(); err != nil && https.closeErr == nil {
				https.closeErr = err
			}
		}
		if parent != nil {
			if err := parent.Close(); err != nil && https.closeErr == nil {
				https.closeErr = err
			}
		}
	})
	return https.closeErr
}

// new https server by cert and key file
func (https *HttpsServer) NewHttps(l net.Listener, certFile string, keyFile string) {
	go func() {
		//logs.Error(https.NewServer(0, "https").ServeTLS(l, certFile, keyFile))
		logs.Error(https.NewServerWithTls(0, "https", l, certFile, keyFile))

	}()
}

// handle the https which is just proxy to other client
func (https *HttpsServer) handleHttps(c net.Conn) {
	if c == nil {
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(httpsHandshakeTimeout))
	hostName, rb, parseErr := getServerNameFromClientHello(c)
	if parseErr != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	var targetAddr string
	r := buildHttpsRequest(hostName)
	var host *file.Host
	var err error
	if host, err = file.GetDb().GetInfoByHost(hostName, r); err != nil {
		c.Close()
		logs.Notice("the url %s can't be parsed!", hostName)
		return
	}
	hostClient, hostTarget, _, validHost := snapshotHostProxyParts(host)
	if !validHost {
		logs.Warn("reject malformed HTTPS host %q", hostName)
		_ = c.Close()
		return
	}
	clientConfig, configOK := snapshotClientConfig(hostClient)
	if !configOK {
		_ = c.Close()
		return
	}
	if IsGlobalBlackIp(c.RemoteAddr().String()) || isClientBlackBlocked(hostClient, c.RemoteAddr().String()) || isIPWhiteBlocked(hostClient, c.RemoteAddr().String()) {
		logs.Warn("https connection rejected by IP allowlist, host %s, remote addr %s", hostName, c.RemoteAddr().String())
		_ = c.Close()
		return
	}
	if err := https.CheckFlowAndConnNum(hostClient); err != nil {
		hostClient.RLock()
		clientID := hostClient.Id
		hostClient.RUnlock()
		logs.Warn("client id %d, host id %d, error %s, when https connection", clientID, host.Id, err.Error())
		c.Close()
		return
	}
	defer hostClient.AddConn()
	if err = https.auth(r, conn.NewConn(c), clientConfig.U, clientConfig.P); err != nil {
		logs.Warn("auth error", err, r.RemoteAddr)
		return
	}
	if targetAddr, err = hostTarget.GetRandomTarget(); err != nil {
		logs.Warn(err.Error())
		c.Close()
		return
	}
	hostClient.RLock()
	clientID := hostClient.Id
	hostClient.RUnlock()
	hostTarget.RLock()
	localProxy := hostTarget.LocalProxy
	hostTarget.RUnlock()
	logs.Trace("new https connection,clientId %d,host %s,remote address %s", clientID, r.Host, c.RemoteAddr().String())
	https.DealClient(conn.NewConn(c), hostClient, targetAddr, rb, common.CONN_TCP, nil, nil, localProxy, nil, host)
}

type HttpsListener struct {
	acceptConn     chan *conn.Conn
	parentListener net.Listener
	closed         int32
	closeCh        chan struct{}
	closeOnce      sync.Once
	mu             sync.RWMutex
}

// https listener
func NewHttpsListener(l net.Listener) *HttpsListener {
	return &HttpsListener{parentListener: l, acceptConn: make(chan *conn.Conn, 1024), closeCh: make(chan struct{})}
}

func (httpsListener *HttpsListener) enqueue(httpsConn *conn.Conn, raw net.Conn) {
	httpsListener.mu.RLock()
	defer httpsListener.mu.RUnlock()
	if atomic.LoadInt32(&httpsListener.closed) == 1 {
		_ = raw.Close()
		return
	}
	select {
	case httpsListener.acceptConn <- httpsConn:
	case <-httpsListener.closeCh:
		_ = raw.Close()
	default:
		logs.Warn("https acceptConn channel full, dropping connection")
		_ = raw.Close()
	}
}

// accept
func (httpsListener *HttpsListener) Accept() (net.Conn, error) {
	if atomic.LoadInt32(&httpsListener.closed) == 1 {
		return nil, errors.New("listener closed")
	}
	select {
	case <-httpsListener.closeCh:
		return nil, net.ErrClosed
	case httpsConn := <-httpsListener.acceptConn:
		if httpsConn == nil {
			return nil, net.ErrClosed
		}
		// A close and a queued connection can become ready at the same time;
		// prefer the close semantics and do not hand a stale socket to Serve.
		select {
		case <-httpsListener.closeCh:
			_ = httpsConn.Close()
			return nil, net.ErrClosed
		default:
		}
		return httpsConn, nil
	}
}

// close
func (httpsListener *HttpsListener) Close() error {
	httpsListener.closeOnce.Do(func() {
		httpsListener.mu.Lock()
		defer httpsListener.mu.Unlock()
		atomic.StoreInt32(&httpsListener.closed, 1)
		close(httpsListener.closeCh)
		for {
			select {
			case pending := <-httpsListener.acceptConn:
				if pending != nil {
					_ = pending.Close()
				}
			default:
				return
			}
		}
	})
	return nil
}

// addr
func (httpsListener *HttpsListener) Addr() net.Addr {
	if httpsListener.parentListener == nil {
		return nil
	}
	return httpsListener.parentListener.Addr()
}

// GetServerNameFromClientHello keeps the historical two-value API for callers
// that only need best-effort inspection. Active listeners use the internal
// helper below so incomplete or malformed records cannot be treated as a
// ClientHello without SNI.
func GetServerNameFromClientHello(c net.Conn) (string, []byte) {
	serverName, rb, _ := getServerNameFromClientHello(c)
	return serverName, rb
}

// getServerNameFromClientHello reads all records needed for one complete
// ClientHello. TLS permits a handshake message to span multiple records, so
// parsing only the first record would reject otherwise valid clients.
func getServerNameFromClientHello(c net.Conn) (string, []byte, error) {
	if c == nil {
		return "", nil, errors.New("TLS ClientHello connection is nil")
	}
	rawBytes := make([]byte, 0, tlsRecordHeaderSize+1024)
	handshake := make([]byte, 0, 1024)
	expectedLen := 0
	for expectedLen == 0 || len(handshake) < expectedLen {
		header := make([]byte, tlsRecordHeaderSize)
		headerN, err := io.ReadFull(c, header)
		rawBytes = append(rawBytes, header[:headerN]...)
		if err != nil {
			return "", rawBytes, err
		}
		if header[0] != 0x16 {
			return "", rawBytes, errors.New("invalid TLS record type")
		}
		recordLen := int(header[3])<<8 | int(header[4])
		if recordLen <= 0 || recordLen > maxTLSRecordSize {
			return "", rawBytes, errors.New("invalid TLS record length")
		}
		body := make([]byte, recordLen)
		bodyN, err := io.ReadFull(c, body)
		rawBytes = append(rawBytes, body[:bodyN]...)
		if err != nil {
			return "", rawBytes, err
		}
		if len(handshake)+len(body) > maxClientHelloSize {
			return "", rawBytes, errors.New("TLS ClientHello is too large")
		}
		handshake = append(handshake, body...)
		if expectedLen == 0 && len(handshake) >= 4 {
			if handshake[0] != 1 {
				return "", rawBytes, errors.New("first TLS handshake is not ClientHello")
			}
			helloLen := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
			expectedLen = 4 + helloLen
			if helloLen == 0 || expectedLen > maxClientHelloSize {
				return "", rawBytes, errors.New("invalid TLS ClientHello length")
			}
		}
	}
	clientHello := new(crypt.ClientHelloMsg)
	if !clientHello.Unmarshal(handshake[:expectedLen]) {
		return "", rawBytes, errors.New("invalid TLS ClientHello")
	}
	return clientHello.GetServerName(), rawBytes, nil
}

// build https request for SNI-based host lookup.
// RequestURI is set to "*" to skip Location filtering in GetInfoByHost,
// because at TLS handshake stage the actual HTTP request path is unknown.
func buildHttpsRequest(hostName string) *http.Request {
	r := new(http.Request)
	r.RequestURI = "*"
	r.URL = new(url.URL)
	r.URL.Scheme = "https"
	r.Host = hostName
	return r
}
