package server

import (
	"ehang.io/nps/lib/version"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/bridge"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/server/proxy"
	"ehang.io/nps/server/tool"
	"github.com/astaxie/beego"
	"github.com/astaxie/beego/logs"
)

var (
	Bridge          *bridge.Bridge
	RunList         sync.Map // task IDs plus the private management service key
	once            sync.Once
	taskLifecycleMu sync.Mutex
)

// managementRunListKey is deliberately not an int. Task IDs are public input
// at the Web/API boundary, so management listeners must not share that key
// space even when a malformed request uses zero or a negative value.
type managementRunListKey struct{ _ byte }

var managementServiceKey managementRunListKey

// managementService owns both listeners that are started for the default
// server process: the HTTP/HTTPS virtual-host proxy and the Web management
// panel. Its RunList entry uses managementServiceKey, which is outside the
// task ID namespace.
type managementService struct {
	web  proxy.Service
	host proxy.Service
}

func (s *managementService) Start() error {
	if s == nil || s.web == nil {
		return errors.New("management service is not configured")
	}
	hostStarted := false
	if s.host != nil {
		if err := s.host.Start(); err != nil {
			// The management panel is the recovery path for a bad or occupied
			// HTTP(S) proxy port, so keep it available in degraded mode.
			logs.Warn("start HTTP/HTTPS host proxy failed: %v", err)
		} else {
			hostStarted = true
		}
	}
	if err := s.web.Start(); err != nil {
		if hostStarted {
			_ = s.host.Close()
		}
		return err
	}
	return nil
}

func (s *managementService) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	if s.web != nil {
		if err := s.web.Close(); err != nil {
			closeErr = err
		}
	}
	if s.host != nil {
		if err := s.host.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func init() {
	RunList = sync.Map{}
}

func forEachStoredTunnel(tasks *sync.Map, visit func(*file.Tunnel)) {
	if tasks == nil || visit == nil {
		return
	}
	tasks.Range(func(key, value interface{}) bool {
		task, ok := value.(*file.Tunnel)
		if ok && task != nil {
			visit(task)
		}
		return true
	})
}

// init task from db
func InitFromCsv() {
	//Add a public password
	if vkey := beego.AppConfig.String("public_vkey"); vkey != "" {
		c := file.NewClient(vkey, true, true)
		if err := file.GetDb().NewClient(c); err != nil {
			logs.Warn("restore public vkey client failed: %v", err)
		}
	}
	//Initialize services in server-side files
	forEachStoredTunnel(&file.GetDb().JsonDb.Tasks, func(task *file.Tunnel) {
		task.RLock()
		status := task.Status
		task.RUnlock()
		if status {
			if err := AddTask(task); err != nil {
				logs.Warn("restore task failed: %v", err)
			}
		}
	})
}

// get bridge command
func DealBridgeTask() {
	for {
		select {
		case t := <-Bridge.OpenTask:
			if t != nil {
				StartTask(t.Id)
			}
		case t := <-Bridge.CloseTask:
			if t != nil {
				StopServer(t.Id)
			}
		case id := <-Bridge.CloseClient:
			DelTunnelAndHostByClientId(id, true)
			if v, ok := file.GetDb().JsonDb.Clients.Load(id); ok {
				client, valid := v.(*file.Client)
				if valid && client != nil && client.NoStore {
					file.GetDb().DelClient(id)
				}
			}
		case s := <-Bridge.SecretChan:
			if s == nil || s.Conn == nil || s.Conn.Conn == nil {
				continue
			}
			logs.Trace("New secret connection, addr", s.Conn.Conn.RemoteAddr())
			if t := file.GetDb().GetTaskByMd5Password(s.Password); t != nil {
				t.RLock()
				status, mode := t.Status, t.Mode
				client, target, flow := t.Client, t.Target, t.Flow
				t.RUnlock()
				if status && mode == "secret" && client != nil && target != nil {
					target.RLock()
					targetStr, localProxy := target.TargetStr, target.LocalProxy
					target.RUnlock()
					base := proxy.NewBaseServer(Bridge, t)
					if err := base.CheckFlowAndConnNum(client); err != nil {
						_ = s.Conn.Close()
						logs.Warn("reject secret connection for client quota: %s", err)
						continue
					}
					secretConn := s.Conn
					go func() {
						defer client.AddConn()
						if err := base.DealClient(secretConn, client, targetStr, nil, common.CONN_TCP, nil, flow, localProxy, nil, nil); err != nil {
							logs.Warn("secret connection failed: %s", err)
						}
					}()
				} else {
					_ = s.Conn.Close()
					logs.Trace("This key %s cannot be processed,status is close", s.Password)
				}
			} else {
				logs.Trace("This key %s cannot be processed", s.Password)
				s.Conn.Close()
			}
		}
	}
}

// start a new server
func StartNewServer(bridgePort int, cnf *file.Tunnel, bridgeType string, bridgeDisconnect int) error {
	if cnf == nil {
		return errors.New("management service configuration is nil")
	}
	Bridge = bridge.NewTunnel(bridgePort, bridgeType, common.GetBoolByStr(beego.AppConfig.String("ip_limit")), &RunList, bridgeDisconnect)
	if err := Bridge.StartTunnel(); err != nil {
		return err
	}
	if p, err := beego.AppConfig.Int("p2p_port"); err == nil {
		go proxy.NewP2PServer(p).Start()
		go proxy.NewP2PServer(p + 1).Start()
		go proxy.NewP2PServer(p + 2).Start()
	}
	go DealBridgeTask()
	go dealClientFlow()
	go dealClientExpire()
	tool.StartIORateCollector()
	if minute, err := beego.AppConfig.Int("flow_store_interval"); err == nil && minute > 0 {
		go flowSession(time.Minute * time.Duration(minute))
	}
	// Restore persisted services before constructing the synthetic management
	// service. NewMode must remain side-effect free so it can also be called by
	// addTask while taskLifecycleMu is held.
	InitFromCsv()
	if svr := NewMode(Bridge, cnf); svr != nil {
		if cnf.Mode == "webServer" {
			// Hosts are stored independently from tunnel tasks. Start the shared
			// HTTP/HTTPS virtual-host proxy alongside the management panel so a
			// fresh server still serves persisted host entries.
			hostTask := &file.Tunnel{Id: -1, Mode: "httpHostServer", Status: true}
			svr = &managementService{web: svr, host: NewMode(Bridge, hostTask)}
		}
		RunList.Store(managementServiceKey, svr)
		// WebServer.Start serves until Close, so register the real service before
		// launching it. The management listener is intentionally not addressable
		// through StopServer, whose IDs are untrusted task IDs.
		go func() {
			if err := svr.Start(); err != nil {
				logs.Error(err)
				RunList.CompareAndDelete(managementServiceKey, svr)
			}
		}()
	} else {
		return errors.New("incorrect startup mode " + cnf.Mode)
	}
	return nil
}

func dealClientFlow() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			dealClientData()
		}
	}
}

// new a server by mode name
func NewMode(Bridge *bridge.Bridge, c *file.Tunnel) proxy.Service {
	if c == nil {
		return nil
	}
	var service proxy.Service
	switch c.Mode {
	case "tcp", "file":
		service = proxy.NewTunnelModeServer(proxy.ProcessTunnel, Bridge, c)
	case "socks5":
		service = proxy.NewSock5ModeServer(Bridge, c)
	case "httpProxy":
		service = proxy.NewTunnelModeServer(proxy.ProcessHttp, Bridge, c)
	case "tcpTrans":
		service = proxy.NewTunnelModeServer(proxy.HandleTrans, Bridge, c)
	case "udp":
		service = proxy.NewUdpModeServer(Bridge, c)
	case "webServer":
		service = proxy.NewWebServer(Bridge)
	case "httpHostServer":
		httpPort, _ := beego.AppConfig.Int("http_proxy_port")
		httpsPort, _ := beego.AppConfig.Int("https_proxy_port")
		useCache, _ := beego.AppConfig.Bool("http_cache")
		cacheLen, _ := beego.AppConfig.Int("http_cache_length")
		addOrigin, _ := beego.AppConfig.Bool("http_add_origin_header")
		service = proxy.NewHttp(Bridge, c, httpPort, httpsPort, useCache, cacheLen, addOrigin)
	}
	return service
}

// stop server
func StopServer(id int) error {
	if err := validateTaskID(id); err != nil {
		return err
	}
	taskLifecycleMu.Lock()
	defer taskLifecycleMu.Unlock()
	return stopServer(id)
}

func validateTaskID(id int) error {
	if id <= 0 {
		return errors.New("invalid task ID")
	}
	return nil
}

func stopServer(id int) error {
	if v, ok := RunList.Load(id); ok {
		if svr, ok := v.(proxy.Service); ok {
			if err := svr.Close(); err != nil {
				logs.Error("stop server id %d error", id, err)
				return err
			}
		} else {
			logs.Warn("stop server id %d error", id)
		}
		RunList.Delete(id)
		if t, err := file.GetDb().GetTask(id); err == nil {
			t.Lock()
			t.Status = false
			port, remark, taskID := t.Port, t.Remark, t.Id
			clientID := 0
			if t.Client != nil {
				clientID = t.Client.Id
			}
			t.Unlock()
			logs.Info("close port %d,remark %s,client id %d,task id %d", port, remark, clientID, taskID)
			file.GetDb().UpdateTask(t)
		}
		return nil
	}
	return errors.New("task is not running")
}

// add task
func AddTask(t *file.Tunnel) error {
	if t == nil {
		return errors.New("task is nil")
	}
	if err := validateTaskID(t.Id); err != nil {
		return err
	}
	taskLifecycleMu.Lock()
	defer taskLifecycleMu.Unlock()
	return addTask(t)
}

func addTask(t *file.Tunnel) error {
	if t == nil {
		return errors.New("task is nil")
	}
	if _, running := RunList.Load(t.Id); running {
		return errors.New("task is already running")
	}
	if t.Mode == "secret" || t.Mode == "p2p" {
		logs.Info("secret task %s start ", t.Remark)
		//RunList[t.Id] = nil
		RunList.Store(t.Id, nil)
		return nil
	}
	if b := tool.TestServerPort(t.Port, t.Mode); !b && t.Mode != "httpHostServer" {
		logs.Error("taskId %d start error port %d open failed", t.Id, t.Port)
		return errors.New("the port open error")
	}
	if svr := NewMode(Bridge, t); svr != nil {
		logs.Info("tunnel task %s start mode：%s port %d", t.Remark, t.Mode, t.Port)
		//RunList[t.Id] = svr
		RunList.Store(t.Id, svr)
		t.RLock()
		clientID := 0
		if t.Client != nil {
			clientID = t.Client.Id
		}
		taskID := t.Id
		t.RUnlock()
		go func() {
			if err := svr.Start(); err != nil {
				logs.Error("clientId %d taskId %d start error %s", clientID, taskID, err)
				taskLifecycleMu.Lock()
				defer taskLifecycleMu.Unlock()
				// A delete or replacement can happen while Start is binding. Never
				// restore the failed, stale task into persistent storage.
				current, exists := file.GetDb().JsonDb.Tasks.Load(taskID)
				if !exists || current != t {
					RunList.CompareAndDelete(taskID, svr)
					return
				}
				if RunList.CompareAndDelete(taskID, svr) {
					t.Lock()
					t.Status = false
					t.Unlock()
					if updateErr := file.GetDb().UpdateTask(t); updateErr != nil {
						logs.Warn("persist failed task start state for task %d: %v", taskID, updateErr)
					}
				}
				return
			}
		}()
	} else {
		return errors.New("the mode is not correct")
	}
	return nil
}

// start task
func StartTask(id int) error {
	if err := validateTaskID(id); err != nil {
		return err
	}
	taskLifecycleMu.Lock()
	defer taskLifecycleMu.Unlock()
	if t, err := file.GetDb().GetTask(id); err != nil {
		return err
	} else {
		if _, running := RunList.Load(id); running {
			return errors.New("task is already running")
		}
		// Publish the desired state before launching the asynchronous listener.
		// Start may fail immediately (for example, a bind error); setting this
		// first prevents that goroutine from racing a later status update.
		t.Lock()
		t.Status = true
		t.Unlock()
		if err := file.GetDb().UpdateTask(t); err != nil {
			return err
		}
		if err := addTask(t); err != nil {
			t.Lock()
			t.Status = false
			t.Unlock()
			_ = file.GetDb().UpdateTask(t)
			return err
		}
	}
	return nil
}

// delete task
func DelTask(id int) error {
	if err := validateTaskID(id); err != nil {
		return err
	}
	taskLifecycleMu.Lock()
	defer taskLifecycleMu.Unlock()
	if _, ok := RunList.Load(id); ok {
		if err := stopServer(id); err != nil {
			return err
		}
	}
	return file.GetDb().DelTask(id)
}

// get task list by page num
func GetTunnel(start, length int, typeVal string, clientId int, search string, sortField string, order string) ([]*file.Tunnel, int) {
	return GetTunnelByOwnerFilter(start, length, typeVal, clientId, search, sortField, order, nil, nil)
}

func GetTunnelByAllowedClients(start, length int, typeVal string, clientId int, search string, sortField string, order string, allowedClientIds map[int]struct{}) ([]*file.Tunnel, int) {
	return GetTunnelByOwnerFilter(start, length, typeVal, clientId, search, sortField, order, nil, allowedClientIds)
}

// GetTunnelByOwnerFilter applies dashboard ownership before sorting, search,
// counting, and pagination. A zero-valued owner filter intentionally matches
// tunnels attached to unassigned clients.
func GetTunnelByOwnerFilter(start, length int, typeVal string, clientId int, search string, sortField string, order string, owner *file.OwnerFilter, allowedClientIds map[int]struct{}) ([]*file.Tunnel, int) {
	all_list := make([]*file.Tunnel, 0) //store all Tunnel
	list := make([]*file.Tunnel, 0)
	var cnt int
	searchInt, searchIntErr := strconv.Atoi(strings.TrimSpace(search))
	keys := file.GetMapKeys(&file.GetDb().JsonDb.Tasks, false, "", "")

	//get all Tunnel and sort
	for _, key := range keys {
		if value, ok := file.GetDb().JsonDb.Tasks.Load(key); ok {
			v, valid := value.(*file.Tunnel)
			if !valid || v == nil {
				continue
			}
			client := tunnelClient(v)
			if client == nil {
				continue
			}
			clientID, _ := clientSnapshot(client)
			if !isClientAllowed(clientID, allowedClientIds) {
				continue
			}
			if !tunnelOwnerMatches(client, owner) {
				continue
			}
			v.RLock()
			mode := v.Mode
			v.RUnlock()
			if (typeVal != "" && mode != typeVal) || (clientId != 0 && clientID != clientId) || (typeVal == "" && clientId != 0 && clientId != clientID) {
				continue
			}
			all_list = append(all_list, v)
		}
	}
	//sort by Id, Remark, TargetStr, Port, asc or desc
	if sortField == "Id" {
		if order == "asc" {
			sort.SliceStable(all_list, func(i, j int) bool { return all_list[i].Id < all_list[j].Id })
		} else {
			sort.SliceStable(all_list, func(i, j int) bool { return all_list[i].Id > all_list[j].Id })
		}
	} else if sortField == "ClientId" {
		if order == "asc" {
			sort.SliceStable(all_list, func(i, j int) bool { return tunnelClientID(all_list[i]) < tunnelClientID(all_list[j]) })
		} else {
			sort.SliceStable(all_list, func(i, j int) bool { return tunnelClientID(all_list[i]) > tunnelClientID(all_list[j]) })
		}
	} else if sortField == "Remark" {
		if order == "asc" {
			sort.SliceStable(all_list, func(i, j int) bool { return all_list[i].Remark < all_list[j].Remark })
		} else {
			sort.SliceStable(all_list, func(i, j int) bool { return all_list[i].Remark > all_list[j].Remark })
		}
	} else if sortField == "Client.VerifyKey" {
		if order == "asc" {
			sort.SliceStable(all_list, func(i, j int) bool { return tunnelClientVerifyKey(all_list[i]) < tunnelClientVerifyKey(all_list[j]) })
		} else {
			sort.SliceStable(all_list, func(i, j int) bool { return tunnelClientVerifyKey(all_list[i]) > tunnelClientVerifyKey(all_list[j]) })
		}
	} else if sortField == "Target" {
		if order == "asc" {
			sort.SliceStable(all_list, func(i, j int) bool { return tunnelTargetString(all_list[i]) < tunnelTargetString(all_list[j]) })
		} else {
			sort.SliceStable(all_list, func(i, j int) bool { return tunnelTargetString(all_list[i]) > tunnelTargetString(all_list[j]) })
		}
	}

	//search
	for _, key := range all_list {
		if value, ok := file.GetDb().JsonDb.Tasks.Load(key.Id); ok {
			v, valid := value.(*file.Tunnel)
			if !valid || v == nil {
				continue
			}
			client := tunnelClient(v)
			if client == nil {
				continue
			}
			clientID, _ := clientSnapshot(client)
			if !isClientAllowed(clientID, allowedClientIds) {
				continue
			}
			if !tunnelOwnerMatches(client, owner) {
				continue
			}
			v.RLock()
			mode, password, remark := v.Mode, v.Password, v.Remark
			v.RUnlock()
			if (typeVal != "" && mode != typeVal) || (clientId != 0 && clientID != clientId) || (typeVal == "" && clientId != 0 && clientId != clientID) {
				continue
			}
			v.RLock()
			taskID, port := v.Id, v.Port
			v.RUnlock()
			numericMatch := search != "" && searchIntErr == nil && (taskID == searchInt || port == searchInt)
			if search != "" && !(numericMatch || strings.Contains(password, search) || strings.Contains(remark, search) || strings.Contains(tunnelTargetString(v), search)) {
				continue
			}
			cnt++
			connected := bridgeClientConnected(clientID)
			client.Lock()
			client.IsConnect = connected
			client.Unlock()
			if start--; start < 0 {
				if length--; length >= 0 {
					_, running := RunList.Load(taskID)
					v.Lock()
					v.RunStatus = running
					v.Unlock()
					list = append(list, v)
				}
			}
		}
	}
	return list, cnt
}

func tunnelOwnerMatches(client *file.Client, owner *file.OwnerFilter) bool {
	if owner == nil || owner.UserID == nil {
		return true
	}
	if client == nil {
		return false
	}
	userID := file.GetDb().GetClientOwnerID(client)
	return userID == *owner.UserID
}

// get client list
func GetClientList(start, length int, search, sort, order string, clientId int) (list []*file.Client, cnt int) {
	list, cnt = GetClientListByOwnerFilter(start, length, search, sort, order, clientId, nil, nil)
	return
}

// GetClientListByOwnerFilter keeps the server-side client status refresh used
// by the historical list endpoint while adding optional admin ownership
// filtering.
func GetClientListByOwnerFilter(start, length int, search, sort, order string, clientId int, owner *file.OwnerFilter, allowedClientIds map[int]struct{}) (list []*file.Client, cnt int) {
	list, cnt = file.GetDb().GetClientListFiltered(start, length, search, sort, order, clientId, owner, allowedClientIds)
	dealClientData()
	return
}

// GetClientListForAllowedIds applies ownership filtering before pagination.
// This keeps ordinary-user page counts and offsets independent of other users'
// clients while preserving the same sorting and search behavior as the admin
// list.
func GetClientListForAllowedIds(start, length int, search, sort, order string, clientId int, allowedClientIds map[int]struct{}) (list []*file.Client, cnt int) {
	return GetClientListByOwnerAndAllowedIds(start, length, search, sort, order, clientId, nil, allowedClientIds)
}

func GetClientListByOwnerAndAllowedIds(start, length int, search, sort, order string, clientId int, owner *file.OwnerFilter, allowedClientIds map[int]struct{}) (list []*file.Client, cnt int) {
	return GetClientListByOwnerFilter(start, length, search, sort, order, clientId, owner, allowedClientIds)
}

func FilterClientsByUserId(clients []*file.Client, userId int) []*file.Client {
	list := make([]*file.Client, 0, len(clients))
	for _, client := range clients {
		if client == nil {
			continue
		}
		client.RLock()
		belongsToUser := client.UserId == userId
		client.RUnlock()
		if belongsToUser {
			list = append(list, client)
		}
	}
	return list
}

func FilterClientsByAllowedIds(clients []*file.Client, allowedClientIds map[int]struct{}) []*file.Client {
	list := make([]*file.Client, 0, len(clients))
	for _, client := range clients {
		if client == nil {
			continue
		}
		clientID, _ := clientSnapshot(client)
		if isClientAllowed(clientID, allowedClientIds) {
			list = append(list, client)
		}
	}
	return list
}

func FilterTunnelsByAllowedClients(tunnels []*file.Tunnel, allowedClientIds map[int]struct{}) []*file.Tunnel {
	list := make([]*file.Tunnel, 0, len(tunnels))
	for _, tunnel := range tunnels {
		client := tunnelClient(tunnel)
		if client == nil {
			continue
		}
		clientID, _ := clientSnapshot(client)
		if isClientAllowed(clientID, allowedClientIds) {
			list = append(list, tunnel)
		}
	}
	return list
}

func isClientAllowed(clientId int, allowedClientIds map[int]struct{}) bool {
	if allowedClientIds == nil {
		return true
	}
	_, ok := allowedClientIds[clientId]
	return ok
}

func tunnelClient(tunnel *file.Tunnel) *file.Client {
	if tunnel == nil {
		return nil
	}
	tunnel.RLock()
	client := tunnel.Client
	tunnel.RUnlock()
	return client
}

func clientSnapshot(client *file.Client) (id int, verifyKey string) {
	if client == nil {
		return 0, ""
	}
	client.RLock()
	id, verifyKey = client.Id, client.VerifyKey
	client.RUnlock()
	return
}

func tunnelClientID(tunnel *file.Tunnel) int {
	return func() int {
		id, _ := clientSnapshot(tunnelClient(tunnel))
		return id
	}()
}

func tunnelClientVerifyKey(tunnel *file.Tunnel) string {
	_, verifyKey := clientSnapshot(tunnelClient(tunnel))
	return verifyKey
}

func tunnelTargetString(tunnel *file.Tunnel) string {
	if tunnel == nil {
		return ""
	}
	tunnel.RLock()
	target := tunnel.Target
	tunnel.RUnlock()
	if target == nil {
		return ""
	}
	target.RLock()
	targetString := target.TargetStr
	target.RUnlock()
	return targetString
}

func bridgeClientConnected(clientID int) bool {
	if Bridge == nil {
		return false
	}
	_, connected := Bridge.Client.Load(clientID)
	return connected
}

func dealClientData() {

	file.GetDb().JsonDb.Clients.Range(func(key, value interface{}) bool {
		v, ok := value.(*file.Client)
		if !ok || v == nil {
			return true
		}
		connected := false
		version := ""
		clientID, _ := clientSnapshot(v)
		if Bridge != nil {
			if vv, ok := Bridge.Client.Load(clientID); ok {
				connected = true
				if client, ok := vv.(*bridge.Client); ok {
					version = client.VersionSnapshot()
				}
			}
		}
		v.RLock()
		userID := v.UserId
		v.RUnlock()
		userName := ""
		if userID != 0 {
			if user, err := file.GetDb().GetUser(userID); err == nil {
				user.RLock()
				userName = user.UserName
				user.RUnlock()
			}
		}
		v.Lock()
		v.IsConnect = connected
		if connected {
			v.LastOnlineTime = time.Now().Format("2006-01-02 15:04:05")
			v.Version = version
		}
		v.UserName = userName
		if v.Rate == nil {
			v.Rate = rate.NewRate(0)
		}
		v.Unlock()
		return true
	})
	return
}

// delete all host and tasks by client id
func DelTunnelAndHostByClientId(clientId int, justDelNoStore bool) {
	var ids []int
	file.GetDb().JsonDb.Tasks.Range(func(key, value interface{}) bool {
		v, ok := value.(*file.Tunnel)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		noStore, id, client := v.NoStore, v.Id, v.Client
		v.RUnlock()
		if justDelNoStore && !noStore {
			return true
		}
		if client != nil {
			currentClientID, _ := clientSnapshot(client)
			if currentClientID == clientId {
				ids = append(ids, id)
			}
		}
		return true
	})
	for _, id := range ids {
		DelTask(id)
	}
	ids = ids[:0]
	file.GetDb().JsonDb.Hosts.Range(func(key, value interface{}) bool {
		v, ok := value.(*file.Host)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		noStore, id, client := v.NoStore, v.Id, v.Client
		v.RUnlock()
		if justDelNoStore && !noStore {
			return true
		}
		if client != nil {
			currentClientID, _ := clientSnapshot(client)
			if currentClientID == clientId {
				ids = append(ids, id)
			}
		}
		return true
	})
	for _, id := range ids {
		file.GetDb().DelHost(id)
	}
}

// close the client
func DelClientConnect(clientId int) {
	if Bridge != nil {
		Bridge.DelClient(clientId)
	}
}

// RevokeUserClients disconnects every client owned by a disabled/expired user
// and removes its active proxy records. Existing TCP/UDP sessions therefore
// cannot continue operating after the account is suspended.
func RevokeUserClients(userID int) {
	revokeUserClientsWith(userID, &file.GetDb().JsonDb.Clients, DelClientConnect, DelTunnelAndHostByClientId)
}

// revokeUserClientsWith disconnects and removes proxy resources using
// injectable callbacks. The production wrapper above supplies the server
// lifecycle functions; keeping this helper pure makes revocation observable
// without opening real listeners in tests.
func revokeUserClientsWith(userID int, clients *sync.Map, disconnect func(int), removeResources func(int, bool)) {
	if userID <= 0 || clients == nil {
		return
	}
	clientIDs := make([]int, 0)
	clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*file.Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		belongs, clientID := client.UserId == userID, client.Id
		client.RUnlock()
		if belongs {
			clientIDs = append(clientIDs, clientID)
		}
		return true
	})
	for _, clientID := range clientIDs {
		if disconnect != nil {
			disconnect(clientID)
		}
		if removeResources != nil {
			removeResources(clientID, false)
		}
	}
}

func GetDashboardData() map[string]interface{} {
	return getDashboardData(nil, true)
}

// GetDashboardDataForClients returns a dashboard snapshot scoped to the
// supplied client IDs. A non-nil allowlist is a hard data boundary: clients,
// tunnels, hosts, flow totals, rates, quotas and status rows are all derived
// from that set. Pass nil only for an administrator's global overview.
func GetDashboardDataForClients(allowedClientIds map[int]struct{}) map[string]interface{} {
	return getDashboardData(allowedClientIds, allowedClientIds == nil)
}

func getDashboardData(allowedClientIds map[int]struct{}, isAdmin bool) map[string]interface{} {
	data := make(map[string]interface{})
	data["dashboardIsAdmin"] = isAdmin
	data["dashboardScope"] = "user"
	if isAdmin {
		data["dashboardScope"] = "global"
	}
	data["systemInfoDisplay"] = isAdmin && beego.AppConfig.DefaultBool("system_info_display", true)
	data["version"] = version.VERSION
	clientCount := 0
	hostCount := 0
	tunnelCount := 0
	clientOnlineCount := 0
	currentProxyConnections := 0
	var in, out int64
	clients := make(map[int]*file.Client)
	file.GetDb().JsonDb.Clients.Range(func(key, value interface{}) bool {
		client, ok := value.(*file.Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		clientID, noDisplay, connected, flow := client.Id, client.NoDisplay, client.IsConnect, client.Flow
		client.RUnlock()
		if noDisplay || !dashboardClientAllowed(clientID, allowedClientIds) {
			return true
		}
		clients[clientID] = client
		clientCount++
		if connected {
			clientOnlineCount++
		}
		currentProxyConnections += int(atomic.LoadInt32(&client.NowConn))
		if flow != nil {
			clientIn, clientOut, _ := flow.Snapshot()
			in += clientIn
			out += clientOut
		}
		return true
	})
	data["hostCount"] = hostCount
	data["clientCount"] = clientCount
	if isAdmin {
		dealClientData()
	} else {
		refreshDashboardClients(clients)
	}
	// dealClientData refreshes bridge-backed online state. Re-read the selected
	// clients after it runs so the returned snapshot is current and scoped.
	clientOnlineCount = 0
	currentProxyConnections = 0
	in, out = 0, 0
	for _, client := range clients {
		client.RLock()
		connected, flow := client.IsConnect, client.Flow
		client.RUnlock()
		if connected {
			clientOnlineCount++
		}
		currentProxyConnections += int(atomic.LoadInt32(&client.NowConn))
		if flow != nil {
			clientIn, clientOut, _ := flow.Snapshot()
			in += clientIn
			out += clientOut
		}
	}
	data["clientOnlineCount"] = clientOnlineCount
	data["inletFlowCount"] = int(in)
	data["exportFlowCount"] = int(out)
	var tcp, udp, secret, socks5, p2p, http int
	file.GetDb().JsonDb.Tasks.Range(func(key, value interface{}) bool {
		task, ok := value.(*file.Tunnel)
		if !ok || task == nil {
			return true
		}
		task.RLock()
		client := task.Client
		task.RUnlock()
		if client == nil {
			return true
		}
		if !dashboardClientVisible(client, allowedClientIds) {
			return true
		}
		tunnelCount++
		task.RLock()
		mode := task.Mode
		task.RUnlock()
		switch mode {
		case "tcp":
			tcp += 1
		case "socks5":
			socks5 += 1
		case "httpProxy":
			http += 1
		case "udp":
			udp += 1
		case "p2p":
			p2p += 1
		case "secret":
			secret += 1
		}
		return true
	})
	file.GetDb().JsonDb.Hosts.Range(func(key, value interface{}) bool {
		host, ok := value.(*file.Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		client := host.Client
		host.RUnlock()
		if client == nil {
			return true
		}
		if dashboardClientVisible(client, allowedClientIds) {
			hostCount++
		}
		return true
	})
	data["hostCount"] = hostCount
	data["tunnelCount"] = tunnelCount

	data["tcpC"] = tcp
	data["udpCount"] = udp
	data["socks5Count"] = socks5
	data["httpProxyCount"] = http
	data["secretCount"] = secret
	data["p2pCount"] = p2p
	data["tcpCount"] = currentProxyConnections
	data["currentProxyConnections"] = currentProxyConnections
	var proxyInRate, proxyOutRate int64
	for _, client := range clients {
		client.RLock()
		flow := client.Flow
		client.RUnlock()
		if flow != nil {
			inRate, outRate := flow.RateSnapshot()
			proxyInRate += inRate
			proxyOutRate += outRate
		}
	}
	data["proxyInRate"] = proxyInRate
	data["proxyOutRate"] = proxyOutRate
	data["proxyRate"] = map[string]int64{"in": proxyInRate, "out": proxyOutRate}
	updatedAt := time.Now().Format(time.RFC3339)
	data["updatedAt"] = updatedAt
	data["lastUpdated"] = updatedAt
	runtimeRows := dashboardRuntimeStatus(clients, allowedClientIds)
	runtimeSummary := dashboardRuntimeSummary(runtimeRows, proxyInRate, proxyOutRate)
	data["runtimeStatus"] = runtimeSummary
	data["runtimeRows"] = runtimeRows
	data["resourceStatus"] = map[string]int{
		"running": dashboardSummaryInt(runtimeSummary, "tunnelRunning"),
		"stopped": dashboardSummaryInt(runtimeSummary, "tunnelStopped"),
		"waiting": dashboardSummaryInt(runtimeSummary, "tunnelWaiting"),
	}
	data["pendingItems"] = dashboardPendingItems(clients, allowedClientIds)
	data["quotas"] = dashboardQuotaRows(clients)
	data["runningTunnels"] = runtimeSummary["tunnelRunning"]
	data["stoppedTunnels"] = runtimeSummary["tunnelStopped"]
	if !isAdmin {
		// Host-machine CPU, memory, socket and NIC samples, as well as listener
		// configuration, are administrator-only. Resource counters above are
		// already derived from the allowlist and remain safe for this scope.
		return data
	}
	data["bridgeType"] = beego.AppConfig.String("bridge_type")
	data["httpProxyPort"] = beego.AppConfig.String("http_proxy_port")
	data["httpsProxyPort"] = beego.AppConfig.String("https_proxy_port")
	data["ipLimit"] = beego.AppConfig.String("ip_limit")
	data["flowStoreInterval"] = beego.AppConfig.String("flow_store_interval")
	data["serverIp"] = beego.AppConfig.String("p2p_ip")
	data["p2pPort"] = beego.AppConfig.String("p2p_port")
	data["logLevel"] = beego.AppConfig.String("log_level")
	for key, value := range tool.GetSystemStatus() {
		data[key] = value
	}
	for index, status := range tool.GetServerStatusSamples(10) {
		data["sys"+strconv.Itoa(index+1)] = status
	}
	return data
}

func dashboardSummaryInt(summary map[string]interface{}, key string) int {
	value, ok := summary[key]
	if !ok {
		return 0
	}
	count, ok := value.(int)
	if !ok {
		return 0
	}
	return count
}

func dashboardClientAllowed(clientID int, allowedClientIds map[int]struct{}) bool {
	if allowedClientIds == nil {
		return true
	}
	_, ok := allowedClientIds[clientID]
	return ok
}

func dashboardClientVisible(client *file.Client, allowedClientIds map[int]struct{}) bool {
	if client == nil {
		return false
	}
	client.RLock()
	clientID, noDisplay := client.Id, client.NoDisplay
	client.RUnlock()
	return !noDisplay && dashboardClientAllowed(clientID, allowedClientIds)
}

// refreshDashboardClients updates only the clients present in the current
// scope. Administrators use dealClientData, which also refreshes display-only
// metadata; user snapshots avoid touching clients outside their allowlist.
func refreshDashboardClients(clients map[int]*file.Client) {
	for clientID, client := range clients {
		if client == nil {
			continue
		}
		connected := bridgeClientConnected(clientID)
		client.Lock()
		client.IsConnect = connected
		if connected {
			client.LastOnlineTime = time.Now().Format("2006-01-02 15:04:05")
		}
		client.Unlock()
	}
}

type dashboardRuntimeRow struct {
	Kind      string `json:"kind"`
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ClientID  int    `json:"clientId"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

func dashboardRuntimeStatus(clients map[int]*file.Client, allowedClientIds map[int]struct{}) []dashboardRuntimeRow {
	rows := make([]dashboardRuntimeRow, 0)
	for clientID, client := range clients {
		client.RLock()
		status, connected, remark, expireTime := client.Status, client.IsConnect, client.Remark, client.ExpireTime
		client.RUnlock()
		state := "离线"
		if !status {
			state = "已停用"
		} else if clientNearExpiry(expireTime, time.Now()) {
			state = "即将到期"
		} else if connected {
			state = "在线"
		}
		rows = append(rows, dashboardRuntimeRow{Kind: "client", ID: clientID, Name: remark, Status: state, ClientID: clientID})
	}
	file.GetDb().JsonDb.Tasks.Range(func(key, value interface{}) bool {
		task, ok := value.(*file.Tunnel)
		if !ok || task == nil {
			return true
		}
		task.RLock()
		id, remark, status, client := task.Id, task.Remark, task.Status, task.Client
		task.RUnlock()
		if client == nil {
			return true
		}
		client.RLock()
		clientID, connected, clientEnabled := client.Id, client.IsConnect, client.Status
		client.RUnlock()
		if !dashboardClientVisible(client, allowedClientIds) {
			return true
		}
		_, running := RunList.Load(id)
		state := "已停止"
		if !clientEnabled {
			state = "已停止"
		} else if status && !connected {
			state = "等待客户端连接"
		} else if status && running {
			state = "运行中"
		}
		rows = append(rows, dashboardRuntimeRow{Kind: "tunnel", ID: id, Name: remark, Status: state, ClientID: clientID})
		return true
	})
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

func dashboardRuntimeSummary(rows []dashboardRuntimeRow, proxyInRate, proxyOutRate int64) map[string]interface{} {
	summary := map[string]interface{}{
		"clientOnline":   0,
		"clientOffline":  0,
		"clientDisabled": 0,
		"clientExpiring": 0,
		"tunnelRunning":  0,
		"tunnelStopped":  0,
		"tunnelWaiting":  0,
		"proxyInRate":    proxyInRate,
		"proxyOutRate":   proxyOutRate,
	}
	for _, row := range rows {
		var key string
		switch row.Kind {
		case "client":
			switch row.Status {
			case "在线":
				key = "clientOnline"
			case "已停用":
				key = "clientDisabled"
			case "即将到期":
				key = "clientExpiring"
			default:
				key = "clientOffline"
			}
		case "tunnel":
			switch row.Status {
			case "运行中":
				key = "tunnelRunning"
			case "等待客户端连接":
				key = "tunnelWaiting"
			default:
				key = "tunnelStopped"
			}
		}
		if key != "" {
			summary[key] = summary[key].(int) + 1
		}
	}
	return summary
}

type dashboardQuotaRow struct {
	ClientID        int    `json:"clientId"`
	ClientName      string `json:"clientName"`
	TunnelUsed      int    `json:"tunnelUsed"`
	TunnelLimit     int    `json:"tunnelLimit"`
	ConnectionUsed  int32  `json:"connectionUsed"`
	ConnectionLimit int    `json:"connectionLimit"`
	FlowUsedBytes   int64  `json:"flowUsedBytes"`
	FlowLimitBytes  int64  `json:"flowLimitBytes"`
}

func dashboardQuotaRows(clients map[int]*file.Client) []dashboardQuotaRow {
	rows := make([]dashboardQuotaRow, 0, len(clients))
	for clientID, client := range clients {
		if client == nil {
			continue
		}
		client.RLock()
		name, maxTunnel, maxConn, nowConn, flow := client.Remark, client.MaxTunnelNum, client.MaxConn, atomic.LoadInt32(&client.NowConn), client.Flow
		client.RUnlock()
		var flowUsed, flowLimit int64
		if flow != nil {
			inlet, export, limit := flow.Snapshot()
			flowUsed = inlet + export
			if limit > 0 {
				flowLimit = limit << 20
			}
		}
		rows = append(rows, dashboardQuotaRow{
			ClientID:        clientID,
			ClientName:      name,
			TunnelUsed:      client.GetTunnelNum(),
			TunnelLimit:     maxTunnel,
			ConnectionUsed:  nowConn,
			ConnectionLimit: maxConn,
			FlowUsedBytes:   flowUsed,
			FlowLimitBytes:  flowLimit,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ClientID < rows[j].ClientID })
	return rows
}

func dashboardPendingItems(clients map[int]*file.Client, allowedClientIds map[int]struct{}) []string {
	pending := make([]string, 0)
	now := time.Now()
	for _, client := range clients {
		client.RLock()
		maxConn, nowConn, maxTunnel, status, flow, expireTime := client.MaxConn, atomic.LoadInt32(&client.NowConn), client.MaxTunnelNum, client.Status, client.Flow, client.ExpireTime
		client.RUnlock()
		if !status {
			continue
		}
		if maxConn > 0 && dashboardNearLimit(int(nowConn), maxConn) {
			pending = append(pending, "客户端连接数接近上限")
		}
		if maxTunnel > 0 {
			owned := client.GetTunnelNum()
			if dashboardNearLimit(owned, maxTunnel) {
				pending = append(pending, "隧道数量接近上限")
			}
		}
		if flow != nil {
			inlet, export, limit := flow.Snapshot()
			if limit > 0 {
				if dashboardNearLimitBytes(inlet+export, limit<<20) {
					pending = append(pending, "流量配额接近上限")
				}
			}
		}
		if clientNearExpiry(expireTime, now) {
			pending = append(pending, "客户端即将到期")
		}
	}
	if dashboardHasUnhealthyResource(allowedClientIds) {
		pending = append(pending, "后端健康检查异常")
	}
	return uniqueDashboardStrings(pending)
}

func dashboardNearLimit(current, limit int) bool {
	if current < 0 || limit <= 0 {
		return false
	}
	threshold := (limit*80 + 99) / 100
	if threshold < 1 {
		threshold = 1
	}
	return current >= threshold
}

func dashboardNearLimitBytes(current, limit int64) bool {
	if current < 0 || limit <= 0 {
		return false
	}
	threshold := (limit*80 + 99) / 100
	if threshold < 1 {
		threshold = 1
	}
	return current >= threshold
}

func dashboardHasUnhealthyResource(allowedClientIds map[int]struct{}) bool {
	unhealthy := false
	file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
		task, ok := value.(*file.Tunnel)
		if !ok || task == nil {
			return true
		}
		task.RLock()
		client := task.Client
		task.RUnlock()
		if !dashboardClientVisible(client, allowedClientIds) {
			return true
		}
		// Health state has its own lock because the client health reporter updates
		// it independently of the tunnel's metadata lock.
		task.Health.RLock()
		unhealthy = dashboardHealthFailed(task.Health.HealthMap, task.Health.HealthRemoveArr, task.Health.HealthMaxFail)
		task.Health.RUnlock()
		return !unhealthy
	})
	if unhealthy {
		return true
	}
	file.GetDb().JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*file.Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		client := host.Client
		host.RUnlock()
		if !dashboardClientVisible(client, allowedClientIds) {
			return true
		}
		host.Health.RLock()
		unhealthy = dashboardHealthFailed(host.Health.HealthMap, host.Health.HealthRemoveArr, host.Health.HealthMaxFail)
		host.Health.RUnlock()
		return !unhealthy
	})
	return unhealthy
}

func dashboardHealthFailed(failures map[string]int, removed []string, maxFail int) bool {
	if len(removed) > 0 {
		return true
	}
	if maxFail <= 0 {
		return false
	}
	for _, count := range failures {
		if count >= maxFail {
			return true
		}
	}
	return false
}

func uniqueDashboardStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func clientNearExpiry(expire string, now time.Time) bool {
	expire = strings.TrimSpace(expire)
	if expire == "" {
		return false
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		parsed, err := time.ParseInLocation(layout, expire, time.Local)
		if err == nil {
			return parsed.After(now) && parsed.Sub(now) <= 7*24*time.Hour
		}
	}
	return false
}

// 实例化流量数据到文件
func flowSession(m time.Duration) {
	once.Do(func() {
		ticker := time.NewTicker(m)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				file.GetDb().JsonDb.StoreHostToJsonFile()
				file.GetDb().JsonDb.StoreTasksToJsonFile()
				file.GetDb().JsonDb.StoreClientsToJsonFile()
				file.GetDb().JsonDb.StoreGlobalToJsonFile()
			}
		}
	})
}

func dealClientExpire() {
	// 启动时立即检查一次，避免重启后到期客户端最多1分钟内才被暂停
	checkClientExpire()
	checkUserExpire()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			checkClientExpire()
			checkUserExpire()
		}
	}
}

func checkClientExpire() {
	now := time.Now()
	changed := false
	file.GetDb().JsonDb.Clients.Range(func(key, value interface{}) bool {
		v, ok := value.(*file.Client)
		if !ok || v == nil {
			return true
		}
		v.Lock()
		if v.ExpireTime == "" || !v.Status {
			v.Unlock()
			return true
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", v.ExpireTime, time.Local)
		if err != nil {
			v.Unlock()
			return true
		}
		if now.Before(t) {
			v.Unlock()
			return true
		}
		v.Status = false
		clientID, remark, expireTime := v.Id, v.Remark, v.ExpireTime
		v.Unlock()
		changed = true
		DelClientConnect(clientID)
		DelTunnelAndHostByClientId(clientID, false)
		logs.Info("client id %d (remark: %s) expired at %s, auto paused", clientID, remark, expireTime)
		return true
	})
	if changed {
		file.GetDb().JsonDb.StoreClientsToJsonFile()
	}
}

func checkUserExpire() {
	now := time.Now()
	changed := false
	file.GetDb().JsonDb.Users.Range(func(key, value interface{}) bool {
		v, ok := value.(*file.User)
		if !ok || v == nil {
			return true
		}
		v.Lock()
		if v.ExpireTime == "" || !v.Status {
			v.Unlock()
			return true
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", v.ExpireTime, time.Local)
		if err != nil || now.Before(t) {
			v.Unlock()
			return true
		}
		v.Status = false
		userID, userName, expireTime := v.Id, v.UserName, v.ExpireTime
		v.Unlock()
		changed = true
		RevokeUserClients(userID)
		logs.Info("user id %d (username: %s) expired at %s, auto paused", userID, userName, expireTime)
		return true
	})
	if changed {
		file.GetDb().JsonDb.StoreUsersToJsonFile()
	}
}
