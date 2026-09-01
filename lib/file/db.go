package file

import (
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/rate"
)

type DbUtils struct {
	JsonDb *JsonDb
}

var (
	Db             *DbUtils
	once           sync.Once
	hostMutationMu sync.Mutex
)

// init csv from file
func GetDb() *DbUtils {
	once.Do(func() {
		jsonDb := NewJsonDb(common.GetRunPath())
		jsonDb.LoadClientFromJsonFile()
		jsonDb.LoadUserFromJsonFile()
		jsonDb.LoadTaskFromJsonFile()
		jsonDb.LoadHostFromJsonFile()
		jsonDb.LoadGlobalFromJsonFile()
		Db = &DbUtils{JsonDb: jsonDb}
		if err := Db.MigrateUsersFromClients(); err != nil {
			// 迁移失败不影响主流程，保留旧客户端登录兼容。
		}
	})
	return Db
}

func GetMapKeys(m *sync.Map, isSort bool, sortKey, order string) (keys []int) {
	if sortKey != "" && isSort {
		return sortClientByKey(m, sortKey, order)
	}
	m.Range(func(key, value interface{}) bool {
		if id, ok := key.(int); ok {
			keys = append(keys, id)
		}
		return true
	})
	sort.Ints(keys)
	return
}

func (s *DbUtils) GetClientList(start, length int, search, sort, order string, clientId int) ([]*Client, int) {
	list := make([]*Client, 0)
	var cnt int
	keys := GetMapKeys(&s.JsonDb.Clients, true, sort, order)
	for _, key := range keys {
		if value, ok := s.JsonDb.Clients.Load(key); ok {
			v, valid := value.(*Client)
			if !valid || v == nil {
				continue
			}
			v.RLock()
			noDisplay, candidateID := v.NoDisplay, v.Id
			verifyKey, remark := v.VerifyKey, v.Remark
			v.RUnlock()
			if noDisplay {
				continue
			}
			if clientId != 0 && clientId != candidateID {
				continue
			}
			if search != "" && !(candidateID == common.GetIntNoErrByStr(search) || strings.Contains(verifyKey, search) || strings.Contains(remark, search)) {
				continue
			}
			cnt++
			if start--; start < 0 {
				if length--; length >= 0 {
					list = append(list, v)
				}
			}
		}
	}
	return list, cnt
}

func (s *DbUtils) GetUserList(start, length int, search string) ([]*User, int) {
	list := make([]*User, 0)
	all := make([]*User, 0)
	s.JsonDb.Users.Range(func(key, value interface{}) bool {
		v, ok := value.(*User)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		userID, userName, remark := v.Id, v.UserName, v.Remark
		v.RUnlock()
		if search != "" && !(userID == common.GetIntNoErrByStr(search) || strings.Contains(userName, search) || strings.Contains(remark, search)) {
			return true
		}
		all = append(all, v)
		return true
	})
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].Id < all[j].Id
	})
	cnt := len(all)
	for _, user := range all {
		if start--; start < 0 {
			if length--; length >= 0 {
				list = append(list, user)
			}
		}
	}
	return list, cnt
}

func (s *DbUtils) NewUser(u *User) error {
	if u == nil {
		return errors.New("用户记录无效")
	}
	if u.UserName == "" {
		return errors.New("username can not be empty")
	}
	if u.Password == "" {
		return errors.New("password can not be empty")
	}
	if !s.VerifyUserLoginName(u.UserName, u.Id) {
		return errors.New("username duplicate, please reset")
	}
	if u.Id == 0 {
		u.Id = int(s.JsonDb.GetUserId())
	}
	if u.CreateTime == "" {
		u.CreateTime = time.Now().Format("2006-01-02 15:04:05")
	}
	u.Status = true
	s.JsonDb.Users.Store(u.Id, u)
	s.JsonDb.StoreUsersToJsonFile()
	return nil
}

func (s *DbUtils) UpdateUser(u *User) error {
	if u == nil {
		return errors.New("用户记录无效")
	}
	if u.UserName == "" {
		return errors.New("username can not be empty")
	}
	if u.Password == "" {
		return errors.New("password can not be empty")
	}
	if !s.VerifyUserLoginName(u.UserName, u.Id) {
		return errors.New("username duplicate, please reset")
	}
	s.JsonDb.Users.Store(u.Id, u)
	s.JsonDb.StoreUsersToJsonFile()
	return nil
}

func (s *DbUtils) DelUser(id int) error {
	if id <= 0 {
		return errors.New("用户 ID 无效")
	}
	if user, err := s.GetUser(id); err == nil && user != nil {
		user.Lock()
		user.Status = false
		user.Unlock()
	}
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		c, ok := value.(*Client)
		if !ok || c == nil {
			return true
		}
		c.Lock()
		if c.UserId == id {
			c.UserId = 0
			// Do not leave an orphaned client usable after its owner is deleted.
			// The controller revokes live sessions before this method is called;
			// keeping the client disabled also protects direct DbUtils callers.
			c.Status = false
			c.IsConnect = false
		}
		c.Unlock()
		return true
	})
	s.JsonDb.Users.Delete(id)
	s.JsonDb.StoreUsersToJsonFile()
	s.JsonDb.StoreClientsToJsonFile()
	return nil
}

func (s *DbUtils) GetUser(id int) (*User, error) {
	if v, ok := s.JsonDb.Users.Load(id); ok {
		if user, ok := v.(*User); ok && user != nil {
			return user, nil
		}
		return nil, errors.New("用户记录无效")
	}
	return nil, errors.New("未找到用户")
}

func (s *DbUtils) GetUserByName(username string) (*User, error) {
	var user *User
	s.JsonDb.Users.Range(func(key, value interface{}) bool {
		v, ok := value.(*User)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		userName := v.UserName
		v.RUnlock()
		if userName == username {
			user = v
			return false
		}
		return true
	})
	if user == nil {
		return nil, errors.New("未找到用户")
	}
	return user, nil
}

func (s *DbUtils) VerifyUserLoginName(username string, id int) bool {
	res := true
	s.JsonDb.Users.Range(func(key, value interface{}) bool {
		v, ok := value.(*User)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		userName, userID := v.UserName, v.Id
		v.RUnlock()
		if userName == username && userID != id {
			res = false
			return false
		}
		return true
	})
	return res
}

func (s *DbUtils) MigrateUsersFromClients() error {
	type credential struct {
		userId   int
		password string
	}
	// Older releases persisted the dashboard credentials on each client but
	// did not have users.json. Recover only when that whole file is absent. If
	// it exists, a missing user is treated as an intentional revocation and the
	// bridge continues to fail closed.
	recoverMissingOwners := !common.FileExists(s.JsonDb.UserFilePath)
	byName := make(map[string]credential)
	byID := make(map[int]struct{})
	s.JsonDb.Users.Range(func(key, value interface{}) bool {
		u, ok := value.(*User)
		if !ok || u == nil {
			return true
		}
		u.RLock()
		userName, userID, password := u.UserName, u.Id, u.Password
		u.RUnlock()
		byName[userName] = credential{userId: userID, password: password}
		byID[userID] = struct{}{}
		return true
	})

	changedUsers := false
	changedClients := false
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		c, ok := value.(*Client)
		if !ok || c == nil {
			return true
		}
		c.RLock()
		clientUserID, webUserName, webPassword, remark, clientID := c.UserId, c.WebUserName, c.WebPassword, c.Remark, c.Id
		c.RUnlock()
		if clientUserID != 0 {
			if !recoverMissingOwners || webUserName == "" || webPassword == "" {
				return true
			}
			if _, exists := byID[clientUserID]; exists {
				return true
			}
			name := webUserName
			if cred, exists := byName[name]; exists && (cred.password != webPassword || cred.userId != clientUserID) {
				name = fmt.Sprintf("%s_%d", webUserName, clientUserID)
			}
			u := &User{
				Id:         clientUserID,
				UserName:   name,
				Password:   webPassword,
				Status:     true,
				Remark:     remark,
				CreateTime: time.Now().Format("2006-01-02 15:04:05"),
			}
			s.JsonDb.Users.Store(u.Id, u)
			byID[u.Id] = struct{}{}
			byName[name] = credential{userId: u.Id, password: u.Password}
			if clientUserID > int(atomic.LoadInt32(&s.JsonDb.UserIncreaseId)) {
				atomic.StoreInt32(&s.JsonDb.UserIncreaseId, int32(clientUserID))
			}
			changedUsers = true
			return true
		}
		if webUserName == "" || webPassword == "" {
			return true
		}
		name := webUserName
		cred, ok := byName[name]
		if ok && cred.password != webPassword {
			name = fmt.Sprintf("%s_%d", webUserName, clientID)
			ok = false
		}
		if !ok {
			u := &User{
				Id:         int(s.JsonDb.GetUserId()),
				UserName:   name,
				Password:   webPassword,
				Status:     true,
				Remark:     remark,
				CreateTime: time.Now().Format("2006-01-02 15:04:05"),
			}
			s.JsonDb.Users.Store(u.Id, u)
			cred = credential{userId: u.Id, password: u.Password}
			byName[name] = cred
			changedUsers = true
		}
		c.Lock()
		c.UserId = cred.userId
		c.Unlock()
		changedClients = true
		return true
	})
	if changedUsers {
		s.JsonDb.StoreUsersToJsonFile()
	}
	if changedClients {
		s.JsonDb.StoreClientsToJsonFile()
	}
	return nil
}

func (s *DbUtils) UserClientIds(userId int) map[int]struct{} {
	ids := make(map[int]struct{})
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		c, ok := value.(*Client)
		if !ok || c == nil {
			return true
		}
		c.RLock()
		clientUserID, clientID := c.UserId, c.Id
		c.RUnlock()
		if clientUserID == userId {
			ids[clientID] = struct{}{}
		}
		return true
	})
	return ids
}

func (s *DbUtils) IsClientBelongToUser(clientId, userId int) bool {
	c, err := s.GetClient(clientId)
	if err != nil || c == nil {
		return false
	}
	c.RLock()
	belongs := c.UserId == userId
	c.RUnlock()
	return belongs
}

func (s *DbUtils) IsUserActive(userId int) bool {
	user, err := s.GetUser(userId)
	if err != nil || user == nil {
		return false
	}
	// User status and expiration are changed by the Web controller and the
	// periodic expiry worker. Snapshot both fields under the model lock so an
	// authentication request never observes a torn state.
	user.RLock()
	status, expireTime := user.Status, strings.TrimSpace(user.ExpireTime)
	user.RUnlock()
	if !status {
		return false
	}
	if expireTime == "" {
		return true
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", expireTime, time.Local)
	// A malformed expiry is safer to treat as expired than to let it bypass the
	// bridge authentication check.
	return err == nil && time.Now().Before(t)
}

// IsClientActive applies both the client's own status and the status of its
// owning user. A client with a missing owner is treated as inactive when it
// still carries a non-zero UserId, which fails closed after user deletion or
// malformed persistence data.
func (s *DbUtils) IsClientActive(client *Client) bool {
	if client == nil {
		return false
	}
	client.RLock()
	status, userID := client.Status, client.UserId
	client.RUnlock()
	if !status {
		return false
	}
	return userID == 0 || s.IsUserActive(userID)
}

func (s *DbUtils) GetUserTunnelNum(userId int) int {
	clientIds := s.UserClientIds(userId)
	num := 0
	s.JsonDb.Tasks.Range(func(key, value interface{}) bool {
		t, ok := value.(*Tunnel)
		if !ok || t == nil {
			return true
		}
		t.RLock()
		client := t.Client
		t.RUnlock()
		if client != nil {
			client.RLock()
			clientID := client.Id
			client.RUnlock()
			if _, ok := clientIds[clientID]; ok {
				num++
			}
		}
		return true
	})
	s.JsonDb.Hosts.Range(func(key, value interface{}) bool {
		h, ok := value.(*Host)
		if !ok || h == nil {
			return true
		}
		h.RLock()
		client := h.Client
		h.RUnlock()
		if client != nil {
			client.RLock()
			clientID := client.Id
			client.RUnlock()
			if _, ok := clientIds[clientID]; ok {
				num++
			}
		}
		return true
	})
	return num
}

func (s *DbUtils) IsUserTunnelLimitReached(userId int) bool {
	user, err := s.GetUser(userId)
	if err != nil || user == nil {
		return false
	}
	user.RLock()
	maxTunnelNum := user.MaxTunnelNum
	user.RUnlock()
	return maxTunnelNum > 0 && s.GetUserTunnelNum(userId) >= maxTunnelNum
}

func (s *DbUtils) GetIdByVerifyKey(vKey string, addr string) (id int, err error) {
	var exist bool
	var rejectReason error
	normalizedAddr := common.GetIpByAddr(addr)
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v, ok := value.(*Client)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		verifyKey, status, userID, clientID := v.VerifyKey, v.Status, v.UserId, v.Id
		v.RUnlock()
		if common.Getverifyval(verifyKey) != vKey {
			return true
		}
		if !status {
			rejectReason = errors.New("client disabled")
			return true
		}
		// A user expiry stops and removes current resources, but the NPC can
		// otherwise reconnect immediately and recreate its config-backed tunnels.
		// Check the owning user at the bridge authentication boundary as well.
		if userID != 0 {
			if _, userErr := s.GetUser(userID); userErr != nil {
				rejectReason = errors.New("client owner not found")
				return true
			}
			if !s.IsUserActive(userID) {
				rejectReason = errors.New("client owner inactive or expired")
				return true
			}
		}
		v.Lock()
		v.Addr = normalizedAddr
		v.Unlock()
		id = clientID
		exist = true
		return false
	})
	if exist {
		return
	}
	if rejectReason != nil {
		return 0, rejectReason
	}
	return 0, errors.New("not found")
}

func (s *DbUtils) NewTask(t *Tunnel) (err error) {
	if t == nil || t.Client == nil || t.Target == nil {
		return errors.New("隧道记录无效")
	}
	s.JsonDb.Tasks.Range(func(key, value interface{}) bool {
		v, ok := value.(*Tunnel)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		mode, password := v.Mode, v.Password
		v.RUnlock()
		if (mode == "secret" || mode == "p2p") && password == t.Password && t.Password != "" {
			err = errors.New(fmt.Sprintf("secret mode keys %s must be unique", t.Password))
			return false
		}
		return true
	})
	if err != nil {
		return
	}
	t.Flow = new(Flow)
	s.JsonDb.Tasks.Store(t.Id, t)
	s.JsonDb.StoreTasksToJsonFile()
	return
}

func (s *DbUtils) UpdateTask(t *Tunnel) error {
	if t == nil || t.Client == nil || t.Target == nil {
		return errors.New("隧道记录无效")
	}
	s.JsonDb.Tasks.Store(t.Id, t)
	s.JsonDb.StoreTasksToJsonFile()
	return nil
}

func (s *DbUtils) SaveGlobal(t *Glob) error {
	s.JsonDb.setGlobal(t)
	s.JsonDb.StoreGlobalToJsonFile()
	return nil
}

func (s *DbUtils) DelTask(id int) error {
	s.JsonDb.Tasks.Delete(id)
	s.JsonDb.StoreTasksToJsonFile()
	return nil
}

// md5 password
func (s *DbUtils) GetTaskByMd5Password(p string) (t *Tunnel) {
	s.JsonDb.Tasks.Range(func(key, value interface{}) bool {
		task, ok := value.(*Tunnel)
		if !ok || task == nil {
			return true
		}
		task.RLock()
		password := task.Password
		task.RUnlock()
		if password != "" && crypt.Md5(password) == p {
			t = task
			return false
		}
		return true
	})
	return
}

func (s *DbUtils) GetTask(id int) (t *Tunnel, err error) {
	if v, ok := s.JsonDb.Tasks.Load(id); ok {
		if t, ok = v.(*Tunnel); ok && t != nil {
			return t, nil
		}
		return nil, errors.New("隧道记录无效")
	}
	err = errors.New("not found")
	return
}

func (s *DbUtils) DelHost(id int) error {
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	s.JsonDb.Hosts.Delete(id)
	s.JsonDb.StoreHostToJsonFile()
	return nil
}

func (s *DbUtils) IsHostExist(h *Host) bool {
	if h == nil {
		return false
	}
	h.RLock()
	hostID, hostName, location, scheme := h.Id, h.Host, h.Location, h.Scheme
	h.RUnlock()
	var exist bool
	s.JsonDb.Hosts.Range(func(key, value interface{}) bool {
		v, ok := value.(*Host)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		candidateID, candidateHost, candidateLocation, candidateScheme, candidateClient := v.Id, v.Host, v.Location, v.Scheme, v.Client
		v.RUnlock()
		if candidateClient != nil && candidateID != hostID && candidateHost == hostName && location == candidateLocation && (candidateScheme == "all" || candidateScheme == scheme) {
			exist = true
			return false
		}
		return true
	})
	return exist
}

type hostOwnerKey struct {
	userID   int
	clientID int
}

func hostOwner(client *Client) hostOwnerKey {
	if client == nil {
		return hostOwnerKey{}
	}
	client.RLock()
	owner := hostOwnerKey{userID: client.UserId, clientID: client.Id}
	client.RUnlock()
	return owner
}

func sameHostOwner(a, b *Client) bool {
	aOwner, bOwner := hostOwner(a), hostOwner(b)
	if aOwner.userID > 0 || bOwner.userID > 0 {
		return aOwner.userID > 0 && aOwner.userID == bOwner.userID
	}
	return aOwner.clientID > 0 && aOwner.clientID == bOwner.clientID
}

func normalizeHostLocation(location string) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return "/"
	}
	if !strings.HasPrefix(location, "/") {
		location = "/" + location
	}
	for len(location) > 1 && strings.HasSuffix(location, "/") {
		location = strings.TrimSuffix(location, "/")
	}
	return location
}

func hostLocationsOverlap(a, b string) bool {
	a, b = normalizeHostLocation(a), normalizeHostLocation(b)
	if a == "/" || b == "/" || a == b {
		return true
	}
	if strings.HasPrefix(a, b) {
		return len(a) > len(b) && a[len(b)] == '/'
	}
	if strings.HasPrefix(b, a) {
		return len(b) > len(a) && b[len(a)] == '/'
	}
	return false
}

func validHostRule(rule string) bool {
	rule = normalizeHostName(rule)
	if rule == "" {
		return false
	}
	return !strings.Contains(rule, "*") || (strings.HasPrefix(rule, "*.") && strings.Count(rule, "*") == 1 && strings.TrimPrefix(rule, "*.") != "")
}

func hostRulesOverlap(a, b string) bool {
	a, b = normalizeHostName(a), normalizeHostName(b)
	if !validHostRule(a) || !validHostRule(b) {
		return false
	}
	aWildcard, bWildcard := strings.HasPrefix(a, "*."), strings.HasPrefix(b, "*.")
	if !aWildcard && !bWildcard {
		return a == b
	}
	if aWildcard && !bWildcard {
		return hostRuleMatches(b, a)
	}
	if !aWildcard && bWildcard {
		return hostRuleMatches(a, b)
	}
	aSuffix := strings.TrimPrefix(a, "*.")
	bSuffix := strings.TrimPrefix(b, "*.")
	return aSuffix == bSuffix || strings.HasSuffix(aSuffix, "."+bSuffix) || strings.HasSuffix(bSuffix, "."+aSuffix)
}

// IsHostRouteConflict rejects overlapping host/path rules owned by different
// users (or by different unowned clients). Deterministic route selection alone
// is not an isolation boundary: a more specific rule must not let one tenant
// shadow another tenant's traffic.
func (s *DbUtils) IsHostRouteConflict(h *Host) bool {
	if h == nil || h.Client == nil {
		return false
	}
	h.RLock()
	hostID, hostName, location, scheme, client := h.Id, h.Host, h.Location, h.Scheme, h.Client
	h.RUnlock()
	if !validHostRule(hostName) {
		return false
	}
	owner := hostOwner(client)
	if owner.userID == 0 && owner.clientID == 0 {
		return true
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" {
		scheme = "all"
	}
	exists := false
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		candidate, ok := value.(*Host)
		if !ok || candidate == nil {
			return true
		}
		candidate.RLock()
		candidateID, candidateHost, candidateLocation, candidateScheme, candidateClient := candidate.Id, candidate.Host, candidate.Location, candidate.Scheme, candidate.Client
		candidate.RUnlock()
		if candidateID == hostID || candidateClient == nil || sameHostOwner(client, candidateClient) {
			return true
		}
		schemeA, schemeB := scheme, strings.ToLower(strings.TrimSpace(candidateScheme))
		if schemeB == "" {
			// An empty scheme is treated as the historical default of all.
			schemeB = "all"
		}
		schemesOverlap := schemeA == "all" || schemeB == "all" || schemeA == schemeB
		if schemesOverlap && hostRulesOverlap(hostName, candidateHost) && hostLocationsOverlap(location, candidateLocation) {
			exists = true
			return false
		}
		return true
	})
	return exists
}

func (s *DbUtils) NewHost(t *Host) error {
	if t == nil || t.Client == nil || t.Target == nil {
		return errors.New("主机记录无效")
	}
	if !validHostRule(t.Host) {
		return errors.New("主机域名无效")
	}
	if t.Location == "" {
		t.Location = "/"
	}
	t.Location = normalizeHostLocation(t.Location)
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	if s.IsHostExist(t) {
		return errors.New("host has exist")
	}
	if s.IsHostRouteConflict(t) {
		return errors.New("host route overlaps another tenant")
	}
	t.Flow = new(Flow)
	s.JsonDb.Hosts.Store(t.Id, t)
	s.JsonDb.StoreHostToJsonFile()
	return nil
}

// UpdateHost validates a replacement route and applies it while holding the
// same mutation lock used by NewHost. Runtime workers can continue using the
// existing object, while concurrent edits cannot race the conflict check.
func (s *DbUtils) UpdateHost(t *Host) error {
	if t == nil || t.Client == nil || t.Target == nil {
		return errors.New("主机记录无效")
	}
	if !validHostRule(t.Host) {
		return errors.New("主机域名无效")
	}
	t.Location = normalizeHostLocation(t.Location)
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	storedValue, ok := s.JsonDb.Hosts.Load(t.Id)
	stored, valid := storedValue.(*Host)
	if !ok || !valid || stored == nil {
		return errors.New("未找到主机")
	}
	if s.IsHostExist(t) {
		return errors.New("host has exist")
	}
	if s.IsHostRouteConflict(t) {
		return errors.New("host route overlaps another tenant")
	}
	stored.Lock()
	stored.Host = t.Host
	stored.Client = t.Client
	stored.Target = t.Target
	stored.HeaderChange = t.HeaderChange
	stored.HostChange = t.HostChange
	stored.Remark = t.Remark
	stored.Location = t.Location
	stored.Scheme = t.Scheme
	stored.KeyFilePath = t.KeyFilePath
	stored.CertFilePath = t.CertFilePath
	stored.AutoHttps = t.AutoHttps
	if stored.Flow == nil {
		stored.Flow = new(Flow)
	}
	stored.Unlock()
	s.JsonDb.StoreHostToJsonFile()
	return nil
}

func (s *DbUtils) GetHost(start, length int, id int, search string) ([]*Host, int) {
	return s.GetHostByAllowedClients(start, length, id, search, nil)
}

func (s *DbUtils) GetHostByAllowedClients(start, length int, id int, search string, allowedClientIds map[int]struct{}) ([]*Host, int) {
	list := make([]*Host, 0)
	var cnt int
	keys := GetMapKeys(&s.JsonDb.Hosts, false, "", "")
	for _, key := range keys {
		if value, ok := s.JsonDb.Hosts.Load(key); ok {
			v, valid := value.(*Host)
			if !valid || v == nil {
				continue
			}
			v.RLock()
			client := v.Client
			hostID, hostName, remark := v.Id, v.Host, v.Remark
			v.RUnlock()
			if client == nil {
				continue
			}
			client.RLock()
			clientID, verifyKey := client.Id, client.VerifyKey
			client.RUnlock()
			if allowedClientIds != nil {
				if _, ok := allowedClientIds[clientID]; !ok {
					continue
				}
			}
			if search != "" && !(hostID == common.GetIntNoErrByStr(search) || strings.Contains(hostName, search) || strings.Contains(remark, search) || strings.Contains(verifyKey, search)) {
				continue
			}
			if id == 0 || clientID == id {
				cnt++
				if start--; start < 0 {
					if length--; length >= 0 {
						list = append(list, v)
					}
				}
			}
		}
	}
	return list, cnt
}

func (s *DbUtils) DelClient(id int) error {
	if old, ok := s.JsonDb.Clients.Load(id); ok {
		if client, valid := old.(*Client); valid && client != nil {
			client.RLock()
			clientRate := client.Rate
			client.RUnlock()
			if clientRate != nil {
				clientRate.Stop()
			}
		}
	}
	s.JsonDb.Clients.Delete(id)
	s.JsonDb.StoreClientsToJsonFile()
	return nil
}

func (s *DbUtils) NewClient(c *Client) error {
	if c == nil {
		return errors.New("客户端记录无效")
	}
	var isNotSet bool
reset:
	if c.VerifyKey == "" || isNotSet {
		isNotSet = true
		c.VerifyKey = crypt.GetVkey()
	}
	if !s.VerifyVkey(c.VerifyKey, c.Id) {
		if isNotSet {
			goto reset
		}
		return errors.New("Vkey duplicate, please reset")
	}
	if c.Id == 0 {
		c.Id = int(s.JsonDb.GetClientId())
	}
	if c.Flow == nil {
		c.Flow = new(Flow)
	}
	if c.RateLimit == 0 {
		c.Rate = rate.NewRate((2 << 23) * 1024)
	} else if c.Rate == nil {
		c.Rate = rate.NewRate(int64(c.RateLimit * 1024))
	}
	c.Rate.Start()
	s.JsonDb.Clients.Store(c.Id, c)
	s.JsonDb.StoreClientsToJsonFile()
	return nil
}

func (s *DbUtils) VerifyVkey(vkey string, id int) (res bool) {
	res = true
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v, ok := value.(*Client)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		verifyKey, clientID := v.VerifyKey, v.Id
		v.RUnlock()
		if verifyKey == vkey && clientID != id {
			res = false
			return false
		}
		return true
	})
	return res
}

func (s *DbUtils) VerifyUserName(username string, id int) (res bool) {
	res = true
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v, ok := value.(*Client)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		webUserName, clientID := v.WebUserName, v.Id
		v.RUnlock()
		if webUserName == username && clientID != id {
			res = false
			return false
		}
		return true
	})
	return res
}

func (s *DbUtils) UpdateClient(t *Client) error {
	if t == nil {
		return errors.New("客户端记录无效")
	}
	// 先 Stop 旧的 Rate，防止内存泄漏
	if old, ok := s.JsonDb.Clients.Load(t.Id); ok {
		if oldClient, valid := old.(*Client); valid && oldClient != nil {
			oldClient.RLock()
			oldRate := oldClient.Rate
			oldClient.RUnlock()
			if oldRate != nil {
				oldRate.Stop()
			}
		}
	}
	s.JsonDb.Clients.Store(t.Id, t)
	if t.RateLimit == 0 {
		t.Rate = rate.NewRate(int64((2 << 23) * 1024))
		t.Rate.Start()
	}
	return nil
}

func (s *DbUtils) IsPubClient(id int) bool {
	client, err := s.GetClient(id)
	if err != nil || client == nil {
		return false
	}
	client.RLock()
	noDisplay := client.NoDisplay
	client.RUnlock()
	return noDisplay
}

func (s *DbUtils) GetClient(id int) (c *Client, err error) {
	if v, ok := s.JsonDb.Clients.Load(id); ok {
		if c, ok = v.(*Client); ok && c != nil {
			return c, nil
		}
		return nil, errors.New("客户端记录无效")
	}
	err = errors.New("未找到客户端")
	return
}

func (s *DbUtils) GetGlobal() (c *Glob) {
	return s.JsonDb.getGlobal()
}

func (s *DbUtils) GetClientIdByVkey(vkey string) (id int, err error) {
	var exist bool
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v, ok := value.(*Client)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		verifyKey, clientID := v.VerifyKey, v.Id
		v.RUnlock()
		if crypt.Md5(verifyKey) == vkey {
			exist = true
			id = clientID
			return false
		}
		return true
	})
	if exist {
		return
	}
	err = errors.New("未找到客户端")
	return
}

func (s *DbUtils) GetClientByVkey(vkey string) (c *Client, err error) {
	var exist bool
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v, ok := value.(*Client)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		verifyKey := v.VerifyKey
		v.RUnlock()
		if fmt.Sprintf("%x", md5.Sum([]byte(verifyKey))) == vkey {
			exist = true
			c = v
			return false
		}
		return true
	})
	if exist {
		return
	}
	err = errors.New("未找到客户端")
	return
}

func (s *DbUtils) GetHostById(id int) (h *Host, err error) {
	if v, ok := s.JsonDb.Hosts.Load(id); ok {
		if h, ok = v.(*Host); ok && h != nil {
			return h, nil
		}
		return nil, errors.New("主机记录无效")
	}
	err = errors.New("The host could not be parsed")
	return
}

func normalizeHostName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.TrimSuffix(value, ".")
}

func hostRuleMatches(host, rule string) bool {
	host = normalizeHostName(host)
	rule = normalizeHostName(rule)
	if host == "" || rule == "" {
		return false
	}
	if !strings.Contains(rule, "*") {
		return host == rule
	}
	// Only a leading wildcard label is valid. Requiring a dot before the
	// suffix prevents both suffix lookalikes and the bare apex from matching.
	if !strings.HasPrefix(rule, "*.") || strings.Count(rule, "*") != 1 {
		return false
	}
	suffix := strings.TrimPrefix(rule, "*.")
	return suffix != "" && len(host) > len(suffix)+1 && strings.HasSuffix(host, "."+suffix)
}

func hostRuleSpecificity(rule string) (exact bool, suffixLength int) {
	rule = normalizeHostName(rule)
	if rule == "" {
		return false, 0
	}
	if strings.HasPrefix(rule, "*.") && strings.Count(rule, "*") == 1 {
		return false, len(strings.TrimPrefix(rule, "*."))
	}
	if !strings.Contains(rule, "*") {
		return true, len(rule)
	}
	return false, 0
}

func hostPathMatches(requestURI, location string) bool {
	if location == "" || location == "/" {
		return strings.HasPrefix(requestURI, "/")
	}
	if !strings.HasPrefix(requestURI, location) {
		return false
	}
	if len(requestURI) == len(location) {
		return true
	}
	next := requestURI[len(location)]
	return next == '/' || next == '?'
}

// get key by host from x
func (s *DbUtils) GetInfoByHost(host string, r *http.Request) (h *Host, err error) {
	if r == nil || r.URL == nil {
		return nil, errors.New("invalid host request")
	}
	requestURI := r.RequestURI
	if requestURI == "" {
		requestURI = r.URL.RequestURI()
		if requestURI == "" {
			requestURI = "/"
		}
	}
	type hostMatch struct {
		host             *Host
		location, rule   string
		exact            bool
		suffixLength, id int
	}
	var hosts []hostMatch
	longestLocation := ""
	bestExact, bestSuffixLength, bestID := false, -1, int(^uint(0)>>1)
	//Handling Ported Access
	host = common.GetIpByAddr(host)
	s.JsonDb.Hosts.Range(func(key, value interface{}) bool {
		v, ok := value.(*Host)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		isClose, scheme, hostName := v.IsClose, v.Scheme, v.Host
		v.RUnlock()
		if isClose {
			return true
		}
		//Remove http(s) http(s)://a.proxy.com
		//*.proxy.com *.a.proxy.com  Do some pan-parsing
		if scheme != "all" && scheme != r.URL.Scheme {
			return true
		}
		if hostRuleMatches(host, hostName) {
			v.RLock()
			location, hostID := v.Location, v.Id
			v.RUnlock()
			if location == "" {
				location = "/"
			}
			if !strings.HasPrefix(location, "/") {
				location = "/" + location
			}
			exact, suffixLength := hostRuleSpecificity(hostName)
			hosts = append(hosts, hostMatch{host: v, location: location, rule: hostName, exact: exact, suffixLength: suffixLength, id: hostID})
		}
		return true
	})

	for _, match := range hosts {
		v, location := match.host, match.location
		betterHost := match.exact && !bestExact || match.exact == bestExact && match.suffixLength > bestSuffixLength || match.exact == bestExact && match.suffixLength == bestSuffixLength && len(location) > len(longestLocation) || match.exact == bestExact && match.suffixLength == bestSuffixLength && len(location) == len(longestLocation) && match.id < bestID
		// "*" means SNI-based HTTPS lookup where actual URI is unknown, skip location filter
		if requestURI == "*" {
			if h == nil || betterHost {
				h = v
				longestLocation = location
				bestExact, bestSuffixLength, bestID = match.exact, match.suffixLength, match.id
			}
			continue
		}
		if hostPathMatches(requestURI, location) {
			if h == nil || betterHost {
				h = v
				longestLocation = location
				bestExact, bestSuffixLength, bestID = match.exact, match.suffixLength, match.id
			}
		}
	}
	if h != nil {
		return
	}
	err = errors.New("The host could not be parsed")
	return
}
