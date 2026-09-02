package goroutine

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
	"github.com/panjf2000/ants/v2"
)

const maxAuthIPRequestSize = 8 << 10

var errAuthIPHandled = errors.New("ip whitelist request handled")

// parseAuthIPRequest recognizes the allowlist challenge endpoint without
// trusting query-string credentials. It returns false until the complete form
// body is available, allowing a TCP read boundary to fall anywhere in the
// request.
func parseAuthIPRequest(data []byte) (pass string, ok bool) {
	pass, isAuthRequest, complete := inspectAuthIPRequest(data)
	return pass, isAuthRequest && complete
}

// inspectAuthIPRequest reports whether data is (or could still become) the
// POST /authIp form, and whether its full body has arrived. Credentials in the
// query string are deliberately ignored.
func inspectAuthIPRequest(data []byte) (pass string, isAuthRequest, complete bool) {
	const requestPrefix = "POST /authIp"
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd < 0 {
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) <= len(requestPrefix) && bytes.Equal(trimmed, []byte(requestPrefix[:len(trimmed)])) {
			return "", true, false
		}
		return "", false, true
	}

	parts := strings.Fields(string(bytes.TrimSpace(data[:lineEnd])))
	if len(parts) < 2 || parts[0] != http.MethodPost {
		return "", false, true
	}
	u, err := url.Parse(parts[1])
	if err != nil || u.Path != "/authIp" {
		return "", false, true
	}

	headerEnd := bytes.Index(data, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		return "", true, false
	}

	var contentLength int64
	for _, line := range strings.Split(string(data[lineEnd+1:headerEnd]), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			contentLength, err = strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || contentLength < 0 || contentLength > maxAuthIPRequestSize {
				return "", true, true
			}
			break
		}
		if strings.EqualFold(strings.TrimSpace(key), "Transfer-Encoding") {
			return "", true, true
		}
	}
	bodyStart := headerEnd + 4
	if int64(len(data)-bodyStart) < contentLength {
		return "", true, false
	}
	values, err := url.ParseQuery(string(data[bodyStart : bodyStart+int(contentLength)]))
	if err != nil {
		return "", true, true
	}
	return values.Get("pass"), true, true
}

func authClientSnapshot(client *file.Client) (enabled bool, pass, vkey string, allowlist []string) {
	client.RLock()
	defer client.RUnlock()
	enabled = client.IpWhite
	pass = client.IpWhitePass
	vkey = client.VerifyKey
	allowlist = append([]string(nil), client.IpWhiteList...)
	return
}

func authPasswordMatches(stored, supplied string) bool {
	return subtle.ConstantTimeCompare([]byte(html.UnescapeString(stored)), []byte(supplied)) == 1
}

func addAuthIP(client *file.Client, ip string) {
	client.Lock()
	defer client.Unlock()
	for _, existing := range client.IpWhiteList {
		if existing == ip {
			return
		}
	}
	client.IpWhiteList = append(client.IpWhiteList, ip)
}

func writeAuthResponse(src io.Reader, dst io.Writer, response string) {
	if connSrc, ok := src.(net.Conn); ok {
		_, _ = connSrc.Write([]byte(response))
		_ = connSrc.Close()
		return
	}
	_, _ = dst.Write([]byte(response))
}

type connGroup struct {
	src    io.ReadWriteCloser
	dst    io.ReadWriteCloser
	wg     *sync.WaitGroup
	n      *int64
	flow   *file.Flow
	task   *file.Tunnel
	host   *file.Host
	remote string
}

//func newConnGroup(dst, src io.ReadWriteCloser, wg *sync.WaitGroup, n *int64) connGroup {
//	return connGroup{
//		src: src,
//		dst: dst,
//		wg:  wg,
//		n:   n,
//	}
//}

func newConnGroup(dst, src io.ReadWriteCloser, wg *sync.WaitGroup, n *int64, flow *file.Flow, task *file.Tunnel, host *file.Host, remote string) connGroup {
	return connGroup{
		src:    src,
		dst:    dst,
		wg:     wg,
		n:      n,
		flow:   flow,
		task:   task,
		host:   host,
		remote: remote,
	}
}

func CopyBuffer(dst io.Writer, src io.Reader, flow *file.Flow, task *file.Tunnel, host *file.Host, remote string) (err error) {
	return CopyBufferWithFlows(dst, src, flow, nil, task, host, remote)
}

// CopyBufferWithFlows copies bytes while accounting them against the primary
// flow and any additional flows. The extra flow list is useful when one byte
// stream belongs to more than one ownership scope, such as a Host response
// that must count toward both the Host and its Client totals.
func CopyBufferWithFlows(dst io.Writer, src io.Reader, flow *file.Flow, additionalFlows []*file.Flow, task *file.Tunnel, host *file.Host, remote string) (err error) {
	buf := common.CopyBuff.Get()
	defer common.CopyBuff.Put(buf)
	var taskClient *file.Client
	var taskFlow *file.Flow
	if task != nil {
		task.RLock()
		taskClient, taskFlow = task.Client, task.Flow
		task.RUnlock()
	}
	var hostClient *file.Client
	var hostFlow *file.Flow
	if host != nil {
		host.RLock()
		hostClient, hostFlow = host.Client, host.Flow
		host.RUnlock()
	}
	var authRequest []byte
	authInspectionDone := false
	for {
		if len(buf) <= 0 {
			break
		}
		nr, er := src.Read(buf)

		_, inbound := src.(net.Conn)
		if inbound && !authInspectionDone && taskClient != nil {
			enabled, whitePass, verifyKey, allowlist := authClientSnapshot(taskClient)
			if enabled && whitePass != "" {
				if common.IsAuthIp(remote, verifyKey, allowlist) {
					ip := common.GetIpByAddr(remote)
					var jsonBytes []byte
					authRequest = append(authRequest, buf[:nr]...)
					pass, isAuthRequest, complete := inspectAuthIPRequest(authRequest)
					if len(authRequest) > maxAuthIPRequestSize {
						// Bound buffering for ordinary or malformed requests. A request
						// that is already identified as /authIp must be rejected rather
						// than forwarded to the protected target.
						if isAuthRequest {
							complete = true
						}
					} else if !isAuthRequest {
						complete = true
					} else if !complete {
						if er == nil {
							continue
						}
						// A truncated challenge request cannot be forwarded.
						isAuthRequest = false
						complete = true
					}

					// 优先处理客户端直接访问的 POST /authIp 请求，直接响应给客户端，不经隧道转发
					if isAuthRequest && complete {
						authInspectionDone = true
						authRequest = nil
						if pass != "" && authPasswordMatches(whitePass, pass) {
							addAuthIP(taskClient, ip)
							file.GetDb().JsonDb.StoreClientsToJsonFile()
							taskClient.RLock()
							clientID := taskClient.Id
							taskClient.RUnlock()
							logs.Info("客户端IP白名单认证授权成功:client_id [%d] ip [%s]", clientID, ip)
							jsonBytes, err = json.Marshal(map[string]interface{}{"success": true, "message": "授权成功"})
						} else {
							taskClient.RLock()
							clientID := taskClient.Id
							taskClient.RUnlock()
							logs.Error("客户端IP白名单认证授权密码错误:client_id [%d] ip [%s]", clientID, ip)
							jsonBytes, err = json.Marshal(map[string]interface{}{"success": false, "message": "密码错误"})
						}
						response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(jsonBytes), jsonBytes)
						writeAuthResponse(src, dst, response)
						return errAuthIPHandled
					}

					// Every other first packet from a blocked address is rejected. In
					// particular, do not mark inspection complete and forward binary
					// protocols to the protected target.
					authInspectionDone = true
					authRequest = nil
					errorContent, _ := common.ReadAllFromFile(filepath.Join(common.GetRunPath(), "web", "static", "page", "auth.html"))
					authHtml := string(errorContent)
					authHtml = strings.ReplaceAll(authHtml, "${ip}", html.EscapeString(ip))
					response := fmt.Sprintf("HTTP/1.1 401 Unauthorized\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(authHtml), authHtml)
					writeAuthResponse(src, dst, response)
					return errAuthIPHandled
				}
			}
		}

		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				trafficExceeded := false
				//written += int64(nw)
				if flow != nil {
					flow.Add(int64(nw), int64(nw))
					// <<20 = 1024 * 1024
					if flow.Exceeded() {
						clientID := 0
						if taskClient != nil {
							taskClient.RLock()
							clientID = taskClient.Id
							taskClient.RUnlock()
						} else if hostClient != nil {
							hostClient.RLock()
							clientID = hostClient.Id
							hostClient.RUnlock()
						}
						logs.Error("客户端[%d]流量已经超出", clientID)
						err = errors.New("traffic exceeded")
						trafficExceeded = true
					}
				}
				if taskFlow != nil && flow != taskFlow {
					taskFlow.Add(int64(nw), int64(nw))
				}
				if hostFlow != nil && flow != hostFlow {
					hostFlow.Add(int64(nw), int64(nw))
				}
				for _, additionalFlow := range additionalFlows {
					if additionalFlow != nil && additionalFlow != flow && additionalFlow != taskFlow && additionalFlow != hostFlow {
						additionalFlow.Add(int64(nw), int64(nw))
						if additionalFlow.Exceeded() {
							clientID := 0
							if taskClient != nil {
								taskClient.RLock()
								clientID = taskClient.Id
								taskClient.RUnlock()
							} else if hostClient != nil {
								hostClient.RLock()
								clientID = hostClient.Id
								hostClient.RUnlock()
							}
							logs.Error("客户端[%d]流量已经超出", clientID)
							err = errors.New("traffic exceeded")
							trafficExceeded = true
							break
						}
					}
				}
				if trafficExceeded {
					break
				}
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			err = er
			break
		}
	}
	return err
}

func copyConnGroup(group interface{}) {
	//logs.Info("copyConnGroup.........")
	cg, ok := group.(connGroup)
	if !ok {
		return
	}

	var err error
	err = CopyBuffer(cg.dst, cg.src, cg.flow, cg.task, cg.host, cg.remote)
	if err != nil {
		cg.src.Close()
		cg.dst.Close()
		//logs.Warn("close npc by copy from nps", err, c.connId)
	}

	//if conns.flow != nil {
	//	conns.flow.Add(in, out)
	//}
	cg.wg.Done()
}

type Conns struct {
	conn1 io.ReadWriteCloser // mux connection
	conn2 net.Conn           // outside connection
	flow  *file.Flow
	wg    *sync.WaitGroup
	task  *file.Tunnel
	host  *file.Host
}

func NewConns(c1 io.ReadWriteCloser, c2 net.Conn, flow *file.Flow, wg *sync.WaitGroup, task *file.Tunnel, host *file.Host) Conns {
	return Conns{
		conn1: c1,
		conn2: c2,
		flow:  flow,
		wg:    wg,
		task:  task,
		host:  host,
	}
}

func copyConns(group interface{}) {
	//logs.Info("copyConns.........")
	conns := group.(Conns)
	wg := new(sync.WaitGroup)
	wg.Add(2)
	var in, out int64
	remoteAddr := conns.conn2.RemoteAddr().String()
	_ = connCopyPool.Invoke(newConnGroup(conns.conn1, conns.conn2, wg, &in, conns.flow, conns.task, conns.host, remoteAddr))
	// outside to mux : incoming
	_ = connCopyPool.Invoke(newConnGroup(conns.conn2, conns.conn1, wg, &out, conns.flow, conns.task, conns.host, remoteAddr))
	// mux to outside : outgoing
	wg.Wait()
	//if conns.flow != nil {
	//	conns.flow.Add(in, out)
	//}
	conns.wg.Done()
}

var connCopyPool, _ = ants.NewPoolWithFunc(200000, copyConnGroup, ants.WithNonblocking(false))
var CopyConnsPool, _ = ants.NewPoolWithFunc(100000, copyConns, ants.WithNonblocking(false))
