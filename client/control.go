package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/config"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/version"
	"github.com/astaxie/beego/logs"
	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go"
	"github.com/xtaci/kcp-go"
)

var tlsEnable1 atomic.Bool

const clientDialTimeout = 10 * time.Second

func SetTlsEnable(tlsEnable11 bool) {
	tlsEnable1.Store(tlsEnable11)
}

func GetTlsEnable() bool {
	return tlsEnable1.Load()
}

func GetTaskStatus(path string) {
	cnf, err := config.NewConfig(path)
	if err != nil || cnf == nil || cnf.CommonConfig == nil {
		if err == nil {
			err = errors.New("missing common configuration")
		}
		log.Fatalln(err)
	}
	if err := common.SetCustomDNS(cnf.CommonConfig.DnsServer); err != nil {
		logs.Warn("[配置] DNS 服务器配置无效，继续使用当前系统解析器：%v", err)
	}
	c, err := NewConnWithTLS(cnf.CommonConfig.Tp, cnf.CommonConfig.VKey, cnf.CommonConfig.Server, common.WORK_CONFIG, cnf.CommonConfig.ProxyUrl, cnf.CommonConfig.TlsEnable, TLSOptionsFromConfig(cnf.CommonConfig))
	if err != nil {
		log.Fatalln(err)
	}
	if _, err := c.Write([]byte(common.WORK_STATUS)); err != nil {
		log.Fatalln(err)
	}
	//read now vKey and write to server
	if f, err := common.ReadAllFromFile(filepath.Join(common.GetTmpPath(), "npc_vkey.txt")); err != nil {
		log.Fatalln(err)
	} else if _, err := c.Write([]byte(crypt.Md5(string(f)))); err != nil {
		log.Fatalln(err)
	}
	var isPub bool
	binary.Read(c, binary.LittleEndian, &isPub)
	if l, err := c.GetLen(); err != nil {
		log.Fatalln(err)
	} else if b, err := c.GetShortContent(l); err != nil {
		log.Fatalln(err)
	} else {
		arr := strings.Split(string(b), common.CONN_DATA_SEQ)
		for _, v := range cnf.Hosts {
			if common.InStrArr(arr, v.Remark) {
				log.Println(v.Remark, "ok")
			} else {
				log.Println(v.Remark, "not running")
			}
		}
		for _, v := range cnf.Tasks {
			ports := common.GetPorts(v.Ports)
			if v.Mode == "secret" {
				ports = append(ports, 0)
			}
			for _, vv := range ports {
				var remark string
				if len(ports) > 1 {
					remark = v.Remark + "_" + strconv.Itoa(vv)
				} else {
					remark = v.Remark
				}
				if common.InStrArr(arr, remark) {
					log.Println(remark, "ok")
				} else {
					log.Println(remark, "not running")
				}
			}
		}
	}
	os.Exit(0)
}

var errAdd = errors.New("The server returned an error, which port or host may have been occupied or not allowed to open.")

// StartFromFile runs one NPC configuration file. When the server deletes the
// client registration, each supplied onKick callback is invoked once with the
// affected vkey before this function returns; callers use it to coordinate
// service-level cleanup without tearing down other live connections.
func StartFromFile(path string, onKick ...func(vKey string)) {
	first := true
	cnf, err := config.NewConfig(path)
	if err != nil || cnf.CommonConfig == nil {
		if err != nil {
			logs.Error("Config file %s loading error %s", path, err)
		} else {
			logs.Error("Config file %s loading error: missing common configuration", path)
		}
		return
	}
	logs.Info("[配置] 配置文件加载成功：%s", path)
	if err := common.SetCustomDNS(cnf.CommonConfig.DnsServer); err != nil {
		logs.Warn("[配置] DNS 服务器配置无效，继续使用当前系统解析器：%v", err)
	}

	logs.Info("the version of client is %s, the core version of client is %s,tls enable is %t", version.VERSION, version.GetVersion(), cnf.CommonConfig.TlsEnable)
	if cnf.CommonConfig.NtpServer != "" {
		interval := time.Duration(cnf.CommonConfig.NtpInterval) * time.Minute
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		common.ConfigureTimeSync(cnf.CommonConfig.NtpServer, interval)
	}
re:
	if first || cnf.CommonConfig.AutoReconnection {
		if !first {
			logs.Info("[重连] 配置连接断开，5 秒后重连")
			time.Sleep(time.Second * 5)
		}
	} else {
		return
	}
	first = false
	c, err := NewConnWithTLS(cnf.CommonConfig.Tp, cnf.CommonConfig.VKey, cnf.CommonConfig.Server, common.WORK_CONFIG, cnf.CommonConfig.ProxyUrl, cnf.CommonConfig.TlsEnable, TLSOptionsFromConfig(cnf.CommonConfig))
	if err != nil {
		logs.Error("[连接异常] 配置通道连接失败：%v", err)
		goto re
	}
	var isPub bool
	binary.Read(c, binary.LittleEndian, &isPub)

	// get tmp password
	var b []byte
	vkey := cnf.CommonConfig.VKey
	if isPub {
		// send global configuration to server and get status of config setting
		if _, err := c.SendInfo(cnf.CommonConfig.Client, common.NEW_CONF); err != nil {
			logs.Error(err)
			goto re
		}
		if !c.GetAddStatus() {
			logs.Error("the web_user may have been occupied!")
			goto re
		}

		if b, err = c.GetShortContent(16); err != nil {
			logs.Error(err)
			goto re
		}
		vkey = string(b)
	}
	os.WriteFile(filepath.Join(common.GetTmpPath(), "npc_vkey.txt"), []byte(vkey), 0600)

	//send hosts to server
	for _, v := range cnf.Hosts {
		if _, err := c.SendInfo(v, common.NEW_HOST); err != nil {
			logs.Error(err)
			goto re
		}
		if !c.GetAddStatus() {
			logs.Error(errAdd, v.Host)
			goto re
		}
	}

	//send  task to server
	localGeneration := ensureLocalGeneration()
	for _, v := range cnf.Tasks {
		if _, err := c.SendInfo(v, common.NEW_TASK); err != nil {
			logs.Error(err)
			goto re
		}
		if !c.GetAddStatus() {
			logs.Error(errAdd, v.Ports, v.Remark)
			goto re
		}
		if v.Mode == "file" {
			//start local file server
			go startLocalFileServer(cnf.CommonConfig, v, vkey, localGeneration)
		}
	}

	// Create local P2P/Secret ingress listeners with the modern manager for
	// current NPCs. Older builds retain the historical UDP/KCP listener path.
	var modernLocalManager *P2PManager
	if version.SupportsModernP2P(version.VERSION) {
		crypt.EnsureTls()
		modernLocalManager = NewP2PManager(context.Background(), cnf.CommonConfig)
	}
	//create local server secret or p2p
	for _, v := range cnf.LocalServer {
		if modernLocalManager != nil && (v.Type == "p2p" || v.Type == "secret") {
			if err := modernLocalManager.StartLocalServer(v); err != nil {
				logs.Error("启动 P2P/Secret 本地监听失败（端口 %d）：%v", v.Port, err)
			}
			continue
		}
		go startLocalServer(v, cnf.CommonConfig, localGeneration)
	}

	c.Close()
	if cnf.CommonConfig.Client.WebUserName == "" || cnf.CommonConfig.Client.WebPassword == "" {
		logs.Notice("web access login username:user password:%s", vkey)
	} else {
		logs.Notice("web access login username:%s password:%s", cnf.CommonConfig.Client.WebUserName, cnf.CommonConfig.Client.WebPassword)
	}
	client := NewRPClientWithTLS(cnf.CommonConfig.Server, vkey, cnf.CommonConfig.Tp, cnf.CommonConfig.ProxyUrl, cnf, cnf.CommonConfig.DisconnectTime, cnf.CommonConfig.TlsEnable, TLSOptionsFromConfig(cnf.CommonConfig))
	if modernLocalManager != nil {
		client.SetP2PManager(modernLocalManager)
	}
	client.Start()
	if client.Kicked() {
		for _, fn := range onKick {
			if fn != nil {
				fn(vkey)
			}
		}
		logs.Info("[通知] 服务端已删除本客户端，停止运行")
		if modernLocalManager != nil {
			modernLocalManager.Close()
		}
		CloseLocalServer()
		return
	}
	if modernLocalManager != nil {
		modernLocalManager.Close()
	}
	CloseLocalServer()
	goto re
}

// Create a new connection with the server and verify it
func NewConn(tp string, vkey string, server string, connType string, proxyUrl string) (*conn.Conn, error) {
	return NewConnWithTLS(tp, vkey, server, connType, proxyUrl, GetTlsEnable())
}

// NewConnWithTLS establishes a verified control/data connection using the
// caller's TLS preference. Keeping the preference on the call path avoids a
// process-wide setting leaking between concurrently running GUI clients. DNS
// configuration is process-wide and must be applied once by the application
// before it starts concurrent connections (for example, with
// common.SetCustomDNS); it is intentionally not changed on every reconnect.
func NewConnWithTLS(tp string, vkey string, server string, connType string, proxyUrl string, tlsEnabled bool, tlsOptions ...TLSOptions) (*conn.Conn, error) {
	var err error
	var connection net.Conn
	var sess *kcp.UDPSession
	transport := strings.ToLower(strings.TrimSpace(tp))
	options := TLSOptions{}
	if len(tlsOptions) > 0 {
		options = tlsOptions[0]
	}
	if transport == "tls" || transport == "tcp+tls" || transport == "tcp-tls" {
		transport = "tcp"
		tlsEnabled = true
	}
	switch transport {
	case "tcp":
		ctx, cancel := context.WithTimeout(context.Background(), clientDialTimeout)
		connection, err = dialBridgeTCP(ctx, proxyUrl, server, options.LocalIP)
		cancel()
		if err == nil && tlsEnabled {
			config, configErr := tlsClientConfig(server, options)
			if configErr != nil {
				_ = connection.Close()
				return nil, configErr
			}
			// Apply the deadline before the handshake so a stalled TLS peer cannot
			// retain a dialing goroutine indefinitely. This also covers proxy-based
			// connections, which historically skipped TLS entirely.
			_ = connection.SetDeadline(time.Now().Add(clientDialTimeout))
			tlsConnection := tls.Client(connection, config)
			if err = tlsConnection.Handshake(); err != nil {
				_ = connection.Close()
				return nil, fmt.Errorf("TLS handshake failed: %w", err)
			}
			connection = tlsConnection
		}
	case "ws", "wss":
		connection, err = dialWebSocketBridge(transport, server, proxyUrl, options)
	case "kcp":
		if strings.TrimSpace(proxyUrl) != "" || environmentProxyURL(server) != "" {
			return nil, errors.New("KCP Bridge does not support TCP-based outbound proxies; use TCP, WS, or WSS")
		}
		sess, err = conn.DialKCPWithLocalIP(server, options.LocalIP)
		if err == nil {
			conn.SetUdpSession(sess)
			connection = sess
		}
	case "quic":
		if strings.TrimSpace(proxyUrl) != "" || environmentProxyURL(server) != "" {
			return nil, errors.New("QUIC Bridge does not support TCP-based outbound proxies; use TCP, WS, or WSS")
		}
		quicTLS, tlsErr := tlsClientConfig(server, options)
		if tlsErr != nil {
			return nil, tlsErr
		}
		alpn := strings.TrimSpace(options.QuicALPN)
		if alpn == "" {
			alpn = "nps"
		}
		quicTLS.NextProtos = []string{alpn}
		quicConfig := &quic.Config{
			KeepAlivePeriod:    10 * time.Second,
			MaxIdleTimeout:     30 * time.Second,
			MaxIncomingStreams: 100000,
		}
		ctx, cancel := context.WithTimeout(context.Background(), clientDialTimeout)
		connection, err = conn.DialQuicWithLocalIP(ctx, server, quicTLS, quicConfig, options.LocalIP)
		cancel()
	default:
		return nil, fmt.Errorf("unsupported Bridge transport %q; supported types are tcp, kcp, ws, wss, and quic", tp)
	}
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, errors.New("connection was not established")
	}
	connected := false
	defer func() {
		if !connected {
			_ = connection.Close()
		}
	}()
	connection.SetDeadline(time.Now().Add(time.Second * 10))
	defer connection.SetDeadline(time.Time{})
	c := conn.NewConn(connection)
	if _, err := c.Write([]byte(common.CONN_TEST)); err != nil {
		return nil, err
	}
	if err := c.WriteLenContent([]byte(version.GetVersion())); err != nil {
		return nil, err
	}
	if err := c.WriteLenContent([]byte(version.VERSION)); err != nil {
		return nil, err
	}
	serverHandshake, err := c.GetShortContent(32)
	if err != nil {
		logs.Error(err)
		return nil, err
	}
	if version.UsesSecureHandshake(version.VERSION) && bytes.Equal(serverHandshake, crypt.SecureHandshakeMarker()) {
		if err := c.WriteLenContent(crypt.SecureHandshakeKeyID(vkey)); err != nil {
			return nil, fmt.Errorf("send secure client identifier: %w", err)
		}
		challenge, err := c.GetShortLenContent()
		if err != nil {
			return nil, fmt.Errorf("read secure server challenge: %w", err)
		}
		payload, err := crypt.SecureHandshakeClientPayload(vkey, version.GetVersion(), version.VERSION, common.TimeNow().Unix(), challenge)
		if err != nil {
			return nil, err
		}
		if err := c.WriteLenContent(payload); err != nil {
			return nil, fmt.Errorf("send secure client proof: %w", err)
		}
		if flag, err := c.ReadFlag(); err != nil {
			return nil, err
		} else if flag == common.VERIFY_EER {
			return nil, errors.New("客户端验证失败，请检查 VerifyKey、客户端状态和服务器时间")
		}
		serverProof, err := c.GetShortContent(32)
		if err != nil {
			return nil, fmt.Errorf("read secure server proof: %w", err)
		}
		if err := crypt.VerifySecureHandshakeServerProof(vkey, payload, challenge, serverProof); err != nil {
			return nil, err
		}
		if version.SupportsModernP2P(version.VERSION) {
			fingerprint, err := c.GetShortLenContent()
			if err != nil {
				return nil, fmt.Errorf("read Bridge certificate fingerprint: %w", err)
			}
			proof, err := c.GetShortContent(32)
			if err != nil {
				return nil, fmt.Errorf("read Bridge certificate fingerprint proof: %w", err)
			}
			if err := crypt.VerifySecureHandshakeFingerprintProof(vkey, fingerprint, proof); err != nil {
				return nil, err
			}
			c.PeerFingerprint = append([]byte(nil), fingerprint...)
		}
	} else {
		// An older server answers with its historical 32-byte core-version
		// digest. A 1.1.29+ client falls back only on this legacy response so it
		// can still connect to a server that has not yet been upgraded.
		if _, err := c.Write([]byte(common.Getverifyval(vkey))); err != nil {
			return nil, err
		}
		if flag, err := c.ReadFlag(); err != nil {
			return nil, err
		} else if flag == common.VERIFY_EER {
			return nil, errors.New("客户端验证失败，请检查 VerifyKey 或客户端状态")
		}
	}
	if _, err := c.Write([]byte(connType)); err != nil {
		return nil, err
	}
	c.SetAlive(tp)
	connected = true
	return c, nil
}

func dialWebSocketBridge(transport, server, proxyURL string, options TLSOptions) (net.Conn, error) {
	path := strings.TrimSpace(options.BridgePath)
	if path == "" {
		path = "/ws"
	}
	parsedPath, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(path, "/") || parsedPath.RawQuery != "" || parsedPath.Fragment != "" {
		return nil, fmt.Errorf("invalid Bridge path %q: expected an absolute URL path", path)
	}
	server = strings.TrimSpace(server)
	if strings.Contains(server, "://") {
		parsedServer, parseErr := url.Parse(server)
		if parseErr != nil || parsedServer.Host == "" {
			return nil, fmt.Errorf("invalid WebSocket Bridge address %q", server)
		}
		server = parsedServer.Host
		if options.BridgePath == "" && parsedServer.Path != "" && parsedServer.Path != "/" {
			path = parsedServer.Path
		}
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		return nil, fmt.Errorf("WebSocket Bridge address must include a port: %w", err)
	}
	scheme := transport
	endpoint := (&url.URL{Scheme: scheme, Host: server, Path: path}).String()
	dialer := websocket.Dialer{
		HandshakeTimeout:  clientDialTimeout,
		ReadBufferSize:    4096,
		WriteBufferSize:   4096,
		EnableCompression: false,
		Proxy:             nil,
		NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				return nil, fmt.Errorf("unsupported WebSocket Bridge network %q", network)
			}
			return dialBridgeTCP(ctx, proxyURL, address, options.LocalIP)
		},
	}
	if transport == "wss" {
		dialer.TLSClientConfig, err = tlsClientConfig(server, options)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), clientDialTimeout)
	defer cancel()
	websocketConn, response, err := dialer.DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s Bridge: %w", transport, err)
	}
	stream, err := conn.NewWebSocketStream(websocketConn)
	if err != nil {
		_ = websocketConn.Close()
		return nil, err
	}
	return stream, nil
}

func muxTransportType(transport string) string {
	if strings.EqualFold(strings.TrimSpace(transport), "kcp") {
		return "kcp"
	}
	return "tcp"
}

// NewHttpProxyConn preserves the historical helper while supporting the
// complete proxy URL parser used by current NPC connections.
func NewHttpProxyConn(url *url.URL, remoteAddr string) (net.Conn, error) {
	if url == nil {
		return nil, errors.New("outbound proxy URL is nil")
	}
	ctx, cancel := context.WithTimeout(context.Background(), clientDialTimeout)
	defer cancel()
	return dialBridgeTCP(ctx, url.String(), remoteAddr, "")
}

func getRemoteAddressFromServer(rAddr string, localConn *net.UDPConn, md5Password, role string, add int) error {
	rAddr, err := getNextAddr(rAddr, add)
	if err != nil {
		logs.Error(err)
		return err
	}
	addr, err := net.ResolveUDPAddr("udp", rAddr)
	if err != nil {
		return err
	}
	if _, err := localConn.WriteTo(common.GetWriteStr(md5Password, role), addr); err != nil {
		return err
	}
	return nil
}

func handleP2PUdp(localAddr, rAddr, md5Password, role string) (remoteAddress string, c net.PacketConn, err error) {
	localConn, err := newUdpConnByAddr(localAddr)
	if err != nil {
		return
	}
	defer localConn.Close()
	err = getRemoteAddressFromServer(rAddr, localConn, md5Password, role, 0)
	if err != nil {
		logs.Error(err)
		return
	}
	err = getRemoteAddressFromServer(rAddr, localConn, md5Password, role, 1)
	if err != nil {
		logs.Error(err)
		return
	}
	err = getRemoteAddressFromServer(rAddr, localConn, md5Password, role, 2)
	if err != nil {
		logs.Error(err)
		return
	}
	var remoteAddr1, remoteAddr2, remoteAddr3 string
	localConn.SetReadDeadline(time.Now().Add(time.Second * 30))
	for {
		buf := make([]byte, 1024)
		if n, addr, er := localConn.ReadFromUDP(buf); er != nil {
			err = er
			return
		} else {
			rAddr2, _ := getNextAddr(rAddr, 1)
			rAddr3, _ := getNextAddr(rAddr, 2)
			switch addr.String() {
			case rAddr:
				remoteAddr1 = string(buf[:n])
			case rAddr2:
				remoteAddr2 = string(buf[:n])
			case rAddr3:
				remoteAddr3 = string(buf[:n])
			}
		}
		if remoteAddr1 != "" && remoteAddr2 != "" && remoteAddr3 != "" {
			break
		}
	}
	if remoteAddress, err = sendP2PTestMsg(localConn, remoteAddr1, remoteAddr2, remoteAddr3); err != nil {
		return
	}
	c, err = newUdpConnByAddr(localAddr)
	return
}

func sendP2PTestMsg(localConn *net.UDPConn, remoteAddr1, remoteAddr2, remoteAddr3 string) (string, error) {
	logs.Trace(remoteAddr3, remoteAddr2, remoteAddr1)
	defer localConn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interval, err := getAddrInterval(remoteAddr1, remoteAddr2, remoteAddr3)
	if err != nil {
		return "", err
	}
	go func() {
		addr, err := getNextAddr(remoteAddr3, interval)
		if err != nil {
			return
		}
		remoteUdpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return
		}
		logs.Trace("try send test packet to target %s", addr)
		ticker := time.NewTicker(time.Millisecond * 500)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, err := localConn.WriteTo([]byte(common.WORK_P2P_CONNECT), remoteUdpAddr); err != nil {
					return
				}
			}
		}
	}()
	if interval != 0 {
		ip := common.GetIpByAddr(remoteAddr2)
		go func() {
			ports := getRandomPortArr(common.GetPortByAddr(remoteAddr3), common.GetPortByAddr(remoteAddr3)+interval*50)
			for i := 0; i <= 50; i++ {
				go func(port int) {
					trueAddress := ip + ":" + strconv.Itoa(port)
					logs.Trace("try send test packet to target %s", trueAddress)
					remoteUdpAddr, err := net.ResolveUDPAddr("udp", trueAddress)
					if err != nil {
						return
					}
					ticker := time.NewTicker(time.Second * 2)
					defer ticker.Stop()
					for {
						select {
						case <-ticker.C:
							select {
							case <-ctx.Done():
								return
							default:
							}
							if _, err := localConn.WriteTo([]byte(common.WORK_P2P_CONNECT), remoteUdpAddr); err != nil {
								return
							}
						}
					}
				}(ports[i])
				time.Sleep(time.Millisecond * 10)
			}
		}()

	}

	buf := make([]byte, 10)
	for {
		localConn.SetReadDeadline(time.Now().Add(time.Second * 10))
		n, addr, err := localConn.ReadFromUDP(buf)
		localConn.SetReadDeadline(time.Time{})
		if err != nil {
			break
		}
		switch string(buf[:n]) {
		case common.WORK_P2P_SUCCESS:
			for i := 20; i > 0; i-- {
				if _, err = localConn.WriteTo([]byte(common.WORK_P2P_END), addr); err != nil {
					return "", err
				}
			}
			return addr.String(), nil
		case common.WORK_P2P_END:
			logs.Trace("Remotely Address %s Reply Packet Successfully Received", addr.String())
			return addr.String(), nil
		case common.WORK_P2P_CONNECT:
			connectAddr := addr
			go func() {
				for i := 20; i > 0; i-- {
					logs.Trace("try send receive success packet to target %s", connectAddr.String())
					if _, writeErr := localConn.WriteTo([]byte(common.WORK_P2P_SUCCESS), connectAddr); writeErr != nil {
						return
					}
					timer := time.NewTimer(time.Second)
					select {
					case <-timer.C:
					case <-ctx.Done():
						if !timer.Stop() {
							<-timer.C
						}
						return
					}
				}
			}()
		default:
			continue
		}
	}
	return "", errors.New("connect to the target failed, maybe the nat type is not support p2p")
}

func newUdpConnByAddr(addr string) (*net.UDPConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	return udpConn, nil
}

func getNextAddr(addr string, n int) (string, error) {
	arr := strings.Split(addr, ":")
	if len(arr) != 2 {
		return "", errors.New(fmt.Sprintf("the format of %s incorrect", addr))
	}
	if p, err := strconv.Atoi(arr[1]); err != nil {
		return "", err
	} else {
		return arr[0] + ":" + strconv.Itoa(p+n), nil
	}
}

func getAddrInterval(addr1, addr2, addr3 string) (int, error) {
	arr1 := strings.Split(addr1, ":")
	if len(arr1) != 2 {
		return 0, errors.New(fmt.Sprintf("the format of %s incorrect", addr1))
	}
	arr2 := strings.Split(addr2, ":")
	if len(arr2) != 2 {
		return 0, errors.New(fmt.Sprintf("the format of %s incorrect", addr2))
	}
	arr3 := strings.Split(addr3, ":")
	if len(arr3) != 2 {
		return 0, errors.New(fmt.Sprintf("the format of %s incorrect", addr3))
	}
	p1, err := strconv.Atoi(arr1[1])
	if err != nil {
		return 0, err
	}
	p2, err := strconv.Atoi(arr2[1])
	if err != nil {
		return 0, err
	}
	p3, err := strconv.Atoi(arr3[1])
	if err != nil {
		return 0, err
	}
	interVal := int(math.Floor(math.Min(math.Abs(float64(p3-p2)), math.Abs(float64(p2-p1)))))
	if p3-p1 < 0 {
		return -interVal, nil
	}
	return interVal, nil
}

func getRandomPortArr(min, max int) []int {
	if min > max {
		min, max = max, min
	}
	addrAddr := make([]int, max-min+1)
	for i := min; i <= max; i++ {
		addrAddr[max-i] = i
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	var ri, temp int
	for i := max - min; i > 0; i-- {
		ri = r.Int() % i
		temp = addrAddr[i]
		addrAddr[i] = addrAddr[ri]
		addrAddr[ri] = temp
	}
	return addrAddr
}
