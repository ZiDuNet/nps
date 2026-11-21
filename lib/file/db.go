package file

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/lib/store"
)

type DbUtils struct {
	JsonDb *JsonDb
}

// OwnerFilter scopes list queries to a dashboard user. A nil UserID means
// that no owner filter was requested; a non-nil pointer to zero deliberately
// selects unassigned resources. Keeping the zero value meaningful lets the
// admin UI expose an explicit "未分配" option without adding a second query
// parameter, while preserving the historical nil/no-filter behavior.
type OwnerFilter struct {
	UserID *int
}

var (
	once             sync.Once
	hostMutationMu   sync.Mutex
	clientMutationMu sync.Mutex
	taskMutationMu   sync.Mutex
	globalMutationMu sync.Mutex
)

// init csv from file
func GetDb() *DbUtils {
	once.Do(func() {
		dbMu.RLock()
		if Db != nil {
			dbMu.RUnlock()
			return
		}
		dbMu.RUnlock()
		jsonDb := NewJsonDb(common.GetRunPath())
		dbMu.Lock()
		defer dbMu.Unlock()
		if Db != nil {
			return
		}
		jsonDb.LoadClientFromJsonFile()
		jsonDb.LoadUserFromJsonFile()
		jsonDb.LoadTaskFromJsonFile()
		jsonDb.LoadGlobalFromJsonFile()
		jsonDb.LoadHostFromJsonFile()
		if jsonDb.ReconcilePlatformHostCertificates() {
			jsonDb.StoreHostToJsonFile()
		}
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
	return s.GetClientListFiltered(start, length, search, sort, order, clientId, nil, nil)
}

// GetClientListForAllowedIds returns the visible clients owned by the supplied
// IDs. Filtering is applied before counting and pagination so a user's page
// boundaries are independent of other users' clients.
func (s *DbUtils) GetClientListForAllowedIds(start, length int, search, sort, order string, clientId int, allowedClientIds map[int]struct{}) ([]*Client, int) {
	return s.GetClientListFiltered(start, length, search, sort, order, clientId, nil, allowedClientIds)
}

// GetClientListFiltered returns clients matching the optional owner filter.
// Ownership is applied before search, counting, and pagination so totals and
// page boundaries remain stable for administrator filter combinations.
func (s *DbUtils) GetClientListFiltered(start, length int, search, sort, order string, clientId int, owner *OwnerFilter, allowedClientIds map[int]struct{}) ([]*Client, int) {
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
			noDisplay, candidateID, candidateUserID := v.NoDisplay, v.Id, v.UserId
			verifyKey, remark := v.VerifyKey, v.Remark
			v.RUnlock()
			if noDisplay {
				continue
			}
			if allowedClientIds != nil {
				if _, allowed := allowedClientIds[candidateID]; !allowed {
					continue
				}
			}
			if clientId != 0 && clientId != candidateID {
				continue
			}
			if owner != nil && owner.UserID != nil && candidateUserID != *owner.UserID {
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
	return s.GetUserListSorted(start, length, search, "", "")
}

// GetUserListSorted keeps the historical user-list contract while allowing
// the management table to sort by live shared bandwidth and connections.
func (s *DbUtils) GetUserListSorted(start, length int, search, sortField, order string) ([]*User, int) {
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
	SortUsers(all, sortField, order)
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
	email, err := NormalizeUserEmail(u.Email)
	if err != nil {
		return err
	}
	u.Email = email
	if err := validateUserQuotas(u); err != nil {
		return err
	}
	expireTime, err := NormalizeExpiryTime(u.ExpireTime)
	if err != nil {
		return err
	}
	u.ExpireTime = expireTime

	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	if !s.VerifyUserLoginName(u.UserName, u.Id) {
		return errors.New("username duplicate, please reset")
	}
	if !s.VerifyUserEmail(u.Email, u.Id) {
		return errors.New("邮箱已被其他用户使用")
	}
	if u.Id == 0 {
		u.Id = int(s.JsonDb.GetUserId())
	}
	if u.Id > int(^uint32(0)>>1) {
		return errors.New("用户 ID 超出支持范围")
	}
	if _, exists := s.JsonDb.Users.Load(u.Id); exists {
		return fmt.Errorf("用户 ID %d 已存在", u.Id)
	}
	for current := atomic.LoadInt32(&s.JsonDb.UserIncreaseId); u.Id > int(current); current = atomic.LoadInt32(&s.JsonDb.UserIncreaseId) {
		if atomic.CompareAndSwapInt32(&s.JsonDb.UserIncreaseId, current, int32(u.Id)) {
			break
		}
	}
	if u.CreateTime == "" {
		u.CreateTime = time.Now().Format("2006-01-02 15:04:05")
	}
	u.Status = true
	s.JsonDb.Users.Store(u.Id, u)
	if err := s.JsonDb.StoreUsersToJsonFileErr(); err != nil {
		s.JsonDb.Users.Delete(u.Id)
		return fmt.Errorf("保存用户失败: %w", err)
	}
	SetUserRuntimeLimits(u.Id, u.MaxConn, u.BandwidthLimit)
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
	email, err := NormalizeUserEmail(u.Email)
	if err != nil {
		return err
	}
	u.Email = email
	if err := validateUserQuotas(u); err != nil {
		return err
	}
	expireTime, err := NormalizeExpiryTime(u.ExpireTime)
	if err != nil {
		return err
	}
	u.ExpireTime = expireTime

	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	if !s.VerifyUserLoginName(u.UserName, u.Id) {
		return errors.New("username duplicate, please reset")
	}
	if !s.VerifyUserEmail(u.Email, u.Id) {
		return errors.New("邮箱已被其他用户使用")
	}
	existing, _ := s.GetUser(u.Id)
	if existing != nil {
		existing.RLock()
		existingMaxClientNum, existingMaxTunnelNum, existingMaxHostNum := existing.MaxClientNum, existing.MaxTunnelNum, existing.MaxHostNum
		existingExpireTime := existing.ExpireTime
		existing.RUnlock()
		if reducedBelowUsage(u.MaxClientNum, existingMaxClientNum, s.getUserClientNumLocked(u.Id, 0)) {
			return errors.New("最大客户端数不能低于已分配客户端数量")
		}
		if reducedBelowUsage(u.MaxTunnelNum, existingMaxTunnelNum, s.GetUserTunnelNum(u.Id)) {
			return errors.New("最大隧道数不能低于已创建隧道数量")
		}
		if reducedBelowUsage(u.MaxHostNum, existingMaxHostNum, s.GetUserHostNum(u.Id)) {
			return errors.New("最大域名规则数不能低于已创建域名规则数量")
		}
		if u.ExpireTime != existingExpireTime {
			if err := s.validateUserExpiryForClientsLocked(u.Id, u.ExpireTime); err != nil {
				return err
			}
		}
	}
	s.JsonDb.Users.Store(u.Id, u)
	if err := s.JsonDb.StoreUsersToJsonFileErr(); err != nil {
		if existing != nil {
			s.JsonDb.Users.Store(u.Id, existing)
		} else {
			s.JsonDb.Users.Delete(u.Id)
		}
		return fmt.Errorf("保存用户失败: %w", err)
	}
	SetUserRuntimeLimits(u.Id, u.MaxConn, u.BandwidthLimit)
	return nil
}

func sameIntQuota(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// reducedBelowUsage reports a new finite cap that is lower than the resources
// already allocated. Unlimited (nil) and "not included" (zero) are intentional
// block states, so they are not treated as accidental shrink operations.
func reducedBelowUsage(next, previous *int, used int) bool {
	if next == nil || *next <= 0 || *next >= used {
		return false
	}
	return !sameIntQuota(next, previous)
}

func validateUserQuotas(u *User) error {
	if u == nil {
		return errors.New("用户记录无效")
	}
	if u.MaxClientNum != nil && *u.MaxClientNum < 0 {
		return errors.New("用户配额、连接数和带宽限制不能小于 0")
	}
	if u.MaxTunnelNum != nil && *u.MaxTunnelNum < 0 {
		return errors.New("用户配额、连接数和带宽限制不能小于 0")
	}
	if u.MaxHostNum != nil && *u.MaxHostNum < 0 {
		return errors.New("用户配额、连接数和带宽限制不能小于 0")
	}
	if u.MaxConn != nil && *u.MaxConn < 0 {
		return errors.New("用户配额、连接数和带宽限制不能小于 0")
	}
	if u.BandwidthLimit != nil && *u.BandwidthLimit < 0 {
		return errors.New("用户配额、连接数和带宽限制不能小于 0")
	}
	return nil
}

func (s *DbUtils) DelUser(id int) error {
	if id <= 0 {
		return errors.New("用户 ID 无效")
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	user, userExists := s.JsonDb.Users.Load(id)
	if userExists {
		if typed, ok := user.(*User); !ok || typed == nil {
			return errors.New("用户记录无效")
		}
	}
	previousUserStatus := false
	if typed, ok := user.(*User); ok && typed != nil {
		typed.Lock()
		previousUserStatus = typed.Status
		typed.Status = false
		typed.Unlock()
	}
	type clientDeletionState struct {
		userID    int
		status    bool
		isConnect bool
	}
	previousClients := make(map[int]clientDeletionState)
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		clientID, ok := key.(int)
		c, valid := value.(*Client)
		if !ok || !valid || c == nil {
			return true
		}
		c.Lock()
		if c.UserId == id {
			previousClients[clientID] = clientDeletionState{
				userID:    c.UserId,
				status:    c.Status,
				isConnect: c.IsConnect,
			}
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
	if err := s.JsonDb.persistKinds(store.KindUser, store.KindClient); err != nil {
		if userExists {
			s.JsonDb.Users.Store(id, user)
			if typed, ok := user.(*User); ok && typed != nil {
				typed.Lock()
				typed.Status = previousUserStatus
				typed.Unlock()
			}
		}
		for clientID, previous := range previousClients {
			if current, ok := s.JsonDb.Clients.Load(clientID); ok {
				if client, valid := current.(*Client); valid && client != nil {
					client.Lock()
					client.UserId = previous.userID
					client.Status = previous.status
					client.IsConnect = previous.isConnect
					client.Unlock()
				}
			}
		}
		return fmt.Errorf("保存用户删除结果失败: %w", err)
	}
	CleanupUserRuntime(id)
	return nil
}

// ClientCascadeCommit receives committed records for runtime cleanup. It runs
// while the data mutation locks are still held, so implementations must only
// capture the records and defer lifecycle-locking cleanup until after the
// delete method returns. Persistent IDs are monotonic and never recycled.
type ClientCascadeCommit func(clients []*Client, tunnels []*Tunnel, hosts []*Host)

// DelUserCascade removes the account and every owned client resource in one
// persistence operation.
func (s *DbUtils) DelUserCascade(id int) error {
	return s.DelUserCascadeWithHook(id, nil)
}

// DelUserCascadeWithHook invokes onCommitted only after the SQLite transaction
// succeeds. A persistence failure therefore leaves live listeners untouched.
func (s *DbUtils) DelUserCascadeWithHook(id int, onCommitted ClientCascadeCommit) error {
	if id <= 0 {
		return errors.New("用户 ID 无效")
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()

	userValue, ok := s.JsonDb.Users.Load(id)
	if !ok {
		return errors.New("未找到用户")
	}
	user, valid := userValue.(*User)
	if !valid || user == nil {
		return errors.New("用户记录无效")
	}

	ownedClientIDs := make(map[int]struct{})
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		client, valid := value.(*Client)
		if !valid || client == nil {
			return true
		}
		client.RLock()
		userID, clientID := client.UserId, client.Id
		client.RUnlock()
		if userID == id {
			if clientID <= 0 {
				if intID, ok := key.(int); ok {
					clientID = intID
				}
			}
			ownedClientIDs[clientID] = struct{}{}
		}
		return true
	})

	type removedEntity struct {
		kind  string
		key   int
		value interface{}
	}
	removed := make([]removedEntity, 0)
	removedClients := make([]*Client, 0, len(ownedClientIDs))
	removedTunnels := make([]*Tunnel, 0)
	removedHosts := make([]*Host, 0)
	removeOwned := func(kind string, values *sync.Map) {
		values.Range(func(key, value interface{}) bool {
			entityID, ok := key.(int)
			if !ok {
				return true
			}
			var client *Client
			switch obj := value.(type) {
			case *Client:
				client = obj
			case *Tunnel:
				if obj != nil {
					client = obj.Client
				}
			case *Host:
				if obj != nil {
					client = obj.Client
				}
			}
			if client == nil {
				return true
			}
			client.RLock()
			clientID := client.Id
			client.RUnlock()
			if _, owned := ownedClientIDs[clientID]; !owned {
				return true
			}
			if previous, loaded := values.LoadAndDelete(entityID); loaded {
				removed = append(removed, removedEntity{kind: kind, key: entityID, value: previous})
				switch entity := previous.(type) {
				case *Client:
					removedClients = append(removedClients, entity)
				case *Tunnel:
					removedTunnels = append(removedTunnels, entity)
				case *Host:
					removedHosts = append(removedHosts, entity)
				}
			}
			return true
		})
	}
	removeOwned(store.KindTunnel, &s.JsonDb.Tasks)
	removeOwned(store.KindHost, &s.JsonDb.Hosts)
	removeOwned(store.KindClient, &s.JsonDb.Clients)
	s.JsonDb.Users.Delete(id)

	if err := s.JsonDb.persistKinds(store.KindUser, store.KindClient, store.KindTunnel, store.KindHost); err != nil {
		s.JsonDb.Users.Store(id, userValue)
		for _, entity := range removed {
			switch entity.kind {
			case store.KindClient:
				s.JsonDb.Clients.Store(entity.key, entity.value)
			case store.KindTunnel:
				s.JsonDb.Tasks.Store(entity.key, entity.value)
			case store.KindHost:
				s.JsonDb.Hosts.Store(entity.key, entity.value)
			}
		}
		return fmt.Errorf("保存用户级联删除结果失败: %w", err)
	}

	for _, client := range removedClients {
		if client != nil {
			s.JsonDb.markDeletedClient(client)
		}
	}
	if onCommitted != nil {
		onCommitted(removedClients, removedTunnels, removedHosts)
	}
	for _, entity := range removed {
		if entity.kind != store.KindClient {
			continue
		}
		if client, valid := entity.value.(*Client); valid && client != nil {
			client.RLock()
			clientRate := client.Rate
			client.RUnlock()
			if clientRate != nil {
				clientRate.Stop()
			}
		}
	}
	CleanupUserRuntime(id)
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

// GetUserByEmail refuses ambiguous legacy records rather than choosing a
// random account from sync.Map. Usernames retain their existing exact match.
func (s *DbUtils) GetUserByEmail(email string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if normalized, err := NormalizeUserEmail(email); err != nil || email == "" || normalized != email {
		return nil, errors.New("邮箱地址格式无效")
	}
	var found *User
	ambiguous := false
	s.JsonDb.Users.Range(func(_, value interface{}) bool {
		user, ok := value.(*User)
		if !ok || user == nil {
			return true
		}
		user.RLock()
		match := strings.EqualFold(strings.TrimSpace(user.Email), email)
		user.RUnlock()
		if match {
			if found != nil {
				ambiguous = true
				return false
			}
			found = user
		}
		return true
	})
	if found == nil || ambiguous {
		return nil, errors.New("未找到唯一的邮箱账号")
	}
	return found, nil
}

func (s *DbUtils) GetUserByLoginName(name string) (*User, error) {
	name = strings.TrimSpace(name)
	if user, err := s.GetUserByName(name); err == nil {
		return user, nil
	}
	return s.GetUserByEmail(name)
}

// ResetUserPassword is a compare-and-swap: a code issued before an email or
// password change cannot reset the account afterward. Other fields and the
// existing User pointer are intentionally preserved.
func (s *DbUtils) ResetUserPassword(id int, email, previous, password string) error {
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	user, err := s.GetUser(id)
	if err != nil || password == "" || email == "" {
		return errors.New("账号状态已变化，请重新找回密码")
	}
	user.Lock()
	expired := false
	if expiry := strings.TrimSpace(user.ExpireTime); expiry != "" {
		until, valid := ParseExpiryTime(expiry)
		expired = !valid || !time.Now().Before(until)
	}
	if !user.Status || expired || !strings.EqualFold(user.Email, email) ||
		subtle.ConstantTimeCompare([]byte(user.Password), []byte(previous)) != 1 {
		user.Unlock()
		return errors.New("账号状态已变化，请重新找回密码")
	}
	user.Password = password
	user.Unlock()
	if err = s.JsonDb.StoreUsersToJsonFileErr(); err != nil {
		user.Lock()
		user.Password = previous
		user.Unlock()
		return fmt.Errorf("保存密码失败: %w", err)
	}
	return nil
}

// ValidateClientOwner keeps the client-to-user relationship referentially
// valid. UserId zero intentionally means that a client is not assigned to a
// dashboard user; every other value must point at a persisted user record.
// Keeping this check in DbUtils prevents Web, API and internal callers from
// creating clients that no user can ever see.
func (s *DbUtils) ValidateClientOwner(userID int) error {
	if userID < 0 {
		return errors.New("所属用户无效")
	}
	if userID == 0 {
		return nil
	}
	if _, err := s.GetUser(userID); err != nil {
		return errors.New("所属用户不存在")
	}
	return nil
}

// requireStoredClientReferenceLocked rejects a stale *Client captured before a
// concurrent delete. Pointer equality is intentional: an ID may be reused by a
// later application/restart, and a new client with the same ID must not accept
// tunnels created for the removed record. In-process fixtures may still pass a
// never-registered pointer, matching the historical helper behavior.
func (s *DbUtils) requireStoredClientReferenceLocked(client *Client) error {
	if client == nil {
		return errors.New("所属客户端不存在")
	}
	if _, deleted := s.JsonDb.deletedClientRefs.Load(client); deleted {
		return errors.New("所属客户端已被删除或替换，请刷新后重试")
	}
	client.RLock()
	clientID := client.Id
	client.RUnlock()
	if current, ok := s.JsonDb.Clients.Load(clientID); ok && current != client {
		return errors.New("所属客户端已被删除或替换，请刷新后重试")
	}
	return nil
}

const canonicalExpireTimeLayout = "2006-01-02 15:04:05"

var expiryTimeFormats = []string{
	canonicalExpireTimeLayout,
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
	"2006/01/02",
	time.RFC3339,
}

// ParseExpiryTime accepts the historical expiry formats used by NPS. Empty
// values are deliberately not dates: callers use an empty value to mean that
// no independent expiry is set.
func ParseExpiryTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range expiryTimeFormats {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// NormalizeExpiryTime turns a user supplied expiry into the persisted NPS
// format. An empty input is valid and represents no independent expiry.
func NormalizeExpiryTime(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	parsed, ok := ParseExpiryTime(value)
	if !ok {
		return "", errors.New("到期时间格式无效")
	}
	return parsed.Format(canonicalExpireTimeLayout), nil
}

// GetUserClientNum returns the number of clients currently assigned to a
// user. The count intentionally includes disabled and expired clients: they
// are still allocated credentials and must be deleted or reassigned before a
// new device can consume the slot.
func (s *DbUtils) GetUserClientNum(userID int) int {
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	return s.getUserClientNumLocked(userID, 0)
}

// GetUserClientNumExcluding is useful when editing an existing client. It
// counts the target owner's other clients, so retaining the same assignment at
// the quota limit remains valid.
func (s *DbUtils) GetUserClientNumExcluding(userID, excludedClientID int) int {
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	return s.getUserClientNumLocked(userID, excludedClientID)
}

func (s *DbUtils) getUserClientNumLocked(userID, excludedClientID int) int {
	if userID <= 0 {
		return 0
	}
	count := 0
	s.JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		clientID, ownerID := client.Id, client.UserId
		client.RUnlock()
		if ownerID == userID && (excludedClientID <= 0 || clientID != excludedClientID) {
			count++
		}
		return true
	})
	return count
}

// ValidateClientOwnerQuota validates a prospective client assignment. Passing
// the current client ID as excludedClientID makes the helper safe for an
// ownership-preserving edit or a reassignment operation.
func (s *DbUtils) ValidateClientOwnerQuota(userID, excludedClientID int) error {
	if userID == 0 {
		// Unassigned clients are administrator-owned resources.
		return nil
	}
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	return s.validateClientOwnerQuotaLocked(userID, excludedClientID)
}

func (s *DbUtils) validateClientOwnerQuotaLocked(userID, excludedClientID int) error {
	if err := s.ValidateClientOwner(userID); err != nil {
		return err
	}
	if userID == 0 {
		return nil
	}
	user, err := s.GetUser(userID)
	if err != nil || user == nil {
		return errors.New("所属用户不存在")
	}
	user.RLock()
	maxClientNum := user.MaxClientNum
	user.RUnlock()
	if maxClientNum != nil && *maxClientNum < 0 {
		return errors.New("所属用户的最大客户端数无效")
	}
	if IntQuotaReached(s.getUserClientNumLocked(userID, excludedClientID), maxClientNum) {
		return errors.New("客户端数量已达到所属用户的配额上限")
	}
	return nil
}

// ValidateClientExpiryForOwner verifies that a client's independent expiry is
// valid and does not extend past its owner's expiry. An empty client expiry is
// valid and means that the client follows the user expiry.
func (s *DbUtils) ValidateClientExpiryForOwner(userID int, clientExpireTime string) error {
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	return s.validateClientExpiryForOwnerLocked(userID, clientExpireTime)
}

func (s *DbUtils) validateClientExpiryForOwnerLocked(userID int, clientExpireTime string) error {
	clientExpiry, err := NormalizeExpiryTime(clientExpireTime)
	if err != nil {
		return err
	}
	if userID == 0 {
		return nil
	}
	if err := s.ValidateClientOwner(userID); err != nil {
		return err
	}
	user, err := s.GetUser(userID)
	if err != nil || user == nil {
		return errors.New("所属用户不存在")
	}
	user.RLock()
	userExpireTime := user.ExpireTime
	user.RUnlock()
	return validateClientExpiryBounds(clientExpiry, userExpireTime)
}

func validateClientExpiryBounds(clientExpireTime, userExpireTime string) error {
	clientExpiry, err := NormalizeExpiryTime(clientExpireTime)
	if err != nil {
		return err
	}
	userExpiry, err := NormalizeExpiryTime(userExpireTime)
	if err != nil {
		return errors.New("所属用户到期时间无效")
	}
	if clientExpiry == "" || userExpiry == "" {
		return nil
	}
	clientAt, _ := ParseExpiryTime(clientExpiry)
	userAt, _ := ParseExpiryTime(userExpiry)
	if clientAt.After(userAt) {
		return errors.New("客户端到期时间不能晚于所属用户到期时间")
	}
	return nil
}

// ValidateClientAssignment combines referential integrity, client quota, and
// expiry-bound checks. NewClient and UpdateClient use the locked counterpart
// so concurrent creates and owner transfers cannot oversubscribe a user.
func (s *DbUtils) ValidateClientAssignment(client *Client, excludedClientID int) error {
	return s.validateClientAssignment(client, excludedClientID, false)
}

// ValidateClientAssignmentAsAdmin applies the same owner/expiry integrity
// checks without charging the newly selected owner's customer quota.
// Administrator-managed records are outside the plan entitlement boundary,
// but they must still point at an existing owner and obey its expiry.
func (s *DbUtils) ValidateClientAssignmentAsAdmin(client *Client, excludedClientID int) error {
	return s.validateClientAssignment(client, excludedClientID, true)
}

func (s *DbUtils) validateClientAssignment(client *Client, excludedClientID int, bypassUserQuota bool) error {
	if client == nil {
		return errors.New("客户端记录无效")
	}
	client.RLock()
	userID, expireTime := client.UserId, client.ExpireTime
	client.RUnlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	if bypassUserQuota {
		if err := s.ValidateClientOwner(userID); err != nil {
			return err
		}
		return s.validateClientExpiryForOwnerLocked(userID, expireTime)
	}
	return s.validateClientAssignmentLocked(userID, expireTime, excludedClientID)
}

func (s *DbUtils) validateClientAssignmentLocked(userID int, clientExpireTime string, excludedClientID int) error {
	if userID == 0 {
		return nil
	}
	if err := s.validateClientOwnerQuotaLocked(userID, excludedClientID); err != nil {
		return err
	}
	if err := s.validateClientExpiryForOwnerLocked(userID, clientExpireTime); err != nil {
		return err
	}
	return s.validateClientTransferQuotaLocked(userID, excludedClientID)
}

// validateClientTransferQuotaLocked prevents an owner transfer from silently
// pushing a client's existing tunnels or host rules past the target user's
// plan. The quota is charged per resource, not per client, because the resource
// stays allocated before and after the transfer.
func (s *DbUtils) validateClientTransferQuotaLocked(userID, clientID int) error {
	if userID <= 0 || clientID <= 0 {
		return nil
	}
	source, err := s.GetClient(clientID)
	if err != nil || source == nil {
		return nil
	}
	source.RLock()
	sourceOwner := source.UserId
	source.RUnlock()
	if sourceOwner == userID {
		return nil
	}
	user, err := s.GetUser(userID)
	if err != nil || user == nil {
		return errors.New("所属用户不存在")
	}
	user.RLock()
	maxTunnelNum, maxHostNum := user.MaxTunnelNum, user.MaxHostNum
	user.RUnlock()
	tunnels, hosts := 0, 0
	s.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		client, noStore := tunnel.Client, tunnel.NoStore
		tunnel.RUnlock()
		if noStore || client == nil {
			return true
		}
		client.RLock()
		owner := client.Id
		client.RUnlock()
		if owner == clientID {
			tunnels++
		}
		return true
	})
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		client, noStore := host.Client, host.NoStore
		host.RUnlock()
		if noStore || client == nil {
			return true
		}
		client.RLock()
		owner := client.Id
		client.RUnlock()
		if owner == clientID {
			hosts++
		}
		return true
	})
	if IntQuotaReached(s.GetUserTunnelNum(userID)+tunnels, maxTunnelNum) {
		return errors.New("转移后的隧道数会超过目标用户的套餐上限")
	}
	if IntQuotaReached(s.GetUserHostNum(userID)+hosts, maxHostNum) {
		return errors.New("转移后的域名规则数会超过目标用户的套餐上限")
	}
	return nil
}

// EffectiveClientExpireTime returns the date that actually governs a client.
// A blank result means that neither the client nor its owner expires. The
// helper takes the earlier date defensively so legacy records that predate the
// owner-bound validation remain safe at runtime.
func (s *DbUtils) EffectiveClientExpireTime(client *Client) (string, error) {
	if client == nil {
		return "", errors.New("客户端记录无效")
	}
	client.RLock()
	userID, clientExpireTime := client.UserId, client.ExpireTime
	client.RUnlock()
	clientExpiry, err := NormalizeExpiryTime(clientExpireTime)
	if err != nil {
		return "", err
	}
	if userID == 0 {
		return clientExpiry, nil
	}
	user, err := s.GetUser(userID)
	if err != nil || user == nil {
		return "", errors.New("所属用户不存在")
	}
	user.RLock()
	userExpireTime := user.ExpireTime
	user.RUnlock()
	userExpiry, err := NormalizeExpiryTime(userExpireTime)
	if err != nil {
		return "", errors.New("所属用户到期时间无效")
	}
	if clientExpiry == "" {
		return userExpiry, nil
	}
	if userExpiry == "" {
		return clientExpiry, nil
	}
	clientAt, _ := ParseExpiryTime(clientExpiry)
	userAt, _ := ParseExpiryTime(userExpiry)
	if userAt.Before(clientAt) {
		return userExpiry, nil
	}
	return clientExpiry, nil
}

// validateUserExpiryForClientsLocked prevents an administrator from saving a
// user expiry that would leave existing independently-expiring clients with a
// later, misleading date. Empty client expiry values are intentionally valid:
// those clients follow the user's new expiry automatically.
func (s *DbUtils) validateUserExpiryForClientsLocked(userID int, userExpireTime string) error {
	var validationErr error
	s.JsonDb.Clients.Range(func(_, value interface{}) bool {
		if validationErr != nil {
			return false
		}
		client, ok := value.(*Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		clientID, ownerID, clientExpireTime := client.Id, client.UserId, client.ExpireTime
		client.RUnlock()
		if ownerID != userID {
			return true
		}
		if err := validateClientExpiryBounds(clientExpireTime, userExpireTime); err != nil {
			if err.Error() == "客户端到期时间不能晚于所属用户到期时间" {
				validationErr = fmt.Errorf("用户到期时间不能早于客户端 %d 的到期时间", clientID)
			} else {
				validationErr = err
			}
			return false
		}
		return true
	})
	return validationErr
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

// NormalizeUserEmail validates an optional account email and stores it in a
// stable lower-case form. An empty address remains valid for legacy and
// username/password-only accounts.
func NormalizeUserEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address == nil || address.Address == "" {
		return "", errors.New("邮箱地址格式无效")
	}
	return strings.ToLower(strings.TrimSpace(address.Address)), nil
}

// VerifyUserEmail enforces one account per verified email. Emails are unique
// case-insensitively because NormalizeUserEmail stores them in lower-case.
func (s *DbUtils) VerifyUserEmail(email string, id int) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return true
	}
	available := true
	s.JsonDb.Users.Range(func(key, value interface{}) bool {
		user, ok := value.(*User)
		if !ok || user == nil {
			return true
		}
		user.RLock()
		userEmail, userID := user.Email, user.Id
		user.RUnlock()
		if userID != id && strings.EqualFold(strings.TrimSpace(userEmail), email) {
			available = false
			return false
		}
		return true
	})
	return available
}

func (s *DbUtils) MigrateUsersFromClients() error {
	return s.migrateUsersFromClients(true)
}

// migrateUsersFromClients normalizes legacy ownership in memory. Import paths
// pass persist=false so a failed SQLite transaction cannot rewrite the only
// rollback source.
func (s *DbUtils) migrateUsersFromClients(persist bool) error {
	type credential struct {
		userId   int
		password string
	}
	// Older releases persisted the dashboard credentials on each client but
	// did not have users.json. Existing matching credentials are always allowed
	// to repair an ownership link; creating a new user is only allowed when the
	// users file is absent. This keeps revoked accounts from being resurrected
	// merely because an old client record still contains its credentials.
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
			if _, exists := byID[clientUserID]; exists {
				return true
			}
			// A client can retain an old numeric UserId after users.json was
			// rebuilt or imported. If its legacy credentials identify an
			// existing user, repair the relationship instead of leaving the
			// client invisible to every dashboard account.
			if webUserName != "" && webPassword != "" {
				if cred, exists := byName[webUserName]; exists && cred.password == webPassword {
					c.Lock()
					c.UserId = cred.userId
					c.Unlock()
					changedClients = true
					return true
				}
			}
			if !recoverMissingOwners || webUserName == "" || webPassword == "" {
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
		cred, ok := byName[webUserName]
		if ok && cred.password == webPassword {
			// A legacy client without UserId can still be safely attached to
			// the existing user when both credentials match.
			c.Lock()
			c.UserId = cred.userId
			c.Unlock()
			changedClients = true
			return true
		}
		if !recoverMissingOwners {
			return true
		}
		name := webUserName
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
	if persist && (changedUsers || changedClients) {
		if err := s.JsonDb.persistKinds(store.KindUser, store.KindClient); err != nil {
			return fmt.Errorf("保存旧客户端账号迁移失败: %w", err)
		}
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

// UserResourceCounts is the compact resource summary shown in the user
// management table. Tunnel and host quotas use independent counters.
type UserResourceCounts struct {
	ClientCount int
	TunnelCount int
	HostCount   int
}

// GetUserResourceCounts returns current ownership counts without exposing any
// credentials. Runtime task/host objects can outlive a client replacement, so
// ownerUserID resolves the latest client record by ID before counting.
func (s *DbUtils) GetUserResourceCounts() map[int]UserResourceCounts {
	counts := make(map[int]UserResourceCounts)
	s.JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		userID := client.UserId
		client.RUnlock()
		if userID > 0 {
			entry := counts[userID]
			entry.ClientCount++
			counts[userID] = entry
		}
		return true
	})

	countTunnel := func(client *Client, noStore bool) {
		if noStore {
			return
		}
		userID := s.ownerUserID(client)
		if userID <= 0 {
			return
		}
		entry := counts[userID]
		entry.TunnelCount++
		counts[userID] = entry
	}
	countHost := func(client *Client, noStore bool) {
		if noStore {
			return
		}
		userID := s.ownerUserID(client)
		if userID <= 0 {
			return
		}
		entry := counts[userID]
		entry.HostCount++
		counts[userID] = entry
	}
	s.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		client, noStore := tunnel.Client, tunnel.NoStore
		tunnel.RUnlock()
		countTunnel(client, noStore)
		return true
	})
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		client, noStore := host.Client, host.NoStore
		host.RUnlock()
		countHost(client, noStore)
		return true
	})
	return counts
}

func (s *DbUtils) ownerUserID(client *Client) int {
	if client == nil {
		return 0
	}
	client.RLock()
	clientID, userID := client.Id, client.UserId
	client.RUnlock()
	if current, err := s.GetClient(clientID); err == nil && current != nil && current != client {
		current.RLock()
		userID = current.UserId
		current.RUnlock()
	}
	return userID
}

// GetClientOwnerID resolves the current owner for a client reference. Runtime
// tunnels and hosts can retain an older *Client after an edit replaces the
// record in JsonDb, so callers that enforce ownership should use this helper
// instead of reading the embedded pointer directly.
func (s *DbUtils) GetClientOwnerID(client *Client) int {
	return s.ownerUserID(client)
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
	t, ok := ParseExpiryTime(expireTime)
	// A malformed expiry is safer to treat as expired than to let it bypass the
	// bridge authentication check. ParseExpiryTime also keeps older persisted
	// date formats compatible with the normalized format used by new writes.
	return ok && time.Now().Before(t)
}

// IsClientActive applies the client's status and effective expiry. A client
// with a missing owner or malformed expiry fails closed, which keeps manual
// JSON edits from bypassing the expiration model between periodic cleanup
// passes.
func (s *DbUtils) IsClientActive(client *Client) bool {
	if client == nil {
		return false
	}
	return s.IsClientActiveWithExpiry(client, true)
}

// IsClientActiveWithExpiry applies status and owner checks and optionally
// enforces the effective client expiry. The Web feature switch uses this to
// preserve older deployments that intentionally did not enforce time limits.
func (s *DbUtils) IsClientActiveWithExpiry(client *Client, enforceExpiry bool) bool {
	if client == nil {
		return false
	}
	client.RLock()
	status, userID := client.Status, client.UserId
	client.RUnlock()
	if !status {
		return false
	}
	if userID != 0 && !s.IsUserActive(userID) {
		return false
	}
	if !enforceExpiry {
		return true
	}
	effectiveExpiry, err := s.EffectiveClientExpireTime(client)
	if err != nil || effectiveExpiry == "" {
		return err == nil
	}
	expiresAt, ok := ParseExpiryTime(effectiveExpiry)
	return ok && time.Now().Before(expiresAt)
}

// requireActiveRegisteredClient protects user-owned resource creation. A
// client pointer that is not present in the store cannot represent a live NPC
// session; remote creation paths resolve the client through JsonDb before
// reaching this helper, so only in-process fixtures and imports can arrive
// unregistered.
func (s *DbUtils) requireActiveRegisteredClient(client *Client) error {
	if client == nil {
		return errors.New("客户端记录无效")
	}
	client.RLock()
	clientID := client.Id
	client.RUnlock()
	if _, err := s.GetClient(clientID); err != nil {
		return nil
	}
	if !s.IsClientActive(client) {
		return errors.New("所属客户端已停用或已到期")
	}
	return nil
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
		client, noStore := t.Client, t.NoStore
		t.RUnlock()
		if !noStore && client != nil {
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

// GetClientTunnelNum counts persisted ordinary tunnels owned by one client.
// The caller may hold quotaMutationMu; the helper therefore only reads the
// immutable client identity snapshot and the tunnel map.
func (s *DbUtils) GetClientTunnelNum(clientID int) int {
	if clientID == 0 {
		return 0
	}
	num := 0
	s.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		client, noStore := tunnel.Client, tunnel.NoStore
		tunnel.RUnlock()
		if noStore || client == nil {
			return true
		}
		client.RLock()
		candidateID := client.Id
		client.RUnlock()
		if candidateID == clientID {
			num++
		}
		return true
	})
	return num
}

func (s *DbUtils) GetUserHostNum(userId int) int {
	clientIds := s.UserClientIds(userId)
	num := 0
	s.JsonDb.Hosts.Range(func(key, value interface{}) bool {
		h, ok := value.(*Host)
		if !ok || h == nil {
			return true
		}
		h.RLock()
		client, noStore := h.Client, h.NoStore
		h.RUnlock()
		if !noStore && client != nil {
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

// GetUserTunnelNumExcluding counts a user's tunnels while omitting one
// existing record. Ownership transfers use it to verify the target tenant
// without double-counting the tunnel being moved.
func (s *DbUtils) GetUserTunnelNumExcluding(userId, excludedTunnelID int) int {
	clientIds := s.UserClientIds(userId)
	num := 0
	s.JsonDb.Tasks.Range(func(_, value interface{}) bool {
		tunnel, ok := value.(*Tunnel)
		if !ok || tunnel == nil {
			return true
		}
		tunnel.RLock()
		client, noStore, tunnelID := tunnel.Client, tunnel.NoStore, tunnel.Id
		tunnel.RUnlock()
		if noStore || client == nil || tunnelID == excludedTunnelID {
			return true
		}
		client.RLock()
		clientID := client.Id
		client.RUnlock()
		if _, owned := clientIds[clientID]; owned {
			num++
		}
		return true
	})
	return num
}

// GetUserHostNumExcluding is the host-rule counterpart used by ownership
// transfers.
func (s *DbUtils) GetUserHostNumExcluding(userId, excludedHostID int) int {
	clientIds := s.UserClientIds(userId)
	num := 0
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		client, noStore, hostID := host.Client, host.NoStore, host.Id
		host.RUnlock()
		if noStore || client == nil || hostID == excludedHostID {
			return true
		}
		client.RLock()
		clientID := client.Id
		client.RUnlock()
		if _, owned := clientIds[clientID]; owned {
			num++
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
	return IntQuotaReached(s.GetUserTunnelNum(userId), maxTunnelNum)
}

func (s *DbUtils) IsUserHostLimitReached(userId int) bool {
	user, err := s.GetUser(userId)
	if err != nil || user == nil {
		return false
	}
	user.RLock()
	maxHostNum := user.MaxHostNum
	user.RUnlock()
	return IntQuotaReached(s.GetUserHostNum(userId), maxHostNum)
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
		verifyKey, status, clientID := v.VerifyKey, v.Status, v.Id
		v.RUnlock()
		if common.Getverifyval(verifyKey) != vKey {
			return true
		}
		// Keep the matched ID available even when authentication is rejected
		// so Bridge connection logs can identify the account and client.
		id = clientID
		if !status {
			rejectReason = errors.New("client disabled")
			return true
		}
		// Periodic expiry cleanup revokes current resources, while this check
		// closes the reconnect window and also rejects malformed expiry data.
		if !s.IsClientActive(v) {
			rejectReason = errors.New("client owner inactive or client expired")
			return true
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
		return id, rejectReason
	}
	return 0, errors.New("not found")
}

// GetClientBySecureKeyID resolves the non-secret identifier used by the
// challenge-response Bridge handshake. It returns the current client record
// and a snapshot of its VerifyKey; callers must validate the proof before
// treating the connection as authenticated.
func (s *DbUtils) GetClientBySecureKeyID(keyID []byte) (*Client, string, error) {
	if s == nil || s.JsonDb == nil || len(keyID) != 32 {
		return nil, "", errors.New("invalid client key identifier")
	}
	var matched *Client
	var verifyKey string
	s.JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		candidate := client.VerifyKey
		client.RUnlock()
		if candidate == "" || subtle.ConstantTimeCompare(crypt.SecureHandshakeKeyID(candidate), keyID) != 1 {
			return true
		}
		matched, verifyKey = client, candidate
		return false
	})
	if matched == nil {
		return nil, "", errors.New("client not found")
	}
	return matched, verifyKey, nil
}

func (s *DbUtils) NewTask(t *Tunnel) error {
	return s.newTask(t, false)
}

// NewTaskAsAdmin creates a tunnel without user-level quota checks. It is used
// only by administrator-authorized management paths.
func (s *DbUtils) NewTaskAsAdmin(t *Tunnel) error {
	return s.newTask(t, true)
}

func (s *DbUtils) newTask(t *Tunnel, bypassUserQuota bool) (err error) {
	if t == nil || t.Client == nil || t.Target == nil {
		return errors.New("隧道记录无效")
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	// Serialize the owner snapshot with client reassignment. Without this, a
	// concurrent transfer can charge user A while the stored tunnel ends up
	// owned by user B.
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	if !t.NoStore {
		if err := s.requireStoredClientReferenceLocked(t.Client); err != nil {
			return err
		}
	}
	if t.NoStore {
		// Runtime tunnels use a separate negative namespace. This also repairs
		// older callers that assigned a persistent ID before setting NoStore.
		if t.Id >= 0 {
			t.Id = int(s.JsonDb.GetTemporaryTaskID())
		}
	} else if t.Id <= 0 {
		t.Id = int(s.JsonDb.GetTaskId())
	}
	if t.Id > int(^uint32(0)>>1) {
		return errors.New("隧道 ID 超出支持范围")
	}
	if _, exists := s.JsonDb.Tasks.Load(t.Id); exists {
		return fmt.Errorf("隧道 ID %d 已存在", t.Id)
	}
	if !t.NoStore {
		for current := atomic.LoadInt32(&s.JsonDb.TaskIncreaseId); t.Id > int(current); current = atomic.LoadInt32(&s.JsonDb.TaskIncreaseId) {
			if atomic.CompareAndSwapInt32(&s.JsonDb.TaskIncreaseId, current, int32(t.Id)) {
				break
			}
		}
	}
	if err := NormalizeHealth(&t.Health); err != nil {
		return err
	}
	if err := normalizeTunnelProxyAuth(t); err != nil {
		return err
	}
	if t.Mode == "mixProxy" && !t.HttpProxy && !t.Socks5Proxy {
		t.HttpProxy, t.Socks5Proxy = true, true
	}
	t.CompileDestinationACL()
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
	if t.Flow == nil {
		t.Flow = NewFlow()
	}
	t.SetRateLimits(t.UploadLimit, t.DownloadLimit)
	if !t.NoStore {
		t.Client.RLock()
		clientID, maxTunnelNum := t.Client.Id, t.Client.MaxTunnelNum
		t.Client.RUnlock()
		if maxTunnelNum > 0 && s.GetClientTunnelNum(clientID) >= maxTunnelNum {
			return errors.New("客户端隧道数量已达到限制")
		}
	}
	userID := 0
	if !t.NoStore {
		userID = s.GetClientOwnerID(t.Client)
	}
	if !bypassUserQuota && userID <= 0 {
		// Unassigned clients act as the administrator's own resources. The
		// global user quotas do not apply to them.
		bypassUserQuota = true
	}
	if !t.NoStore {
		if err := s.requireActiveRegisteredClient(t.Client); err != nil {
			return err
		}
	}
	if !bypassUserQuota {
		if err := s.ReserveQuota(userID, QuotaTunnel); err != nil {
			return err
		}
	}
	s.JsonDb.Tasks.Store(t.Id, t)
	if err := s.JsonDb.StoreTasksToJsonFileErr(); err != nil {
		s.JsonDb.Tasks.Delete(t.Id)
		if !bypassUserQuota {
			s.ReleaseQuota(userID, QuotaTunnel)
		}
		return fmt.Errorf("保存隧道失败: %w", err)
	}
	if !bypassUserQuota {
		s.CommitQuota(userID, QuotaTunnel)
	}
	return nil
}

func (s *DbUtils) UpdateTask(t *Tunnel) error {
	return s.UpdateTaskWithOwnerChange(t, nil, false)
}

// UpdateTaskWithOwnerChange persists an edit and atomically admits the target
// owner's tunnel quota when the client changed. previousClient is the client
// before the edit. The caller may submit either the live object or a detached
// replacement; only the editable fields are copied onto the stored object so
// runtime traffic counters and active rate buckets survive the update.
func (s *DbUtils) UpdateTaskWithOwnerChange(t *Tunnel, previousClient *Client, bypassUserQuota bool) error {
	if t == nil || t.Client == nil || t.Target == nil {
		return errors.New("隧道记录无效")
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	storedValue, ok := s.JsonDb.Tasks.Load(t.Id)
	stored, valid := storedValue.(*Tunnel)
	if !ok || !valid || stored == nil {
		return errors.New("未找到隧道")
	}
	if stored == t {
		// Lifecycle callers (start/stop/health) mutate the live object first
		// and then persist it. Do not rewrite unrelated fields here: replacing
		// ACLs or health state would reset an active probe on every status save.
		if err := NormalizeHealth(&t.Health); err != nil {
			return err
		}
		if err := normalizeTunnelProxyAuth(t); err != nil {
			return err
		}
		t.CompileDestinationACL()
		if previousClient != nil && t.Client != nil && t.Client.Id != previousClient.Id {
			if err := s.prepareTunnelOwnerChangeLocked(t, previousClient, bypassUserQuota); err != nil {
				return err
			}
		}
		t.SetRateLimits(t.UploadLimit, t.DownloadLimit)
		if err := s.JsonDb.StoreTasksToJsonFileErr(); err != nil {
			return fmt.Errorf("保存隧道失败: %w", err)
		}
		return nil
	}
	edit := t
	if stored != t {
		edit = snapshotTunnelReplacement(t, stored)
	}
	if err := NormalizeHealth(&edit.Health); err != nil {
		return err
	}
	if err := normalizeTunnelProxyAuth(edit); err != nil {
		return err
	}
	edit.CompileDestinationACL()
	if previousClient != nil && edit.Client.Id != previousClient.Id {
		if err := s.prepareTunnelOwnerChangeLocked(edit, previousClient, bypassUserQuota); err != nil {
			return err
		}
	}
	if edit != t {
		edit.RateLimits.adopt(&stored.RateLimits)
	}
	edit.SetRateLimits(edit.UploadLimit, edit.DownloadLimit)
	previous := applyTunnelEdit(stored, edit)
	if err := s.JsonDb.StoreTasksToJsonFileErr(); err != nil {
		restoreTunnelEdit(stored, previous)
		return fmt.Errorf("保存隧道失败: %w", err)
	}
	return nil
}

func (s *DbUtils) prepareTunnelOwnerChangeLocked(t *Tunnel, previousClient *Client, bypassUserQuota bool) error {
	restore := func() {
		t.Lock()
		t.Client = previousClient
		t.Unlock()
	}
	target := t.Client
	if target == nil {
		restore()
		return errors.New("隧道所属客户端无效")
	}
	targetID := target.Id
	client, err := s.GetClient(targetID)
	if err != nil || client == nil {
		restore()
		return errors.New("隧道所属客户端不存在")
	}
	if !s.IsClientActive(client) {
		restore()
		return errors.New("所属客户端已停用或已到期")
	}
	userID := s.GetClientOwnerID(client)
	if bypassUserQuota || userID <= 0 {
		return nil
	}
	user, err := s.GetUser(userID)
	if err != nil || user == nil {
		restore()
		return errors.New("所属用户不存在")
	}
	user.RLock()
	maxTunnelNum := user.MaxTunnelNum
	user.RUnlock()
	// The tunnel is not yet owned by the target tenant, so exclude its own ID
	// defensively and require room for one additional record.
	used := s.GetUserTunnelNumExcluding(userID, t.Id)
	if IntQuotaReached(used, maxTunnelNum) {
		restore()
		return ErrQuotaExceeded
	}
	return nil
}

func (s *DbUtils) prepareHostOwnerChangeLocked(h *Host, previousClient *Client, bypassUserQuota bool) error {
	restore := func() {
		h.Lock()
		h.Client = previousClient
		h.Unlock()
	}
	target := h.Client
	if target == nil {
		restore()
		return errors.New("域名规则所属客户端无效")
	}
	client, err := s.GetClient(target.Id)
	if err != nil || client == nil {
		restore()
		return errors.New("域名规则所属客户端不存在")
	}
	if !s.IsClientActive(client) {
		restore()
		return errors.New("所属客户端已停用或已到期")
	}
	userID := s.GetClientOwnerID(client)
	if bypassUserQuota || userID <= 0 {
		return nil
	}
	user, err := s.GetUser(userID)
	if err != nil || user == nil {
		restore()
		return errors.New("所属用户不存在")
	}
	user.RLock()
	maxHostNum := user.MaxHostNum
	user.RUnlock()
	used := s.GetUserHostNumExcluding(userID, h.Id)
	if maxHostNum != nil && (*maxHostNum <= 0 || used >= *maxHostNum) {
		restore()
		return ErrQuotaExceeded
	}
	return nil
}

func normalizeTunnelProxyAuth(t *Tunnel) error {
	if t == nil {
		return nil
	}
	t.Lock()
	defer t.Unlock()
	value := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(t.ProxyAuth, "\r\n", "\n"), "\r", "\n"))
	if value == "" {
		t.ProxyAuth = ""
		return nil
	}
	if _, err := ParseBasicAuth(value); err != nil {
		return err
	}
	t.ProxyAuth = value
	return nil
}

func (s *DbUtils) SaveGlobal(t *Glob) error {
	if t == nil {
		return errors.New("全局参数无效")
	}
	if t.MaxConn < 0 {
		return errors.New("全局最大连接数不能小于 0")
	}
	if t.RateLimit < 0 {
		return errors.New("全局限速不能小于 0")
	}
	platformDomains, err := normalizePlatformDomains(t.PlatformDomains)
	if err != nil {
		return err
	}

	// Global platform domains and hosts must change as one logical operation:
	// a concurrent host creation must never observe an old certificate binding.
	globalMutationMu.Lock()
	defer globalMutationMu.Unlock()
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	previousGlobal := s.JsonDb.getGlobal()
	if err := s.validatePlatformDomainReferences(previousGlobal, platformDomains); err != nil {
		return err
	}
	if err := s.validatePlatformWildcardConflicts(platformDomains); err != nil {
		return err
	}
	if err := s.validatePlatformDomainHostSchemes(platformDomains); err != nil {
		return err
	}
	for _, domain := range platformDomains {
		if err := validatePlatformDomainCertificate(domain); err != nil {
			return err
		}
	}
	t.PlatformDomains = platformDomains
	s.JsonDb.setGlobal(t)
	SetGlobalMaxConn(t.MaxConn)
	rate.SetGlobalLimit(rate.BytesPerSecondFromBps(t.RateLimit))

	type hostCertificatePaths struct {
		cert string
		key  string
	}
	previousHostPaths := make(map[int]hostCertificatePaths)
	platformByID := make(map[string]PlatformDomain, len(platformDomains))
	for _, domain := range platformDomains {
		platformByID[domain.ID] = domain
	}
	hostsChanged := false
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.Lock()
		if domain, usesPlatformDomain := platformByID[host.PlatformDomainID]; usesPlatformDomain &&
			(host.CertFilePath != domain.CertFilePath || host.KeyFilePath != domain.KeyFilePath) {
			previousHostPaths[host.Id] = hostCertificatePaths{cert: host.CertFilePath, key: host.KeyFilePath}
			host.CertFilePath = domain.CertFilePath
			host.KeyFilePath = domain.KeyFilePath
			hostsChanged = true
		}
		host.Unlock()
		return true
	})
	kinds := []string{store.KindGlobal}
	if hostsChanged {
		kinds = append(kinds, store.KindHost)
	}
	if err := s.JsonDb.persistKinds(kinds...); err != nil {
		s.JsonDb.setGlobal(previousGlobal)
		if previousGlobal == nil {
			SetGlobalMaxConn(0)
			rate.SetGlobalLimit(0)
		} else {
			SetGlobalMaxConn(previousGlobal.MaxConn)
			rate.SetGlobalLimit(rate.BytesPerSecondFromBps(previousGlobal.RateLimit))
		}
		for id, previous := range previousHostPaths {
			if value, ok := s.JsonDb.Hosts.Load(id); ok {
				if host, valid := value.(*Host); valid && host != nil {
					host.Lock()
					host.CertFilePath = previous.cert
					host.KeyFilePath = previous.key
					host.Unlock()
				}
			}
		}
		// The legacy JSON backend cannot commit two files atomically. Best-effort
		// restore both files so a retry starts from the previous coherent state.
		if s.JsonDb.SQLite() == nil {
			_ = s.JsonDb.persistKinds(kinds...)
		}
		return fmt.Errorf("保存全局参数失败: %w", err)
	}
	return nil
}

// TaskDeleteCommit receives the deleted tunnel after persistence commits and
// before its ID may be reclaimed. Runtime listeners must be closed through this
// hook so a replacement tunnel cannot receive the old ID and then be stopped by
// delayed cleanup.
type TaskDeleteCommit func(task *Tunnel) error

func (s *DbUtils) DelTask(id int) error {
	return s.DelTaskWithHook(id, nil)
}

func (s *DbUtils) DelTaskWithHook(id int, onCommitted TaskDeleteCommit) error {
	taskMutationMu.Lock()
	defer taskMutationMu.Unlock()
	previous, hadPrevious := s.JsonDb.Tasks.LoadAndDelete(id)
	if err := s.JsonDb.StoreTasksToJsonFileErr(); err != nil {
		if hadPrevious {
			s.JsonDb.Tasks.Store(id, previous)
		}
		return fmt.Errorf("保存隧道失败: %w", err)
	}
	if onCommitted != nil {
		task, _ := previous.(*Tunnel)
		if err := onCommitted(task); err != nil {
			// The record is already committed as deleted. Keep the counter
			// monotonic because the old runtime object may still reference it.
			return fmt.Errorf("隧道 %d 已删除，但运行态清理失败: %w", id, err)
		}
	}
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
	previous, hadPrevious := s.JsonDb.Hosts.LoadAndDelete(id)
	if err := s.JsonDb.StoreHostToJsonFileErr(); err != nil {
		if hadPrevious {
			s.JsonDb.Hosts.Store(id, previous)
		}
		return fmt.Errorf("保存域名规则失败: %w", err)
	}
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
		if candidateClient != nil && candidateID != hostID && candidateHost == hostName &&
			location == candidateLocation && hostSchemesOverlap(scheme, candidateScheme) {
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
	// Negative client IDs belong to the same ephemeral NPC registration when
	// they match. They are intentionally excluded from the persisted tenant
	// namespace but may still own more than one runtime host rule.
	if aOwner.clientID < 0 || bOwner.clientID < 0 {
		return aOwner.clientID < 0 && aOwner.clientID == bOwner.clientID
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
	if net.ParseIP(rule) != nil {
		return true
	}
	if strings.ContainsAny(rule, ":/%?#\\\r\n\t ") {
		return false
	}
	if strings.Contains(rule, "*") {
		if !strings.HasPrefix(rule, "*.") || strings.Count(rule, "*") != 1 {
			return false
		}
		rule = strings.TrimPrefix(rule, "*.")
	}
	if len(rule) == 0 || len(rule) > 253 {
		return false
	}
	for _, label := range strings.Split(rule, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func normalizeHostScheme(scheme string) (string, error) {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" {
		// Historical clients omitted this field; the old runtime treated an
		// empty value as both HTTP and HTTPS.
		return "all", nil
	}
	switch scheme {
	case "http", "https", "all":
		return scheme, nil
	default:
		return "", errors.New("域名协议必须是 http、https 或 all")
	}
}

// hostSchemesOverlap reports whether two host rules can serve the same
// protocol. "all" intentionally covers both http and https, and the check is
// symmetric so an existing "all" rule conflicts with a later "http" rule and
// vice versa.
func hostSchemesOverlap(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" {
		a = "all"
	}
	if b == "" {
		b = "all"
	}
	return a == "all" || b == "all" || a == b
}

func validHostTargets(targets string) bool {
	targets = strings.ReplaceAll(targets, "\r\n", "\n")
	targets = strings.TrimSpace(targets)
	if targets == "" {
		return false
	}
	for _, target := range strings.Split(targets, "\n") {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		host, portText, err := net.SplitHostPort(target)
		if err != nil || strings.TrimSpace(host) == "" {
			return false
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

func normalizeHostMutation(host *Host) error {
	if host == nil || host.Client == nil || host.Target == nil {
		return errors.New("主机记录无效")
	}
	host.Host = normalizeHostName(host.Host)
	if !validHostRule(host.Host) {
		return errors.New("主机域名无效")
	}
	scheme, err := normalizeHostScheme(host.Scheme)
	if err != nil {
		return err
	}
	host.Scheme = scheme
	if host.Location == "" {
		host.Location = "/"
	}
	host.Location = normalizeHostLocation(host.Location)
	if !strings.HasPrefix(host.Location, "/") || strings.ContainsAny(host.Location, "?#\\\r\n") {
		return errors.New("路径规则必须是合法的绝对路径")
	}
	for _, segment := range strings.Split(host.Location, "/") {
		if segment == "." || segment == ".." {
			return errors.New("路径规则不能包含 . 或 .. 路径段")
		}
	}
	host.Target.TargetStr = strings.ReplaceAll(host.Target.TargetStr, "\r\n", "\n")
	host.Target.TargetStr = strings.TrimSpace(host.Target.TargetStr)
	if !validHostTargets(host.Target.TargetStr) {
		return errors.New("内网目标必须是 host:port 地址，每行一个")
	}
	if host.Scheme == "http" {
		host.AutoHttps = false
	}
	if strings.TrimSpace(host.Auth) != "" {
		if _, err := ParseBasicAuth(host.Auth); err != nil {
			return err
		}
		host.Auth = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(host.Auth, "\r\n", "\n"), "\r", "\n"))
	}
	if err := NormalizeHealth(&host.Health); err != nil {
		return err
	}
	return nil
}

// NormalizeHealth validates and canonicalizes a resource health-check
// profile. Health checks are executed by NPC because only the client can
// reach the private target; the server stores the profile and distributes it
// over the additive control message used by newer NPC versions.
func NormalizeHealth(health *Health) error {
	if health == nil {
		return nil
	}
	health.HealthLock()
	defer health.HealthUnlock()
	health.HealthCheckType = strings.ToLower(strings.TrimSpace(health.HealthCheckType))
	health.HealthCheckTarget = strings.ReplaceAll(health.HealthCheckTarget, "\r\n", "\n")
	health.HealthCheckTarget = strings.ReplaceAll(health.HealthCheckTarget, "\n", ",")
	parts := strings.Split(health.HealthCheckTarget, ",")
	targets := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		host, portText, err := net.SplitHostPort(part)
		if err != nil || strings.TrimSpace(host) == "" {
			return errors.New("健康检查目标必须是 host:port 地址")
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("健康检查目标端口无效")
		}
		targets = append(targets, net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port)))
	}
	if len(targets) == 0 {
		// Empty values disable health checking. Clear all stale settings so a
		// resource can be explicitly reset from the Web form.
		health.HealthCheckType = ""
		health.HealthCheckTarget = ""
		health.HealthCheckTimeout = 0
		health.HealthMaxFail = 0
		health.HealthCheckInterval = 0
		health.HttpHealthUrl = ""
		return nil
	}
	if health.HealthCheckType == "" {
		health.HealthCheckType = "http"
	}
	if health.HealthCheckType != "http" && health.HealthCheckType != "tcp" {
		return errors.New("健康检查类型必须是 http 或 tcp")
	}
	if health.HealthCheckTimeout <= 0 {
		health.HealthCheckTimeout = 2
	}
	if health.HealthMaxFail <= 0 {
		health.HealthMaxFail = 3
	}
	if health.HealthCheckInterval <= 0 {
		health.HealthCheckInterval = 10
	}
	if health.HealthCheckTimeout > 300 || health.HealthMaxFail > 100 || health.HealthCheckInterval > 3600 {
		return errors.New("健康检查参数超出允许范围")
	}
	if health.HealthCheckType == "http" {
		health.HttpHealthUrl = strings.TrimSpace(health.HttpHealthUrl)
		if health.HttpHealthUrl == "" {
			health.HttpHealthUrl = "/"
		}
		if !strings.HasPrefix(health.HttpHealthUrl, "/") || strings.ContainsAny(health.HttpHealthUrl, "\r\n") {
			return errors.New("HTTP 健康检查路径必须以 / 开头")
		}
	} else {
		health.HttpHealthUrl = ""
	}
	health.HealthCheckTarget = strings.Join(targets, ",")
	return nil
}

// HealthConfigSnapshot returns only the persisted/distributed health profile;
// runtime failure counters and timers are deliberately excluded.
func HealthConfigSnapshot(health *Health) Health {
	if health == nil {
		return Health{}
	}
	health.HealthRLock()
	snapshot := Health{
		HealthCheckTimeout:  health.HealthCheckTimeout,
		HealthMaxFail:       health.HealthMaxFail,
		HealthCheckInterval: health.HealthCheckInterval,
		HttpHealthUrl:       health.HttpHealthUrl,
		HealthCheckType:     health.HealthCheckType,
		HealthCheckTarget:   health.HealthCheckTarget,
	}
	health.HealthRUnlock()
	return snapshot
}

func setHealthConfig(dst *Health, src Health) {
	if dst == nil {
		return
	}
	dst.HealthLock()
	dst.HealthCheckTimeout = src.HealthCheckTimeout
	dst.HealthMaxFail = src.HealthMaxFail
	dst.HealthCheckInterval = src.HealthCheckInterval
	dst.HttpHealthUrl = src.HttpHealthUrl
	dst.HealthCheckType = src.HealthCheckType
	dst.HealthCheckTarget = src.HealthCheckTarget
	dst.HealthNextTime = time.Time{}
	dst.HealthMap = nil
	dst.HealthRemoveArr = nil
	dst.HealthUnlock()
}

func HealthConfigEnabled(health *Health) bool {
	if health == nil {
		return false
	}
	snapshot := HealthConfigSnapshot(health)
	return snapshot.HealthCheckTarget != "" && snapshot.HealthCheckTimeout > 0 && snapshot.HealthMaxFail > 0 && snapshot.HealthCheckInterval > 0
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
		if hostSchemesOverlap(scheme, candidateScheme) &&
			hostRulesOverlap(hostName, candidateHost) && hostLocationsOverlap(location, candidateLocation) {
			exists = true
			return false
		}
		return true
	})
	return exists
}

// GetPlatformDomains returns a defensive copy of the administrator-managed
// wildcard domains. Callers must use GetPlatformDomain when resolving a host
// so a stale browser payload cannot select a removed domain.
func (s *DbUtils) GetPlatformDomains() []PlatformDomain {
	global := s.GetGlobal()
	if global == nil {
		return nil
	}
	return append([]PlatformDomain(nil), global.PlatformDomains...)
}

// GetUsablePlatformDomains omits manually edited or stale entries whose
// certificate cannot safely serve a platform hostname. A pair of empty paths
// is intentionally retained as an HTTP-only wildcard; the Host write path
// rejects HTTPS or dual-protocol rules for that case. The administrator can
// still see and repair every entry on the global settings page.
func (s *DbUtils) GetUsablePlatformDomains() []PlatformDomain {
	domains := s.GetPlatformDomains()
	usable := make([]PlatformDomain, 0, len(domains))
	for _, domain := range domains {
		if err := validatePlatformDomainCertificate(domain); err == nil {
			usable = append(usable, domain)
		}
	}
	return usable
}

// GetPlatformDomain returns a managed wildcard domain by its stable ID.
func (s *DbUtils) GetPlatformDomain(id string) (*PlatformDomain, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("平台域名 ID 不能为空")
	}
	for _, domain := range s.GetPlatformDomains() {
		if domain.ID == id {
			return &domain, nil
		}
	}
	return nil, errors.New("未找到平台域名")
}

// ResolvePlatformHost builds a host from one DNS-label prefix and one managed
// wildcard. A platform domain intentionally supports exactly one label so a
// user cannot escape its namespace with a crafted multi-level value.
func (s *DbUtils) ResolvePlatformHost(platformDomainID, prefix string) (string, error) {
	domain, err := s.GetPlatformDomain(platformDomainID)
	if err != nil {
		return "", err
	}
	prefix, err = normalizePlatformPrefix(prefix)
	if err != nil {
		return "", err
	}
	return prefix + "." + strings.TrimPrefix(domain.Wildcard, "*."), nil
}

// IsPlatformHostAvailable reports whether a generated platform hostname can
// carry the requested route. Uniqueness follows normal host routing: the same
// hostname may be reused by different paths, while a duplicate hostname +
// path + scheme combination is rejected. Cross-tenant path shadowing is still
// rejected by IsHostRouteConflict when the record is written.
func (s *DbUtils) IsPlatformHostAvailable(platformDomainID, prefix, location, scheme string, excludeHostID int) (bool, error) {
	fullHost, err := s.ResolvePlatformHost(platformDomainID, prefix)
	if err != nil {
		return false, err
	}
	return s.isPlatformHostUnique(fullHost, location, scheme, excludeHostID), nil
}

func (s *DbUtils) isPlatformHostUnique(fullHost, location, scheme string, excludeHostID int) bool {
	fullHost = normalizeHostName(fullHost)
	location = normalizeHostLocation(location)
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" {
		scheme = "all"
	}
	available := true
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		hostID, hostName, hostLocation, hostScheme := host.Id, host.Host, host.Location, host.Scheme
		host.RUnlock()
		hostLocation = normalizeHostLocation(hostLocation)
		hostScheme = strings.ToLower(strings.TrimSpace(hostScheme))
		if hostScheme == "" {
			hostScheme = "all"
		}
		if hostID != excludeHostID && normalizeHostName(hostName) == fullHost &&
			location == hostLocation && hostSchemesOverlap(scheme, hostScheme) {
			available = false
			return false
		}
		return true
	})
	return available
}

// FindPlatformDomainByHost reports the managed domain that contains host.
// It follows NPS wildcard matching so a custom multi-level hostname cannot
// bypass a platform namespace. ResolvePlatformHost remains stricter and only
// creates single-label platform names.
func (s *DbUtils) FindPlatformDomainByHost(host string) (*PlatformDomain, bool) {
	host = normalizeHostName(host)
	for _, domain := range s.GetPlatformDomains() {
		if hostRuleMatches(host, domain.Wildcard) {
			return &domain, true
		}
	}
	return nil, false
}

// IsCustomHostInPlatformDomain lets the UI explain why a custom hostname is
// reserved for a platform wildcard. The write path enforces the same rule.
func (s *DbUtils) IsCustomHostInPlatformDomain(host string) bool {
	_, found := s.FindPlatformDomainByHost(host)
	return found
}

// PlatformDomainReferenceCount is used before changing or deleting a managed
// domain. A domain with references may update certificate paths, but its ID
// and wildcard name must remain stable.
func (s *DbUtils) PlatformDomainReferenceCount(id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0
	}
	count := 0
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		usesDomain := host.PlatformDomainID == id
		host.RUnlock()
		if usesDomain {
			count++
		}
		return true
	})
	return count
}

func (s *DbUtils) IsPlatformDomainInUse(id string) bool {
	return s.PlatformDomainReferenceCount(id) > 0
}

func (s *DbUtils) validatePlatformDomainReferences(previous *Glob, next []PlatformDomain) error {
	if previous == nil {
		return nil
	}
	nextByID := make(map[string]PlatformDomain, len(next))
	for _, domain := range next {
		nextByID[domain.ID] = domain
	}
	for _, previousDomain := range previous.PlatformDomains {
		if s.PlatformDomainReferenceCount(previousDomain.ID) == 0 {
			continue
		}
		nextDomain, exists := nextByID[previousDomain.ID]
		if !exists {
			return fmt.Errorf("平台域名 %s 正被主机使用，不能删除", previousDomain.Wildcard)
		}
		if nextDomain.Wildcard != previousDomain.Wildcard {
			return fmt.Errorf("平台域名 %s 正被主机使用，不能修改泛域名", previousDomain.Wildcard)
		}
	}
	return nil
}

// validatePlatformWildcardConflicts prevents a managed wildcard namespace from
// being added behind an existing wildcard route. Exact legacy hosts are kept
// compatible: their individual prefixes remain reserved, while unrelated
// platform prefixes can still be issued. A conflicting wildcard would instead
// intercept every newly issued platform host for one client or tenant.
func (s *DbUtils) validatePlatformWildcardConflicts(domains []PlatformDomain) error {
	if len(domains) == 0 {
		return nil
	}
	var conflict error
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		hostName, platformDomainID := host.Host, host.PlatformDomainID
		host.RUnlock()
		hostName = normalizeHostName(hostName)
		if !strings.HasPrefix(hostName, "*.") || !validHostRule(hostName) {
			return true
		}
		for _, domain := range domains {
			// Platform-mode hosts are guaranteed to use a single exact hostname
			// on normal writes. Ignore a legacy/corrupt self-reference here; the
			// persisted host remains subject to normal route validation.
			if platformDomainID == domain.ID {
				continue
			}
			if hostRulesOverlap(hostName, domain.Wildcard) {
				conflict = fmt.Errorf("平台泛域名 %s 与现有泛域名规则 %s 冲突，请先迁移或删除该规则", domain.Wildcard, hostName)
				return false
			}
		}
		return true
	})
	return conflict
}

// validatePlatformDomainHostSchemes prevents an administrator from removing
// a certificate while a referenced platform Host still requires HTTPS. The
// existing route must be edited to HTTP first; silently downgrading it would
// change its public behavior and could leave AutoHttps enabled.
func (s *DbUtils) validatePlatformDomainHostSchemes(domains []PlatformDomain) error {
	if len(domains) == 0 {
		return nil
	}
	domainsByID := make(map[string]PlatformDomain, len(domains))
	for _, domain := range domains {
		domainsByID[domain.ID] = domain
	}
	var conflict error
	s.JsonDb.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.RLock()
		domain, usesDomain := domainsByID[host.PlatformDomainID]
		scheme := strings.ToLower(strings.TrimSpace(host.Scheme))
		hostID := host.Id
		host.RUnlock()
		if usesDomain && domain.CertFilePath == "" && domain.KeyFilePath == "" && scheme != "http" {
			conflict = fmt.Errorf("平台域名 %s 仍被 HTTPS 或双协议主机 %d 使用，请先将该主机改为 HTTP", domain.Wildcard, hostID)
			return false
		}
		return true
	})
	return conflict
}

func (s *DbUtils) preparePlatformHost(host *Host, previous *Host) error {
	platformDomainID := strings.TrimSpace(host.PlatformDomainID)
	if platformDomainID == "" {
		if domain, withinPlatformDomain := s.FindPlatformDomainByHost(host.Host); withinPlatformDomain {
			// Existing custom hosts remain compatible when their hostname is not
			// changed. New records and renamed records must use the platform mode.
			if previous == nil {
				return fmt.Errorf("域名 %s 属于平台泛域名，请使用平台域名模式", domain.Wildcard)
			}
			previous.RLock()
			previousPlatformID, previousHost := previous.PlatformDomainID, previous.Host
			previous.RUnlock()
			if previousPlatformID != "" || normalizeHostName(previousHost) != normalizeHostName(host.Host) {
				return fmt.Errorf("域名 %s 属于平台泛域名，请使用平台域名模式", domain.Wildcard)
			}
		}
		return nil
	}

	domain, err := s.GetPlatformDomain(platformDomainID)
	if err != nil {
		return err
	}
	if err := validatePlatformDomainCertificate(*domain); err != nil {
		return err
	}
	if domain.CertFilePath == "" && domain.KeyFilePath == "" && host.Scheme != "http" {
		return fmt.Errorf("平台域名 %s 未配置证书，只能创建 HTTP 规则", domain.Wildcard)
	}
	hostName := normalizeHostName(host.Host)
	if !platformHostMatches(hostName, domain.Wildcard) {
		return fmt.Errorf("平台域名主机必须使用 %s 的单层前缀", domain.Wildcard)
	}
	if !s.isPlatformHostUnique(hostName, host.Location, host.Scheme, host.Id) {
		return errors.New("平台域名主机已被使用")
	}
	host.PlatformDomainID = domain.ID
	host.Host = hostName
	// Certificate paths are server-owned for platform hosts. This also blocks
	// direct API/NPC payloads from supplying a different certificate.
	host.CertFilePath = domain.CertFilePath
	host.KeyFilePath = domain.KeyFilePath
	return nil
}

func (s *DbUtils) NewHost(h *Host) error {
	return s.newHost(h, false)
}

// NewHostAsAdmin creates a host rule without user-level quota checks.
func (s *DbUtils) NewHostAsAdmin(h *Host) error {
	return s.newHost(h, true)
}

func (s *DbUtils) newHost(t *Host, bypassUserQuota bool) error {
	if err := normalizeHostMutation(t); err != nil {
		return err
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	if !t.NoStore {
		if err := s.requireStoredClientReferenceLocked(t.Client); err != nil {
			return err
		}
	}
	if t.NoStore {
		// Runtime hosts must never consume the persistent host counter. Older
		// callers may still arrive with a positive ID, so normalize those too.
		if t.Id >= 0 {
			t.Id = int(s.JsonDb.GetTemporaryHostID())
		}
	} else if t.Id <= 0 {
		t.Id = int(s.JsonDb.GetHostId())
	}
	if t.Id > int(^uint32(0)>>1) {
		return errors.New("主机 ID 超出支持范围")
	}
	if _, exists := s.JsonDb.Hosts.Load(t.Id); exists {
		return fmt.Errorf("主机 ID %d 已存在", t.Id)
	}
	if !t.NoStore {
		for current := atomic.LoadInt32(&s.JsonDb.HostIncreaseId); t.Id > int(current); current = atomic.LoadInt32(&s.JsonDb.HostIncreaseId) {
			if atomic.CompareAndSwapInt32(&s.JsonDb.HostIncreaseId, current, int32(t.Id)) {
				break
			}
		}
	}
	if err := s.preparePlatformHost(t, nil); err != nil {
		return err
	}
	if s.IsHostExist(t) {
		return errors.New("host has exist")
	}
	if s.IsHostRouteConflict(t) {
		return errors.New("host route overlaps another tenant")
	}
	if t.Flow == nil {
		t.Flow = NewFlow()
	}
	t.SetRateLimits(t.UploadLimit, t.DownloadLimit)
	userID := 0
	if !t.NoStore {
		userID = s.GetClientOwnerID(t.Client)
	}
	if !bypassUserQuota && userID <= 0 {
		bypassUserQuota = true
	}
	if !t.NoStore {
		if err := s.requireActiveRegisteredClient(t.Client); err != nil {
			return err
		}
	}
	if !bypassUserQuota {
		if err := s.ReserveQuota(userID, QuotaHost); err != nil {
			return err
		}
	}
	s.JsonDb.Hosts.Store(t.Id, t)
	if err := s.JsonDb.StoreHostToJsonFileErr(); err != nil {
		s.JsonDb.Hosts.Delete(t.Id)
		if !bypassUserQuota {
			s.ReleaseQuota(userID, QuotaHost)
		}
		return fmt.Errorf("保存域名规则失败: %w", err)
	}
	if !bypassUserQuota {
		s.CommitQuota(userID, QuotaHost)
	}
	return nil
}

// UpdateHost validates a replacement route and applies it while holding the
// same mutation lock used by NewHost. Runtime workers can continue using the
// existing object, while concurrent edits cannot race the conflict check.
func (s *DbUtils) UpdateHost(t *Host) error {
	return s.UpdateHostWithOwnerChange(t, nil, false)
}

// UpdateHostWithOwnerChange persists a host edit and atomically admits the
// target user's host-rule quota when the owning client changes. It mirrors
// UpdateTaskWithOwnerChange for the domain-rule dimension.
func (s *DbUtils) UpdateHostWithOwnerChange(t *Host, previousClient *Client, bypassUserQuota bool) error {
	if err := normalizeHostMutation(t); err != nil {
		return err
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	storedValue, ok := s.JsonDb.Hosts.Load(t.Id)
	stored, valid := storedValue.(*Host)
	if !ok || !valid || stored == nil {
		return errors.New("未找到主机")
	}
	if previousClient != nil {
		stored.RLock()
		storedClient := stored.Client
		stored.RUnlock()
		if storedClient != nil && t.Client != nil && t.Client.Id != storedClient.Id {
			if err := s.prepareHostOwnerChangeLocked(t, storedClient, bypassUserQuota); err != nil {
				return err
			}
		}
	}
	if err := s.preparePlatformHost(t, stored); err != nil {
		return err
	}
	if s.IsHostExist(t) {
		return errors.New("host has exist")
	}
	if s.IsHostRouteConflict(t) {
		return errors.New("host route overlaps another tenant")
	}
	previousACLPriority, previousBlack, previousWhite := sourceListSnapshot(&stored.SourceLists)
	stored.RLock()
	previousRegisteredOnly := stored.RegisteredOnly
	previous := Host{
		RateLimits: stored.RateLimits.copyConfig(),
		Host:       stored.Host, PlatformDomainID: stored.PlatformDomainID,
		HeaderChange: stored.HeaderChange, ResponseHeaderChange: stored.ResponseHeaderChange,
		HostChange: stored.HostChange, Location: stored.Location, PathRewrite: stored.PathRewrite,
		RedirectURL: stored.RedirectURL, Remark: stored.Remark, Auth: stored.Auth, Scheme: stored.Scheme,
		CertFilePath: stored.CertFilePath, KeyFilePath: stored.KeyFilePath,
		AutoHttps: stored.AutoHttps, AutoCORS: stored.AutoCORS, CompatMode: stored.CompatMode,
		TargetIsHTTPS: stored.TargetIsHTTPS, HTTPSJustProxy: stored.HTTPSJustProxy,
		AutoSSL: stored.AutoSSL, TLSOffload: stored.TLSOffload,
		Client: stored.Client, Target: stored.Target,
	}
	previousHealth := HealthConfigSnapshot(&stored.Health)
	stored.RUnlock()
	nextACLPriority, nextBlack, nextWhite := sourceListSnapshot(&t.SourceLists)
	t.RLock()
	nextRegisteredOnly := t.RegisteredOnly
	t.RUnlock()
	stored.Lock()
	stored.Host = t.Host
	stored.PlatformDomainID = t.PlatformDomainID
	stored.Client = t.Client
	stored.Target = t.Target
	stored.HeaderChange = t.HeaderChange
	stored.ResponseHeaderChange = t.ResponseHeaderChange
	stored.HostChange = t.HostChange
	stored.Remark = t.Remark
	stored.Auth = t.Auth
	stored.Location = t.Location
	stored.PathRewrite = t.PathRewrite
	stored.RedirectURL = t.RedirectURL
	stored.Scheme = t.Scheme
	stored.KeyFilePath = t.KeyFilePath
	stored.CertFilePath = t.CertFilePath
	stored.AutoHttps = t.AutoHttps
	stored.AutoCORS = t.AutoCORS
	stored.CompatMode = t.CompatMode
	stored.TargetIsHTTPS = t.TargetIsHTTPS
	stored.HTTPSJustProxy = t.HTTPSJustProxy
	stored.AutoSSL = t.AutoSSL
	stored.TLSOffload = t.TLSOffload
	stored.RegisteredOnly = nextRegisteredOnly
	stored.SetRateLimits(t.UploadLimit, t.DownloadLimit)
	if stored.Flow == nil {
		stored.Flow = NewFlow()
	}
	stored.Unlock()
	stored.SetSourceLists(nextACLPriority, nextBlack, nextWhite, acl.ScopeUser, "host")
	setHealthConfig(&stored.Health, HealthConfigSnapshot(&t.Health))
	if err := s.JsonDb.StoreHostToJsonFileErr(); err != nil {
		stored.Lock()
		stored.Host = previous.Host
		stored.PlatformDomainID = previous.PlatformDomainID
		stored.HeaderChange = previous.HeaderChange
		stored.ResponseHeaderChange = previous.ResponseHeaderChange
		stored.HostChange = previous.HostChange
		stored.Location = previous.Location
		stored.PathRewrite = previous.PathRewrite
		stored.RedirectURL = previous.RedirectURL
		stored.Remark = previous.Remark
		stored.Auth = previous.Auth
		stored.Scheme = previous.Scheme
		stored.CertFilePath = previous.CertFilePath
		stored.KeyFilePath = previous.KeyFilePath
		stored.AutoHttps = previous.AutoHttps
		stored.AutoCORS = previous.AutoCORS
		stored.CompatMode = previous.CompatMode
		stored.TargetIsHTTPS = previous.TargetIsHTTPS
		stored.HTTPSJustProxy = previous.HTTPSJustProxy
		stored.AutoSSL = previous.AutoSSL
		stored.TLSOffload = previous.TLSOffload
		stored.RegisteredOnly = previousRegisteredOnly
		stored.SetRateLimits(previous.UploadLimit, previous.DownloadLimit)
		stored.Client = previous.Client
		stored.Target = previous.Target
		stored.Unlock()
		stored.SetSourceLists(previousACLPriority, previousBlack, previousWhite, acl.ScopeUser, "host")
		setHealthConfig(&stored.Health, previousHealth)
		return fmt.Errorf("保存域名规则失败: %w", err)
	}
	return nil
}

func (s *DbUtils) GetHost(start, length int, id int, search string) ([]*Host, int) {
	return s.GetHostByAllowedClientsFiltered(start, length, id, search, nil, nil)
}

func (s *DbUtils) GetHostByAllowedClients(start, length int, id int, search string, allowedClientIds map[int]struct{}) ([]*Host, int) {
	return s.GetHostByAllowedClientsFiltered(start, length, id, search, nil, allowedClientIds)
}

// GetHostByAllowedClientsFiltered applies the optional dashboard owner filter
// before counting and pagination. Host ownership is inherited from the
// assigned client, so a zero owner selects hosts whose client is unassigned.
func (s *DbUtils) GetHostByAllowedClientsFiltered(start, length int, id int, search string, owner *OwnerFilter, allowedClientIds map[int]struct{}) ([]*Host, int) {
	return s.GetHostByAllowedClientsFilteredSorted(start, length, id, search, "", "", owner, allowedClientIds)
}

// GetHostByAllowedClientsFilteredSorted is the host-list variant that applies
// the requested sort before counting and pagination. Keeping the historical
// wrapper above preserves the API used by older callers.
func (s *DbUtils) GetHostByAllowedClientsFilteredSorted(start, length int, id int, search, sortField, order string, owner *OwnerFilter, allowedClientIds map[int]struct{}) ([]*Host, int) {
	all := make([]*Host, 0)
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
			clientUserID := s.GetClientOwnerID(client)
			if allowedClientIds != nil {
				if _, ok := allowedClientIds[clientID]; !ok {
					continue
				}
			}
			if owner != nil && owner.UserID != nil && clientUserID != *owner.UserID {
				continue
			}
			if search != "" && !(hostID == common.GetIntNoErrByStr(search) || strings.Contains(hostName, search) || strings.Contains(remark, search) || strings.Contains(verifyKey, search)) {
				continue
			}
			if id == 0 || clientID == id {
				all = append(all, v)
			}
		}
	}
	SortHosts(all, sortField, order)
	list := make([]*Host, 0, len(all))
	for _, host := range all {
		if start--; start < 0 {
			if length--; length >= 0 {
				list = append(list, host)
			}
		}
	}
	return list, len(all)
}

func (s *DbUtils) DelClient(id int) error {
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	return s.delClientLocked(id)
}

// DelClientCascade removes a client and every tunnel or host definition that
// references it in one persistence operation.
func (s *DbUtils) DelClientCascade(id int) error {
	return s.delClientCascade(id, nil, nil)
}

// DelClientCascadeWithHook removes the client and invokes onCommitted only
// after the persistence transaction succeeds.
func (s *DbUtils) DelClientCascadeWithHook(id int, onCommitted ClientCascadeCommit) error {
	return s.delClientCascade(id, nil, onCommitted)
}

func (s *DbUtils) delClientCascade(id int, requiredOwnerID *int, onCommitted ClientCascadeCommit) error {
	if id <= 0 {
		return errors.New("客户端 ID 无效")
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	return s.delClientCascadeLocked(id, requiredOwnerID, onCommitted)
}

func (s *DbUtils) DelClientCascadeForUser(id, userID int) error {
	return s.DelClientCascadeForUserWithHook(id, userID, nil)
}

func (s *DbUtils) DelClientCascadeForUserWithHook(id, userID int, onCommitted ClientCascadeCommit) error {
	if id <= 0 {
		return errors.New("客户端 ID 无效")
	}
	if userID <= 0 {
		return errors.New("用户 ID 无效")
	}
	return s.delClientCascade(id, &userID, onCommitted)
}

func (s *DbUtils) delClientCascadeLocked(id int, requiredOwnerID *int, onCommitted ClientCascadeCommit) error {
	clientValue, ok := s.JsonDb.Clients.Load(id)
	if !ok {
		return errors.New("未找到客户端")
	}
	client, valid := clientValue.(*Client)
	if !valid || client == nil {
		return errors.New("客户端记录无效")
	}
	if requiredOwnerID != nil {
		client.RLock()
		ownerID := client.UserId
		client.RUnlock()
		if ownerID != *requiredOwnerID {
			return errors.New("无权删除该客户端")
		}
	}

	type removedEntity struct {
		kind  string
		key   int
		value interface{}
	}
	removed := make([]removedEntity, 0)
	removedTunnels := make([]*Tunnel, 0)
	removedHosts := make([]*Host, 0)
	removeReferences := func(kind string, values *sync.Map) {
		values.Range(func(key, value interface{}) bool {
			entityID, ok := key.(int)
			if !ok {
				return true
			}
			var reference *Client
			switch obj := value.(type) {
			case *Tunnel:
				if obj != nil {
					reference = obj.Client
				}
			case *Host:
				if obj != nil {
					reference = obj.Client
				}
			}
			if reference == nil {
				return true
			}
			reference.RLock()
			referenceID := reference.Id
			reference.RUnlock()
			if referenceID != id {
				return true
			}
			if previous, loaded := values.LoadAndDelete(entityID); loaded {
				removed = append(removed, removedEntity{kind: kind, key: entityID, value: previous})
				switch entity := previous.(type) {
				case *Tunnel:
					removedTunnels = append(removedTunnels, entity)
				case *Host:
					removedHosts = append(removedHosts, entity)
				}
			}
			return true
		})
	}
	removeReferences(store.KindTunnel, &s.JsonDb.Tasks)
	removeReferences(store.KindHost, &s.JsonDb.Hosts)
	s.JsonDb.Clients.Delete(id)

	if err := s.JsonDb.persistKinds(store.KindClient, store.KindTunnel, store.KindHost); err != nil {
		s.JsonDb.Clients.Store(id, clientValue)
		for _, entity := range removed {
			switch entity.kind {
			case store.KindTunnel:
				s.JsonDb.Tasks.Store(entity.key, entity.value)
			case store.KindHost:
				s.JsonDb.Hosts.Store(entity.key, entity.value)
			}
		}
		return fmt.Errorf("保存客户端级联删除结果失败: %w", err)
	}

	s.JsonDb.markDeletedClient(client)
	if onCommitted != nil {
		onCommitted([]*Client{client}, removedTunnels, removedHosts)
	}
	client.RLock()
	clientRate := client.Rate
	client.RUnlock()
	if clientRate != nil {
		clientRate.Stop()
	}
	return nil
}

// DelClientForUser removes a client only while holding the same mutation lock
// used for ownership changes. This keeps a self-service delete from racing an
// administrator reassigning the client to another user.
func (s *DbUtils) DelClientForUser(id, userID int) error {
	if userID <= 0 {
		return errors.New("用户 ID 无效")
	}
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	old, ok := s.JsonDb.Clients.Load(id)
	if !ok {
		return errors.New("未找到客户端")
	}
	client, valid := old.(*Client)
	if !valid || client == nil {
		return errors.New("客户端记录无效")
	}
	client.RLock()
	ownerID := client.UserId
	client.RUnlock()
	if ownerID != userID {
		return errors.New("无权删除该客户端")
	}
	return s.delClientLocked(id)
}

func (s *DbUtils) delClientLocked(id int) error {
	previous, hadPrevious := s.JsonDb.Clients.LoadAndDelete(id)
	if err := s.JsonDb.StoreClientsToJsonFileErr(); err != nil {
		if hadPrevious {
			s.JsonDb.Clients.Store(id, previous)
		}
		return fmt.Errorf("保存客户端失败: %w", err)
	}
	if hadPrevious {
		if client, valid := previous.(*Client); valid && client != nil {
			client.RLock()
			clientRate := client.Rate
			client.RUnlock()
			if clientRate != nil {
				clientRate.Stop()
			}
		}
	}
	return nil
}

func (s *DbUtils) NewClient(c *Client) error {
	return s.newClient(c, false)
}

// NewClientAsAdmin creates a client without charging the assigned user's
// client quota. Administrator-created resources are outside the customer
// entitlement boundary; ownership, expiry and credential validation still
// apply so the resulting record remains consistent.
func (s *DbUtils) NewClientAsAdmin(c *Client) error {
	return s.newClient(c, true)
}

func (s *DbUtils) newClient(c *Client, bypassUserQuota bool) error {
	if c == nil {
		return errors.New("客户端记录无效")
	}
	if err := validateClientLimits(c); err != nil {
		return err
	}
	if c.ConnectionHistory == nil {
		c.ConnectionHistory = &ConnectionHistory{}
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()

	userID, expireTime, err := normalizeClientExpiry(c)
	if err != nil {
		return err
	}
	if bypassUserQuota {
		if err := s.ValidateClientOwner(userID); err != nil {
			return err
		}
		if err := s.validateClientExpiryForOwnerLocked(userID, expireTime); err != nil {
			return err
		}
	} else {
		if err := s.validateClientAssignmentLocked(userID, expireTime, 0); err != nil {
			return err
		}
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
	if c.NoStore {
		// Public-key onboarding clients are runtime-only. Keep them addressable
		// by the Bridge while preventing their IDs from consuming the persisted
		// client sequence.
		if c.Id >= 0 {
			c.Id = int(s.JsonDb.GetTemporaryClientID())
		}
	} else if c.Id == 0 {
		c.Id = int(s.JsonDb.GetClientId())
	}
	if c.Id > int(^uint32(0)>>1) {
		return errors.New("客户端 ID 超出支持范围")
	}
	if _, exists := s.JsonDb.Clients.Load(c.Id); exists {
		return fmt.Errorf("客户端 ID %d 已存在", c.Id)
	}
	if !c.NoStore {
		for current := atomic.LoadInt32(&s.JsonDb.ClientIncreaseId); c.Id > int(current); current = atomic.LoadInt32(&s.JsonDb.ClientIncreaseId) {
			if atomic.CompareAndSwapInt32(&s.JsonDb.ClientIncreaseId, current, int32(c.Id)) {
				break
			}
		}
	}
	if c.Flow == nil {
		c.Flow = NewFlow()
	}
	// Always rebuild the runtime bucket from the persisted bps value. Imported
	// objects may carry a stale Rate pointer from an earlier unit convention.
	c.Rate = rate.NewRate(clientRateBytes(c.RateLimit))
	c.Rate.Start()
	s.JsonDb.Clients.Store(c.Id, c)
	if err := s.JsonDb.StoreClientsToJsonFileErr(); err != nil {
		s.JsonDb.Clients.Delete(c.Id)
		c.Rate.Stop()
		return fmt.Errorf("保存客户端失败: %w", err)
	}
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
	return s.updateClient(t, false)
}

// UpdateClientAsAdmin updates administrator-managed client metadata without
// recharging or rejecting the assigned user's client quota. Ownership,
// credentials, and expiry integrity remain enforced.
func (s *DbUtils) UpdateClientAsAdmin(t *Client) error {
	return s.updateClient(t, true)
}

func (s *DbUtils) updateClient(t *Client, bypassUserQuota bool) error {
	if t == nil {
		return errors.New("客户端记录无效")
	}
	if err := validateClientLimits(t); err != nil {
		return err
	}
	quotaMutationMu.Lock()
	defer quotaMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()

	userID, expireTime, err := normalizeClientExpiry(t)
	if err != nil {
		return err
	}
	if bypassUserQuota {
		if err := s.ValidateClientOwner(userID); err != nil {
			return err
		}
		if err := s.validateClientExpiryForOwnerLocked(userID, expireTime); err != nil {
			return err
		}
	} else {
		if err := s.validateClientAssignmentLocked(userID, expireTime, t.Id); err != nil {
			return err
		}
	}
	previous, hadPrevious := s.JsonDb.Clients.Load(t.Id)
	var previousRate *rate.Rate
	if hadPrevious {
		if previousClient, valid := previous.(*Client); valid && previousClient != nil {
			previousClient.RLock()
			previousRate = previousClient.Rate
			previousClient.RUnlock()
		}
	}
	if t.Flow == nil {
		t.Flow = NewFlow()
	}
	newRate := rate.NewRate(clientRateBytes(t.RateLimit))
	t.Lock()
	t.Rate = newRate
	t.Unlock()
	newRate.Start()
	s.JsonDb.Clients.Store(t.Id, t)
	if err := s.JsonDb.StoreClientsToJsonFileErr(); err != nil {
		newRate.Stop()
		if hadPrevious {
			s.JsonDb.Clients.Store(t.Id, previous)
			if previousRate != nil {
				previousRate.Start()
			}
		} else {
			s.JsonDb.Clients.Delete(t.Id)
		}
		return fmt.Errorf("保存客户端失败: %w", err)
	}
	// Persistence succeeded; only now can the old limiter be retired safely.
	if previousRate != nil && previousRate != newRate {
		previousRate.Stop()
	}
	return nil
}

// validateClientLimits rejects negative resource limits before they can be
// persisted. Client limits use the historical zero-means-unlimited encoding,
// so a negative value has no runtime meaning and would otherwise bypass every
// positive-limit comparison.
func validateClientLimits(client *Client) error {
	if client == nil {
		return errors.New("客户端记录无效")
	}
	client.RLock()
	maxConn, maxTunnelNum, rateLimit := client.MaxConn, client.MaxTunnelNum, client.RateLimit
	client.RUnlock()
	if maxConn < 0 || maxTunnelNum < 0 || rateLimit < 0 {
		return errors.New("客户端配额、连接数和带宽限制不能小于 0")
	}
	return nil
}

// clientRateBytes maps the persisted client-wide bps value to the byte-rate
// token bucket. Zero keeps the historical default ceiling instead of turning a
// blank field into an accidental hard stop; the web and API layers only store
// zero when the administrator explicitly left the field blank.
func clientRateBytes(rateLimitBps int64) int64 {
	if rateLimitBps <= 0 {
		return (2 << 23) * 1024
	}
	return rate.BytesPerSecondFromBps(rateLimitBps)
}

func normalizeClientExpiry(client *Client) (userID int, expireTime string, err error) {
	client.Lock()
	defer client.Unlock()
	normalized, err := NormalizeExpiryTime(client.ExpireTime)
	if err != nil {
		return 0, "", err
	}
	client.ExpireTime = normalized
	return client.UserId, normalized, nil
}

func (s *DbUtils) IsPubClient(id int) bool {
	client, err := s.GetClient(id)
	if err != nil || client == nil {
		return false
	}
	client.RLock()
	noDisplay := client.NoDisplay
	client.RUnlock()
	// NoDisplay is the historical lifecycle marker: these records are hidden
	// service identities and must survive a disconnect without being deleted.
	// Security decisions that must also reject runtime-only onboarding clients
	// use IsPublicClient instead.
	return noDisplay
}

// IsPublicClient reports whether a client belongs to the public onboarding
// namespace. Such a client is runtime-only and must never inherit the
// server-local privileges reserved for internal negative IDs. Unlike
// IsPubClient, this is a permission predicate, not a resource-lifecycle one.
func (s *DbUtils) IsPublicClient(id int) bool {
	client, err := s.GetClient(id)
	if err != nil || client == nil {
		return false
	}
	client.RLock()
	noStore, noDisplay := client.NoStore, client.NoDisplay
	client.RUnlock()
	return noStore || noDisplay
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

func normalizePlatformDomains(input []PlatformDomain) ([]PlatformDomain, error) {
	if len(input) == 0 {
		return nil, nil
	}
	domains := make([]PlatformDomain, 0, len(input))
	byID := make(map[string]struct{}, len(input))
	for _, inputDomain := range input {
		domain := PlatformDomain{
			ID:           strings.TrimSpace(inputDomain.ID),
			CertFilePath: strings.TrimSpace(inputDomain.CertFilePath),
			KeyFilePath:  strings.TrimSpace(inputDomain.KeyFilePath),
		}
		wildcard, err := normalizePlatformWildcard(inputDomain.Wildcard)
		if err != nil {
			return nil, err
		}
		if domain.ID == "" {
			domain.ID, err = deterministicPlatformDomainID(wildcard, byID)
			if err != nil {
				return nil, err
			}
		}
		if _, exists := byID[domain.ID]; exists {
			return nil, errors.New("平台域名 ID 重复")
		}
		if (domain.CertFilePath == "") != (domain.KeyFilePath == "") {
			return nil, fmt.Errorf("平台域名 %s 的证书和私钥路径必须同时填写，或同时留空", wildcard)
		}
		domain.Wildcard = wildcard
		for _, existing := range domains {
			if hostRulesOverlap(domain.Wildcard, existing.Wildcard) {
				return nil, fmt.Errorf("平台泛域名 %s 与 %s 重叠", domain.Wildcard, existing.Wildcard)
			}
		}
		byID[domain.ID] = struct{}{}
		domains = append(domains, domain)
	}
	return domains, nil
}

// deterministicPlatformDomainID gives manually added legacy entries a stable
// ID before any Host can reference them. A random ID here would change on
// every restart until another global save happened, orphaning a Host created
// in that window.
func deterministicPlatformDomainID(wildcard string, existing map[string]struct{}) (string, error) {
	digest := sha256.Sum256([]byte(wildcard))
	encoded := hex.EncodeToString(digest[:])
	for length := 16; length <= len(encoded); length += 8 {
		id := "platform-" + encoded[:length]
		if _, found := existing[id]; !found {
			return id, nil
		}
	}
	return "", errors.New("生成平台域名 ID 冲突")
}

// validatePlatformDomainCertificate keeps a bad certificate path from being
// issued to a new platform hostname. The same check runs on SaveGlobal and at
// the data-layer Host write boundary so manually edited JSON cannot bypass it.
func validatePlatformDomainCertificate(domain PlatformDomain) error {
	// A platform wildcard may intentionally be HTTP-only. Empty certificate
	// paths are valid as a pair; HTTPS hosts still reject this configuration in
	// preparePlatformHost below.
	if domain.CertFilePath == "" && domain.KeyFilePath == "" {
		return nil
	}
	pair, err := tls.LoadX509KeyPair(common.ResolveConfigPath(domain.CertFilePath), common.ResolveConfigPath(domain.KeyFilePath))
	if err != nil {
		return fmt.Errorf("平台域名 %s 的证书或私钥不可用: %w", domain.Wildcard, err)
	}
	if len(pair.Certificate) == 0 {
		return fmt.Errorf("平台域名 %s 的证书内容为空", domain.Wildcard)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("平台域名 %s 的证书无法解析: %w", domain.Wildcard, err)
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
		return fmt.Errorf("平台域名 %s 的证书当前未生效或已过期", domain.Wildcard)
	}
	probeHost := "probe." + strings.TrimPrefix(domain.Wildcard, "*.")
	if err := certificate.VerifyHostname(probeHost); err != nil {
		return fmt.Errorf("平台域名 %s 的证书不覆盖该泛域名", domain.Wildcard)
	}
	return nil
}

func normalizePlatformWildcard(value string) (string, error) {
	value = normalizeHostName(value)
	if !strings.HasPrefix(value, "*.") || strings.Count(value, "*") != 1 {
		return "", errors.New("平台域名必须是 *.example.com 格式")
	}
	suffix := strings.TrimPrefix(value, "*.")
	if !validDNSDomainName(suffix) {
		return "", fmt.Errorf("平台域名 %s 无效", value)
	}
	return "*." + suffix, nil
}

func normalizePlatformPrefix(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if !validDNSLabel(value) {
		return "", errors.New("平台域名前缀必须是 1 到 63 位的字母、数字或连字符")
	}
	return value, nil
}

func validDNSDomainName(value string) bool {
	if len(value) == 0 || len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !validDNSLabel(label) {
			return false
		}
	}
	return true
}

func validDNSLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func platformHostMatches(host, wildcard string) bool {
	host, wildcard = normalizeHostName(host), normalizeHostName(wildcard)
	if !strings.HasPrefix(wildcard, "*.") {
		return false
	}
	suffix := strings.TrimPrefix(wildcard, "*.")
	if !strings.HasSuffix(host, "."+suffix) {
		return false
	}
	prefix := strings.TrimSuffix(host, "."+suffix)
	return validDNSLabel(prefix)
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
