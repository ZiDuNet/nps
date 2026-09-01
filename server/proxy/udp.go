package proxy

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
)

const (
	udpSessionIdleTimeout = 120 * time.Second
	udpSweepInterval      = 30 * time.Second
	udpReadDeadline       = 60 * time.Second
	udpBuildTimeout       = 5 * time.Second
)

// udpSession 代表一个客户端 src addr ↔ npc 之间的 UDP 转发会话。
//
// 同一个 src addr 在并发场景下可能被多个 goroutine 同时尝试建立会话。为避免
// 重复建立，采用"原子占位"模式：第一个 goroutine 通过 sync.Map.LoadOrStore
// 占位（此时 ready 通道未关闭），后续 goroutine 检测到占位后阻塞在 ready
// 通道上等待会话就绪，然后复用同一个 target 转发数据。这样：
//   - 同一 src addr 始终只有 1 条到 npc 的 mux stream
//   - 只有"赢家"消耗一个 NowConn 配额，输家不再重复占用
//   - 避免了 race window 期间的 NowConn 配额泄漏
type udpSession struct {
	mu         sync.RWMutex
	writeMu    sync.Mutex
	target     io.ReadWriteCloser // 用于 Read/Write 的封装层（可能含加密/压缩）
	lastActive int64              // 最近活跃时间（unix nano，原子读写）
	ready      chan struct{}      // 会话就绪后关闭；建立失败时也关闭
	err        error              // 建立失败时设置；ready 关闭后才允许读
	closed     bool               // 会话已被清理，禁止后续安装 target
}

func (u *udpSession) touch() {
	atomic.StoreInt64(&u.lastActive, time.Now().UnixNano())
}

func (u *udpSession) setError(err error) {
	u.mu.Lock()
	u.err = err
	u.mu.Unlock()
}

func (u *udpSession) buildError() error {
	u.mu.RLock()
	err := u.err
	u.mu.RUnlock()
	return err
}

// installTarget publishes the stream only after it is fully constructed. If
// the server was closed while the bridge was dialing, close the late stream
// immediately instead of leaking it outside addrMap.
func (u *udpSession) installTarget(target io.ReadWriteCloser) bool {
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		_ = target.Close()
		return false
	}
	u.target = target
	u.mu.Unlock()
	return true
}

func (u *udpSession) getTarget() io.ReadWriteCloser {
	u.mu.RLock()
	target := u.target
	u.mu.RUnlock()
	return target
}

// write serializes packets for a single mux stream. nps_mux's send window is
// stateful and cannot be mutated by concurrent UDP receive goroutines.
func (u *udpSession) write(data []byte) (int, error) {
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	target := u.getTarget()
	if target == nil {
		return 0, io.ErrClosedPipe
	}
	return target.Write(data)
}

func (u *udpSession) closeTarget() {
	u.writeMu.Lock()
	defer u.writeMu.Unlock()
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	target := u.target
	u.target = nil
	u.mu.Unlock()
	if target != nil {
		_ = target.Close()
	}
}

type UdpModeServer struct {
	BaseServer
	addrMap    sync.Map
	listenerMu sync.RWMutex
	listener   *net.UDPConn
	closeOnce  sync.Once
	closeCh    chan struct{}
	closed     bool
}

type udpTaskSnapshot struct {
	bridgeTask   *file.Tunnel
	client       *file.Client
	clientID     int
	clientConfig file.Config
	clientFlow   *file.Flow
	taskFlow     *file.Flow
	targetStr    string
	localProxy   bool
	serverIP     string
	port         int
	taskID       int
}

func NewUdpModeServer(bridge *bridge.Bridge, task *file.Tunnel) *UdpModeServer {
	s := new(UdpModeServer)
	s.bridge = bridge
	s.task = task
	s.closeCh = make(chan struct{})
	return s
}

func (s *UdpModeServer) snapshotTask() (udpTaskSnapshot, error) {
	var snapshot udpTaskSnapshot
	if s == nil || s.task == nil || s.bridge == nil {
		return snapshot, errors.New("udp server is not configured")
	}
	s.task.RLock()
	snapshot.bridgeTask = &file.Tunnel{Mode: s.task.Mode}
	snapshot.client = s.task.Client
	snapshot.taskFlow = s.task.Flow
	snapshot.serverIP = s.task.ServerIp
	snapshot.port = s.task.Port
	snapshot.taskID = s.task.Id
	target := s.task.Target
	s.task.RUnlock()
	if snapshot.client == nil || target == nil {
		return udpTaskSnapshot{}, errors.New("udp task client or target is not configured")
	}
	snapshot.client.RLock()
	if snapshot.client.Cnf == nil {
		snapshot.client.RUnlock()
		return udpTaskSnapshot{}, errors.New("udp task client configuration is not configured")
	}
	snapshot.clientConfig = *snapshot.client.Cnf
	snapshot.clientID = snapshot.client.Id
	snapshot.clientFlow = snapshot.client.Flow
	snapshot.client.RUnlock()
	target.RLock()
	snapshot.targetStr = target.TargetStr
	snapshot.localProxy = target.LocalProxy
	target.RUnlock()
	if strings.TrimSpace(snapshot.serverIP) == "" {
		snapshot.serverIP = "0.0.0.0"
	}
	if snapshot.port <= 0 || strings.TrimSpace(snapshot.targetStr) == "" {
		return udpTaskSnapshot{}, errors.New("udp task port or target is not configured")
	}
	return snapshot, nil
}

// Start 启动 UDP 监听，主循环只负责快速收包并分发，不做任何耗时操作。
func (s *UdpModeServer) Start() error {
	snapshot, err := s.snapshotTask()
	if err != nil {
		return err
	}
	s.listenerMu.RLock()
	closed := s.closed
	s.listenerMu.RUnlock()
	if closed {
		return net.ErrClosed
	}
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(snapshot.serverIP), Port: snapshot.port})
	if err != nil {
		return err
	}
	s.listenerMu.Lock()
	if s.closed {
		s.listenerMu.Unlock()
		_ = listener.Close()
		return net.ErrClosed
	}
	s.listener = listener
	s.listenerMu.Unlock()
	go s.sweeper()
	for {
		buf := common.BufPoolUdp.Get().([]byte)
		n, addr, err := listener.ReadFromUDP(buf)
		if err != nil {
			common.BufPoolUdp.Put(buf)
			if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			continue
		}

		if IsGlobalBlackIp(addr.String()) {
			common.BufPoolUdp.Put(buf)
			continue
		}
		if isIPWhiteBlocked(snapshot.client, addr.String()) {
			// UDP has no HTTP challenge channel; unauthorized datagrams are
			// dropped before a mux stream or connection quota is allocated.
			common.BufPoolUdp.Put(buf)
			continue
		}
		if isClientBlackBlocked(snapshot.client, addr.String()) {
			common.BufPoolUdp.Put(buf)
			continue
		}

		go s.process(addr, buf, n)
	}
	return nil
}

// process 处理单个 UDP 包。函数持有 buf 的所有权，所有路径必须归还。
func (s *UdpModeServer) process(addr *net.UDPAddr, buf []byte, n int) {
	// ReadFromUDP may have completed just before Close shut down the listener.
	// Do not create a new bridge session after the server has begun closing.
	select {
	case <-s.closeCh:
		common.BufPoolUdp.Put(buf)
		return
	default:
	}

	key := addr.String()
	data := buf[:n]

	// 快路径：会话已存在
	if v, ok := s.addrMap.Load(key); ok {
		s.dispatch(key, v.(*udpSession), data, n)
		common.BufPoolUdp.Put(buf)
		return
	}

	// 慢路径：尝试成为该 key 的"会话建立者"
	placeholder := &udpSession{ready: make(chan struct{})}
	placeholder.touch()
	if existing, loaded := s.addrMap.LoadOrStore(key, placeholder); loaded {
		// 输了占位竞争，等赢家把会话建好后复用
		s.dispatch(key, existing.(*udpSession), data, n)
		common.BufPoolUdp.Put(buf)
		return
	}

	// 赢得占位 —— 我去建立会话
	s.runSession(addr, key, placeholder, buf, n)
}

// dispatch 把数据写入 sess.target。若 sess 仍在建立中则阻塞等待，超时则丢包。
func (s *UdpModeServer) dispatch(key string, sess *udpSession, data []byte, n int) {
	if sess.ready != nil {
		select {
		case <-sess.ready:
			if err := sess.buildError(); err != nil {
				logs.Trace("udp session build failed for %s: %v", key, err)
				return
			}
		case <-time.After(udpBuildTimeout):
			logs.Warn("udp session build timeout for %s, drop packet", key)
			return
		case <-s.closeCh:
			return
		}
	}
	if _, err := sess.write(data); err != nil {
		logs.Warn(err)
		s.removeSession(key, sess)
		return
	}
	sess.touch()
	if snapshot, err := s.snapshotTask(); err == nil {
		if snapshot.clientFlow != nil {
			snapshot.clientFlow.Add(int64(n), int64(n))
		}
		if snapshot.taskFlow != nil {
			snapshot.taskFlow.Add(int64(n), int64(n))
		}
	}
}

// runSession 由占位赢家执行：建立到 npc 的 stream、发送首包、运行下行读循环。
// buf 由本函数负责归还。
func (s *UdpModeServer) runSession(addr *net.UDPAddr, key string, sess *udpSession, buf []byte, n int) {
	data := buf[:n]
	snapshot, snapshotErr := s.snapshotTask()

	// 失败时统一清理：关 ready 通道唤醒所有输家、删占位、归还 buf。
	failBuild := func(err error) {
		sess.setError(err)
		close(sess.ready)
		s.deleteSession(key, sess)
		common.BufPoolUdp.Put(buf)
	}
	if snapshotErr != nil {
		failBuild(snapshotErr)
		return
	}

	if err := s.CheckFlowAndConnNum(snapshot.client); err != nil {
		logs.Warn("client id %d, task id %d,error %s, when udp connection", snapshot.clientID, snapshot.taskID, err.Error())
		failBuild(err)
		return
	}
	// 只有赢家消耗 NowConn 配额，函数返回时释放。
	defer snapshot.client.AddConn()

	link := conn.NewLink(common.CONN_UDP, snapshot.targetStr, snapshot.clientConfig.Crypt, snapshot.clientConfig.Compress, addr.String(), snapshot.localProxy, "")
	type linkResult struct {
		target net.Conn
		err    error
	}
	resultCh := make(chan linkResult, 1)
	go func() {
		clientConn, err := s.bridge.SendLinkInfo(snapshot.clientID, link, snapshot.bridgeTask)
		select {
		case resultCh <- linkResult{target: clientConn, err: err}:
		case <-s.closeCh:
			if clientConn != nil {
				_ = clientConn.Close()
			}
		}
	}()
	var result linkResult
	select {
	case result = <-resultCh:
	case <-s.closeCh:
		failBuild(errors.New("udp server is closed"))
		return
	}
	clientConn, err := result.target, result.err
	if err != nil {
		failBuild(err)
		return
	}

	target := conn.GetConn(clientConn, snapshot.clientConfig.Crypt, snapshot.clientConfig.Compress, nil, true)
	if !sess.installTarget(target) {
		close(sess.ready)
		common.BufPoolUdp.Put(buf)
		return
	}
	sess.touch()
	close(sess.ready) // 唤醒所有等待该会话的输家

	defer s.removeSession(key, sess)

	logs.Trace("New udp connection,client %d,remote address %s", snapshot.clientID, addr)

	if _, err := sess.write(data); err != nil {
		logs.Warn(err)
		common.BufPoolUdp.Put(buf)
		return
	}
	common.BufPoolUdp.Put(buf)
	if snapshot.clientFlow != nil {
		snapshot.clientFlow.Add(int64(n), int64(n))
	}
	if snapshot.taskFlow != nil {
		snapshot.taskFlow.Add(int64(n), int64(n))
	}

	// 下行读循环
	rbuf := common.BufPoolUdp.Get().([]byte)
	defer common.BufPoolUdp.Put(rbuf)

	for {
		// 设到 rawConn 上：mux stream 的 SetReadDeadline 是底层 net.Conn 实现，
		// 比设在加密/压缩包装层 target 上更可靠。
		clientConn.SetReadDeadline(time.Now().Add(udpReadDeadline))
		rn, err := target.Read(rbuf)
		if err != nil {
			// sweeper 主动 Close、idle deadline 触发、或对端断开都会落到这里
			return
		}
		sess.touch()
		s.listenerMu.RLock()
		listener := s.listener
		s.listenerMu.RUnlock()
		if listener == nil {
			return
		}
		if _, err := listener.WriteTo(rbuf[:rn], addr); err != nil {
			logs.Warn(err)
			return
		}
		if snapshot.clientFlow != nil {
			snapshot.clientFlow.Add(int64(rn), int64(rn))
		}
		if snapshot.taskFlow != nil {
			snapshot.taskFlow.Add(int64(rn), int64(rn))
		}
	}
}

// removeSession 安全地从 addrMap 移除并关闭会话。
// 用 == 比对避免误删被替换的新 session（虽然当前协议不会发生，但保持防御性）。
func (s *UdpModeServer) removeSession(key string, sess *udpSession) {
	s.deleteSession(key, sess)
	sess.closeTarget()
}

func (s *UdpModeServer) deleteSession(key string, sess *udpSession) {
	// Load followed by Delete is racy: a newer session can replace the entry
	// between those operations and then be removed by the stale cleanup.
	s.addrMap.CompareAndDelete(key, sess)
}

// sweeper 周期性扫描 addrMap，把空闲超过 udpSessionIdleTimeout 的会话清掉。
// 关闭 target 会让阻塞中的 target.Read 立即返回错误，让对应的 runSession
// 走 defer 链路自然退出（释放 NowConn 配额、删 addrMap 条目）。
func (s *UdpModeServer) sweeper() {
	ticker := time.NewTicker(udpSweepInterval)
	defer ticker.Stop()
	idleNs := int64(udpSessionIdleTimeout)
	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			now := time.Now().UnixNano()
			s.addrMap.Range(func(k, v interface{}) bool {
				sess := v.(*udpSession)
				// 跳过仍在建立中的占位（target 尚未填充）
				if sess.getTarget() == nil {
					return true
				}
				if now-atomic.LoadInt64(&sess.lastActive) > idleNs {
					s.removeSession(k.(string), sess)
				}
				return true
			})
		}
	}
}

func (s *UdpModeServer) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeCh)
	})
	s.listenerMu.Lock()
	s.closed = true
	listener := s.listener
	s.listener = nil
	s.listenerMu.Unlock()
	s.addrMap.Range(func(k, v interface{}) bool {
		s.removeSession(k.(string), v.(*udpSession))
		return true
	})
	if listener == nil {
		return nil
	}
	return listener.Close()
}
