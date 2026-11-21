package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/rate"
	"ehang.io/nps/lib/store"
	"github.com/astaxie/beego/logs"
)

func NewJsonDb(runPath string) *JsonDb {
	db := &JsonDb{
		RunPath:        runPath,
		TaskFilePath:   filepath.Join(runPath, "conf", "tasks.json"),
		HostFilePath:   filepath.Join(runPath, "conf", "hosts.json"),
		ClientFilePath: filepath.Join(runPath, "conf", "clients.json"),
		UserFilePath:   filepath.Join(runPath, "conf", "users.json"),
		GlobalFilePath: filepath.Join(runPath, "conf", "global.json"),
	}
	_ = os.MkdirAll(filepath.Dir(db.TaskFilePath), 0750)
	return db
}

type JsonDb struct {
	Tasks            sync.Map
	Hosts            sync.Map
	HostsTmp         sync.Map
	Clients          sync.Map
	Users            sync.Map
	Global           *Glob
	globalMu         sync.RWMutex
	RunPath          string
	ClientIncreaseId int32 //client increased id
	UserIncreaseId   int32 //user increased id
	TaskIncreaseId   int32 //task increased id
	HostIncreaseId   int32 //host increased id
	// Temporary IDs live in a separate negative namespace. Runtime records
	// received from NPC are marked NoStore and must never advance the
	// persistent counters above; they still need stable keys while connected.
	temporaryClientID int32
	temporaryTaskID   int32
	temporaryHostID   int32
	TaskFilePath      string //task file path
	HostFilePath      string //host file path
	ClientFilePath    string //client file path
	UserFilePath      string //user file path
	GlobalFilePath    string //global file path
	// sqlite is optional. When set, the in-memory maps remain authoritative
	// for reads while every mutation is persisted through the SQLite store.
	sqlite            *store.Store
	persistMu         sync.Mutex
	deletedClientRefs sync.Map
}

const (
	metaClientIDHighWater = "id_high_water.client"
	metaUserIDHighWater   = "id_high_water.user"
	metaTaskIDHighWater   = "id_high_water.tunnel"
	metaHostIDHighWater   = "id_high_water.host"
)

func (s *JsonDb) LoadTaskFromJsonFile() {
	loadSyncMapFromFile(s.TaskFilePath, func(v string) {
		var err error
		post := new(Tunnel)
		if json.Unmarshal([]byte(v), &post) != nil || post == nil {
			return
		}
		if post.Client == nil {
			return
		}
		if post.Client, err = s.GetClient(post.Client.Id); err != nil {
			return
		}
		if post.Target == nil {
			return
		}
		post.CompileSourceLists(acl.ScopeUser, "tunnel")
		post.CompileDestinationACL()
		if post.Mode == "mixProxy" && !post.HttpProxy && !post.Socks5Proxy {
			// Older imports may contain a mixProxy record without the protocol
			// flags. Keep it usable with the mode's documented defaults; Web/API
			// writes reject explicitly disabling both protocols.
			post.HttpProxy = true
			post.Socks5Proxy = true
			logs.Warn("Tunnel %d has no enabled mixProxy protocol; enabling HTTP and SOCKS5 for compatibility", post.Id)
		}
		if post.Flow == nil {
			post.Flow = NewFlow()
		}
		if post.MaxConn < 0 {
			post.MaxConn = 0
		}
		// Resource limit buckets are runtime-only and are created lazily on the
		// first connection. Keep the persisted bps values untouched.
		post.Flow.NormalizeLegacyAccounting()
		// Runtime connection counts and startup errors never survive a restart.
		post.CurrentConnections = 0
		post.RunError = ""
		s.Tasks.Store(post.Id, post)
		if post.Id > int(s.TaskIncreaseId) {
			s.TaskIncreaseId = int32(post.Id)
		}
	})
}

func (s *JsonDb) LoadClientFromJsonFile() {
	loadSyncMapFromFile(s.ClientFilePath, func(v string) {
		post := new(Client)
		if json.Unmarshal([]byte(v), &post) != nil || post == nil {
			return
		}
		post.Rate = rate.NewRate(clientRateBytes(post.RateLimit))
		post.Rate.Start()
		if post.Flow == nil {
			post.Flow = NewFlow()
		}
		post.Flow.NormalizeLegacyAccounting()
		if post.ConnectionHistory == nil {
			post.ConnectionHistory = &ConnectionHistory{}
		}
		post.syncSourceListsFromFields()
		post.CompileSourceLists(acl.ScopeUser, "client")
		post.NowConn = 0
		s.Clients.Store(post.Id, post)
		if post.Id > int(s.ClientIncreaseId) {
			s.ClientIncreaseId = int32(post.Id)
		}
	})
}

func (s *JsonDb) LoadUserFromJsonFile() {
	loadSyncMapFromFile(s.UserFilePath, func(v string) {
		post := new(User)
		if json.Unmarshal([]byte(v), &post) != nil || post == nil {
			return
		}
		s.Users.Store(post.Id, post)
		SetUserRuntimeLimits(post.Id, post.MaxConn, post.BandwidthLimit)
		if post.Id > int(s.UserIncreaseId) {
			s.UserIncreaseId = int32(post.Id)
		}
	})
}

func (s *JsonDb) LoadHostFromJsonFile() {
	loaded := make([]*Host, 0)
	loadSyncMapFromFile(s.HostFilePath, func(v string) {
		var err error
		post := new(Host)
		if json.Unmarshal([]byte(v), &post) != nil || post == nil {
			return
		}
		if post.Client == nil {
			return
		}
		if post.Client, err = s.GetClient(post.Client.Id); err != nil {
			return
		}
		if post.Target == nil {
			return
		}
		post.CompileSourceLists(acl.ScopeUser, "host")
		if post.Flow == nil {
			post.Flow = NewFlow()
		}
		post.SetRateLimits(post.UploadLimit, post.DownloadLimit)
		post.Flow.NormalizeLegacyAccounting()
		loaded = append(loaded, post)
		if post.Id > int(s.HostIncreaseId) {
			s.HostIncreaseId = int32(post.Id)
		}
	})
	// Older versions allowed overlapping routes owned by different users. Load
	// lower IDs first and keep only the first rule in each conflicting set so a
	// persisted configuration cannot reintroduce cross-tenant shadowing.
	sort.SliceStable(loaded, func(i, j int) bool { return loaded[i].Id < loaded[j].Id })
	utils := &DbUtils{JsonDb: s}
	for _, host := range loaded {
		if utils.IsHostRouteConflict(host) {
			logs.Warn("skip overlapping host route %d (%s%s)", host.Id, host.Host, host.Location)
			continue
		}
		s.Hosts.Store(host.Id, host)
	}
}

func (s *JsonDb) LoadGlobalFromJsonFile() {
	loadSyncMapFromFileWithSingleJson(s.GlobalFilePath, func(v string) {
		post := new(Glob)
		if json.Unmarshal([]byte(v), &post) != nil || post == nil {
			return
		}
		platformDomains, err := normalizePlatformDomains(post.PlatformDomains)
		if err != nil {
			// Keep the existing global settings usable when a manually edited
			// platform-domain section is malformed. Administrators can correct it
			// in the management UI without a startup failure.
			logs.Warn("ignore invalid platform domains in %s: %v", s.GlobalFilePath, err)
			post.PlatformDomains = nil
		} else {
			post.PlatformDomains = platformDomains
		}
		s.setGlobal(post)
		post.syncSourceListsFromFields()
		post.CompileSourceLists(acl.ScopeGlobal, "global")
		SetGlobalMaxConn(post.MaxConn)
		rate.SetGlobalLimit(rate.BytesPerSecondFromBps(post.RateLimit))
	})
}

// ReconcilePlatformHostCertificates makes the global platform-domain record
// the single source of truth for managed host certificate paths. It repairs a
// valid but interrupted SaveGlobal persistence sequence on restart: global.json
// can contain a newer certificate path than hosts.json without leaving NPS
// serving the obsolete certificate.
func (s *JsonDb) ReconcilePlatformHostCertificates() bool {
	global := s.getGlobal()
	if global == nil || len(global.PlatformDomains) == 0 {
		return false
	}
	domains := make(map[string]PlatformDomain, len(global.PlatformDomains))
	for _, domain := range global.PlatformDomains {
		domains[domain.ID] = domain
	}
	changed := false
	s.Hosts.Range(func(_, value interface{}) bool {
		host, ok := value.(*Host)
		if !ok || host == nil {
			return true
		}
		host.Lock()
		if domain, usesPlatformDomain := domains[host.PlatformDomainID]; usesPlatformDomain &&
			platformHostMatches(host.Host, domain.Wildcard) &&
			(host.CertFilePath != domain.CertFilePath || host.KeyFilePath != domain.KeyFilePath) {
			host.CertFilePath = domain.CertFilePath
			host.KeyFilePath = domain.KeyFilePath
			changed = true
		}
		host.Unlock()
		return true
	})
	return changed
}

func (s *JsonDb) GetClient(id int) (c *Client, err error) {
	if v, ok := s.Clients.Load(id); ok {
		if c, ok = v.(*Client); ok && c != nil {
			return c, nil
		}
		return nil, errors.New("客户端记录无效")
	}
	err = errors.New("未找到客户端")
	return
}

var hostLock sync.Mutex

func (s *JsonDb) StoreHostToJsonFile() {
	_ = s.StoreHostToJsonFileErr()
}

// StoreHostToJsonFileErr persists the current host set and reports failures to
// mutation paths that must not claim success when the JSON file was not
// replaced. The legacy no-return wrapper remains for callers whose API cannot
// yet surface persistence errors.
func (s *JsonDb) StoreHostToJsonFileErr() error {
	return s.persistKinds(store.KindHost)
}

var taskLock sync.Mutex

func (s *JsonDb) StoreTasksToJsonFile() {
	_ = s.StoreTasksToJsonFileErr()
}

func (s *JsonDb) StoreTasksToJsonFileErr() error {
	return s.persistKinds(store.KindTunnel)
}

var clientLock sync.Mutex

func (s *JsonDb) StoreClientsToJsonFile() {
	_ = s.StoreClientsToJsonFileErr()
}

func (s *JsonDb) StoreClientsToJsonFileErr() error {
	return s.persistKinds(store.KindClient)
}

var userLock sync.Mutex

func (s *JsonDb) StoreUsersToJsonFile() {
	_ = s.StoreUsersToJsonFileErr()
}

func (s *JsonDb) StoreUsersToJsonFileErr() error {
	return s.persistKinds(store.KindUser)
}

// SetSQLite switches persistence to the supplied store. The in-memory maps
// stay authoritative for the runtime, matching the historical JSON design and
// keeping proxy hot paths free of database access.
func (s *JsonDb) SetSQLite(sqlite *store.Store) {
	if s == nil {
		return
	}
	s.sqlite = sqlite
}

// Flush persists every in-memory entity kind and the global settings snapshot.
// Shutdown uses this after stopping writers so the last flow_store_interval of
// accumulated traffic is not lost.
func (s *JsonDb) Flush() error {
	if s == nil {
		return nil
	}
	// Entity mutations update the in-memory maps before their SQLite
	// transaction starts. Block those mutations while the periodic snapshot is
	// assembled so Flush cannot persist a half-completed cascade or ownership
	// transfer. The order matches the mutation methods.
	globalMutationMu.Lock()
	defer globalMutationMu.Unlock()
	hostMutationMu.Lock()
	defer hostMutationMu.Unlock()
	taskMutationMu.Lock()
	defer taskMutationMu.Unlock()
	clientMutationMu.Lock()
	defer clientMutationMu.Unlock()
	return s.persistKinds(
		store.KindHost,
		store.KindTunnel,
		store.KindClient,
		store.KindUser,
		store.KindGlobal,
	)
}

func (s *JsonDb) SQLite() *store.Store {
	if s == nil {
		return nil
	}
	return s.sqlite
}

func entityRows(m *sync.Map) map[int][]byte {
	rows := make(map[int][]byte)
	m.Range(func(key, value interface{}) bool {
		id, ok := key.(int)
		if !ok || id == 0 {
			return true
		}
		data, keep := marshalStoredEntity(value)
		if !keep {
			return true
		}
		rows[id] = data
		return true
	})
	return rows
}

func globalRows(global *Glob) (map[int][]byte, error) {
	if global == nil {
		return nil, nil
	}
	global.RLock()
	defer global.RUnlock()
	data, err := json.Marshal(global)
	if err != nil {
		return nil, err
	}
	return map[int][]byte{0: data}, nil
}

// persistKinds snapshots one or more entity kinds and commits them as one
// logical operation. SQLite receives a single diff transaction; the legacy
// JSON backend keeps its file-by-file behavior.
func (s *JsonDb) persistKinds(kinds ...string) error {
	if s == nil || len(kinds) == 0 {
		return nil
	}
	// Serialize snapshot construction with every persistence operation. A
	// delayed writer that snapshots after another mutation cannot overwrite the
	// newer transaction with stale rows, and multi-kind snapshots stay ordered.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if s.sqlite != nil {
		snapshots := make(map[string]map[int][]byte, len(kinds))
		for _, kind := range kinds {
			switch kind {
			case store.KindClient:
				snapshots[kind] = entityRows(&s.Clients)
			case store.KindTunnel:
				snapshots[kind] = entityRows(&s.Tasks)
			case store.KindHost:
				snapshots[kind] = entityRows(&s.Hosts)
			case store.KindUser:
				snapshots[kind] = entityRows(&s.Users)
			case store.KindGlobal:
				rows, err := globalRows(s.getGlobal())
				if err != nil {
					return fmt.Errorf("序列化全局参数: %w", err)
				}
				snapshots[kind] = rows
			default:
				return fmt.Errorf("未知实体类型 %q", kind)
			}
		}
		return s.sqlite.ReplaceKindsWithMeta(snapshots, s.idHighWaterMeta())
	}
	for _, kind := range kinds {
		var err error
		switch kind {
		case store.KindClient:
			clientLock.Lock()
			err = storeSyncMapToFile(&s.Clients, s.ClientFilePath)
			clientLock.Unlock()
		case store.KindTunnel:
			taskLock.Lock()
			err = storeSyncMapToFile(&s.Tasks, s.TaskFilePath)
			taskLock.Unlock()
		case store.KindHost:
			hostLock.Lock()
			err = storeSyncMapToFile(&s.Hosts, s.HostFilePath)
			hostLock.Unlock()
		case store.KindUser:
			userLock.Lock()
			err = storeSyncMapToFile(&s.Users, s.UserFilePath)
			userLock.Unlock()
		case store.KindGlobal:
			globalLock.Lock()
			err = storeGlobalToFile(s.getGlobal(), s.GlobalFilePath)
			globalLock.Unlock()
		default:
			return fmt.Errorf("未知实体类型 %q", kind)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *JsonDb) idHighWaterMeta() map[string]string {
	return map[string]string{
		metaClientIDHighWater: fmt.Sprint(atomic.LoadInt32(&s.ClientIncreaseId)),
		metaUserIDHighWater:   fmt.Sprint(atomic.LoadInt32(&s.UserIncreaseId)),
		metaTaskIDHighWater:   fmt.Sprint(atomic.LoadInt32(&s.TaskIncreaseId)),
		metaHostIDHighWater:   fmt.Sprint(atomic.LoadInt32(&s.HostIncreaseId)),
	}
}

// marshalStoredEntity returns a consistent snapshot without persisting
// runtime-only records. Keep the JSON and SQLite writers on this single path so
// both backends observe the same locking and NoStore semantics.
func marshalStoredEntity(value interface{}) ([]byte, bool) {
	data, keep, _ := marshalStoredEntityWithError(value)
	return data, keep
}

func marshalStoredEntityWithError(value interface{}) ([]byte, bool, error) {
	switch obj := value.(type) {
	case *Tunnel:
		if obj == nil || obj.NoStore {
			return nil, false, nil
		}
		obj.RLock()
		defer obj.RUnlock()
		if obj.Client != nil {
			obj.Client.RLock()
			defer obj.Client.RUnlock()
		}
		if obj.Flow != nil {
			obj.Flow.RLock()
			defer obj.Flow.RUnlock()
		}
		if obj.Target != nil {
			obj.Target.RLock()
			defer obj.Target.RUnlock()
		}
		obj.RateLimits.rateMu.Lock()
		defer obj.RateLimits.rateMu.Unlock()
		obj.SourceLists.aclMu.RLock()
		defer obj.SourceLists.aclMu.RUnlock()
		obj.Health.HealthRLock()
		defer obj.Health.HealthRUnlock()
		data, err := json.Marshal(obj)
		return data, true, err
	case *Host:
		if obj == nil || obj.NoStore {
			return nil, false, nil
		}
		obj.RLock()
		defer obj.RUnlock()
		if obj.Client != nil {
			obj.Client.RLock()
			defer obj.Client.RUnlock()
		}
		if obj.Flow != nil {
			obj.Flow.RLock()
			defer obj.Flow.RUnlock()
		}
		if obj.Target != nil {
			obj.Target.RLock()
			defer obj.Target.RUnlock()
		}
		obj.RateLimits.rateMu.Lock()
		defer obj.RateLimits.rateMu.Unlock()
		obj.SourceLists.aclMu.RLock()
		defer obj.SourceLists.aclMu.RUnlock()
		obj.Health.HealthRLock()
		defer obj.Health.HealthRUnlock()
		data, err := json.Marshal(obj)
		return data, true, err
	case *Client:
		if obj == nil || obj.NoStore {
			return nil, false, nil
		}
		obj.RLock()
		defer obj.RUnlock()
		if obj.Flow != nil {
			obj.Flow.RLock()
			defer obj.Flow.RUnlock()
		}
		data, err := json.Marshal(obj)
		return data, true, err
	case *User:
		if obj == nil {
			return nil, false, nil
		}
		obj.RLock()
		defer obj.RUnlock()
		data, err := json.Marshal(obj)
		return data, true, err
	default:
		return nil, false, nil
	}
}

var globalLock sync.Mutex

func (s *JsonDb) StoreGlobalToJsonFile() {
	_ = s.StoreGlobalToJsonFileErr()
}

func (s *JsonDb) StoreGlobalToJsonFileErr() error {
	return s.persistKinds(store.KindGlobal)
}

func cloneGlobal(global *Glob) *Glob {
	if global == nil {
		return nil
	}
	global.RLock()
	copy := &Glob{
		ServerUrl:              global.ServerUrl,
		BlackIpList:            append([]string(nil), global.BlackIpList...),
		WhiteIpList:            append([]string(nil), global.WhiteIpList...),
		IpListPriority:         global.IpListPriority,
		MaxConn:                global.MaxConn,
		RateLimit:              global.RateLimit,
		PlatformDomains:        append([]PlatformDomain(nil), global.PlatformDomains...),
		UsageGuideMode:         global.UsageGuideMode,
		UsageGuideSource:       global.UsageGuideSource,
		DashboardKeyHash:       global.DashboardKeyHash,
		DashboardKeyCiphertext: global.DashboardKeyCiphertext,
		AdminAPIKeyHash:        global.AdminAPIKeyHash,
		AdminAPIKeyCiphertext:  global.AdminAPIKeyCiphertext,
	}
	global.RUnlock()
	return copy
}

func (s *JsonDb) setGlobal(global *Glob) {
	s.globalMu.Lock()
	s.Global = cloneGlobal(global)
	s.globalMu.Unlock()
}

func (s *JsonDb) getGlobal() *Glob {
	s.globalMu.RLock()
	global := cloneGlobal(s.Global)
	s.globalMu.RUnlock()
	return global
}

func (s *JsonDb) GetClientId() int32 {
	return atomic.AddInt32(&s.ClientIncreaseId, 1)
}

func (s *JsonDb) GetUserId() int32 {
	return atomic.AddInt32(&s.UserIncreaseId, 1)
}

func (s *JsonDb) GetTaskId() int32 {
	return atomic.AddInt32(&s.TaskIncreaseId, 1)
}

func (s *JsonDb) GetHostId() int32 {
	return atomic.AddInt32(&s.HostIncreaseId, 1)
}

// markDeletedClient keeps a short-lived identity tombstone so a request that
// captured the old *Client before deletion cannot create a resource after its
// ID or fields have been reused.
func (s *JsonDb) markDeletedClient(client *Client) {
	if s == nil || client == nil {
		return
	}
	s.deletedClientRefs.Store(client, struct{}{})
	time.AfterFunc(time.Hour, func() {
		s.deletedClientRefs.Delete(client)
	})
}

// GetTemporaryClientID returns a negative ID for an ephemeral client. The
// counter is intentionally independent from ClientIncreaseId so public or
// runtime-only clients do not create gaps in clients.json IDs.
func (s *JsonDb) GetTemporaryClientID() int32 {
	return atomic.AddInt32(&s.temporaryClientID, -1)
}

// GetTemporaryTaskID returns a negative ID for an ephemeral tunnel.
func (s *JsonDb) GetTemporaryTaskID() int32 {
	return atomic.AddInt32(&s.temporaryTaskID, -1)
}

// GetTemporaryHostID returns a negative ID for an ephemeral host rule.
func (s *JsonDb) GetTemporaryHostID() int32 {
	return atomic.AddInt32(&s.temporaryHostID, -1)
}

func loadSyncMapFromFile(filePath string, f func(value string)) {
	if !common.FileExists(filePath) {
		return
	}
	b, err := common.ReadAllFromFile(filePath)
	if err != nil {
		logs.Warn("skip unreadable data file %s: %v", filePath, err)
		return
	}
	for _, v := range strings.Split(string(b), "\n"+common.CONN_DATA_SEQ) {
		f(v)
	}
}

func loadSyncMapFromFileWithSingleJson(filePath string, f func(value string)) {
	if !common.FileExists(filePath) {
		return
	}

	b, err := common.ReadAllFromFile(filePath)
	if err != nil {
		logs.Warn("skip unreadable data file %s: %v", filePath, err)
		return
	}

	f(string(b))
}

func storeSyncMapToFile(m *sync.Map, filePath string) error {
	file, err := os.Create(filePath + ".tmp")
	// first create a temporary file to store
	if err != nil {
		logs.Error(err, "create temp file err")
		return err
	}
	tempPath := filePath + ".tmp"
	var writeErr error
	defer func() {
		if writeErr != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
			return
		}
	}()
	m.Range(func(key, value interface{}) bool {
		b, keep, err := marshalStoredEntityWithError(value)
		if !keep {
			return true
		}
		if err != nil {
			logs.Error(err, "marshal json err")
			writeErr = err
			return false
		}
		_, err = file.Write(b)
		if err != nil {
			logs.Error(err, "write file err")
			writeErr = err
			return false
		}
		_, err = file.Write([]byte("\n" + common.CONN_DATA_SEQ))
		if err != nil {
			logs.Error(err, "write file err")
			writeErr = err
			return false
		}
		return true
	})
	if writeErr != nil {
		return writeErr
	}
	if err = file.Sync(); err != nil {
		logs.Error(err, "sync file err")
		writeErr = err
		return err
	}
	if err = file.Close(); err != nil {
		logs.Error(err, "close file err")
		writeErr = err
		return err
	}
	err = os.Rename(tempPath, filePath)
	if err != nil {
		logs.Error(err, "store to file err, data will lost")
		writeErr = err
		return err
	}
	return nil
}

func storeGlobalToFile(m *Glob, filePath string) error {
	file, err := os.Create(filePath + ".tmp")
	// first create a temporary file to store
	if err != nil {
		logs.Error(err, "create temp file err")
		return err
	}
	defer func() {
		_ = file.Close()
	}()

	var b []byte
	b, err = json.Marshal(m)
	if err != nil {
		logs.Error(err, "marshal json err")
		return err
	}
	_, err = file.Write(b)
	if err != nil {
		logs.Error(err, "write file err")
		return err
	}
	if err := file.Sync(); err != nil {
		logs.Error(err, "sync file err")
		return err
	}
	// must close file first, then rename it
	if err := file.Close(); err != nil {
		logs.Error(err, "close file err")
		return err
	}
	err = os.Rename(filePath+".tmp", filePath)
	if err != nil {
		logs.Error(err, "store to file err, data will lost")
		return err
	}
	return nil
}
