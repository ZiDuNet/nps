package proxy

import (
	"errors"
	"net"
	"net/http"
	"sync"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
)

type Service interface {
	Start() error
	Close() error
}

type NetBridge interface {
	SendLinkInfo(clientId int, link *conn.Link, t *file.Tunnel) (target net.Conn, err error)
}

// BaseServer struct
type BaseServer struct {
	id           int
	bridge       NetBridge
	task         *file.Tunnel
	errorContent []byte
	sync.Mutex
}

func NewBaseServer(bridge *bridge.Bridge, task *file.Tunnel) *BaseServer {
	return &BaseServer{
		bridge:       bridge,
		task:         task,
		errorContent: nil,
		Mutex:        sync.Mutex{},
	}
}

// add the flow
func (s *BaseServer) FlowAdd(in, out int64) {
	if s.task != nil && s.task.Flow != nil {
		s.task.Flow.Add(in, out)
	}
}

// change the flow
func (s *BaseServer) FlowAddHost(host *file.Host, in, out int64) {
	if host != nil && host.Flow != nil {
		host.Flow.Add(in, out)
	}
}

// write fail bytes to the connection
func (s *BaseServer) writeConnFail(c net.Conn) {
	s.writeConnFailContent(c, s.errorContent)
}

// writeConnFailContent writes a request-specific failure body. HTTP proxy
// handlers may customize the body per request, so callers should pass their
// local snapshot instead of mutating BaseServer.errorContent.
func (s *BaseServer) writeConnFailContent(c net.Conn, content []byte) {
	c.Write([]byte(common.ConnectionFailBytes))
	if len(content) > 0 {
		_, _ = c.Write(content)
	}
}

// auth check for reverse-proxy hosts (401 + WWW-Authenticate).
func (s *BaseServer) auth(r *http.Request, c *conn.Conn, u, p string) error {
	return s.doAuth(r, c, u, p, common.UnauthorizedBytes, "401 Unauthorized")
}

// proxyAuth check for HTTP forward proxy (407 + Proxy-Authenticate).
func (s *BaseServer) proxyAuth(r *http.Request, c *conn.Conn, u, p string) error {
	return s.doAuth(r, c, u, p, common.ProxyAuthRequiredBytes, "407 Proxy Authentication Required")
}

func (s *BaseServer) doAuth(r *http.Request, c *conn.Conn, u, p, failBytes, errMsg string) error {
	if u != "" && p != "" && !common.CheckAuth(r, u, p) {
		c.Write([]byte(failBytes))
		c.Close()
		return errors.New(errMsg)
	}
	return nil
}

// check flow limit of the client ,and decrease the allow num of client
func (s *BaseServer) CheckFlowAndConnNum(client *file.Client) error {
	if client == nil {
		return errors.New("client is nil")
	}
	if client.Flow != nil && client.Flow.Exceeded() {
		return errors.New("Traffic exceeded")
	}
	if !client.GetConn() {
		return errors.New("Connections exceed the current client limit")
	}
	return nil
}

func in(target string, str_array []string) bool {
	for _, value := range str_array {
		if value == target {
			return true
		}
	}
	return false
}

// create a new connection and start bytes copying
func (s *BaseServer) DealClient(c *conn.Conn, client *file.Client, addr string,
	rb []byte, tp string, f func(), flow *file.Flow, localProxy bool, task *file.Tunnel, host *file.Host) error {

	// 判断访问地址是否在全局黑名单内
	if IsGlobalBlackIp(c.RemoteAddr().String()) {
		c.Close()
		return nil
	}

	// 判断访问地址是否在黑名单内
	if common.IsBlackIp(c.RemoteAddr().String(), client.VerifyKey, client.BlackIpList) {
		c.Close()
		return nil
	}

	protoVersion := ""
	if task != nil {
		protoVersion = task.ProtoVersion
	}

	link := conn.NewLink(tp, addr, client.Cnf.Crypt, client.Cnf.Compress, c.Conn.RemoteAddr().String(), localProxy, protoVersion)
	if target, err := s.bridge.SendLinkInfo(client.Id, link, s.task); err != nil {
		logs.Warn("get connection from client id %d  error %s", client.Id, err.Error())
		c.Close()
		return err
	} else {
		if f != nil {
			f()
		}
		conn.CopyWaitGroup(target, c.Conn, link.Crypt, link.Compress, client.Rate, flow, true, rb, task, host)
	}
	return nil
}

// 判断访问地址是否在全局黑名单内
func IsGlobalBlackIp(ipPort string) bool {
	// 判断访问地址是否在全局黑名单内
	global := file.GetDb().GetGlobal()
	if global != nil {
		ip := common.GetIpByAddr(ipPort)
		if in(ip, global.BlackIpList) {
			logs.Error("IP地址[" + ip + "]在全局黑名单列表内")
			return true
		}
	}

	return false
}
