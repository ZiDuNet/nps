package file

import (
	"encoding/json"
	"errors"
	"github.com/astaxie/beego/logs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/rate"
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
	ClientIncreaseId int32  //client increased id
	UserIncreaseId   int32  //user increased id
	TaskIncreaseId   int32  //task increased id
	HostIncreaseId   int32  //host increased id
	TaskFilePath     string //task file path
	HostFilePath     string //host file path
	ClientFilePath   string //client file path
	UserFilePath     string //user file path
	GlobalFilePath   string //global file path
}

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
		// first connection. Keep the persisted KB/s values untouched.
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
		if post.RateLimit > 0 {
			post.Rate = rate.NewRate(int64(post.RateLimit) * 1024)
		} else {
			post.Rate = rate.NewRate((2 << 23) * 1024)
		}
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
		rate.SetGlobalLimit(int64(post.RateLimit) * 1024)
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
	hostLock.Lock()
	defer hostLock.Unlock()
	return storeSyncMapToFile(&s.Hosts, s.HostFilePath)
}

var taskLock sync.Mutex

func (s *JsonDb) StoreTasksToJsonFile() {
	taskLock.Lock()
	storeSyncMapToFile(&s.Tasks, s.TaskFilePath)
	taskLock.Unlock()
}

var clientLock sync.Mutex

func (s *JsonDb) StoreClientsToJsonFile() {
	clientLock.Lock()
	storeSyncMapToFile(&s.Clients, s.ClientFilePath)
	clientLock.Unlock()
}

var userLock sync.Mutex

func (s *JsonDb) StoreUsersToJsonFile() {
	userLock.Lock()
	storeSyncMapToFile(&s.Users, s.UserFilePath)
	userLock.Unlock()
}

var globalLock sync.Mutex

func (s *JsonDb) StoreGlobalToJsonFile() {
	globalLock.Lock()
	storeGlobalToFile(s.getGlobal(), s.GlobalFilePath)
	globalLock.Unlock()
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
		var b []byte
		var err error
		switch obj := value.(type) {
		case *Tunnel:
			if obj.NoStore {
				return true
			}
			obj.RLock()
			if obj.Client != nil {
				obj.Client.RLock()
			}
			if obj.Flow != nil {
				obj.Flow.RLock()
			}
			if obj.Target != nil {
				obj.Target.RLock()
			}
			b, err = json.Marshal(obj)
			if obj.Target != nil {
				obj.Target.RUnlock()
			}
			if obj.Flow != nil {
				obj.Flow.RUnlock()
			}
			if obj.Client != nil {
				obj.Client.RUnlock()
			}
			obj.RUnlock()
		case *Host:
			if obj.NoStore {
				return true
			}
			obj.RLock()
			if obj.Client != nil {
				obj.Client.RLock()
			}
			if obj.Flow != nil {
				obj.Flow.RLock()
			}
			if obj.Target != nil {
				obj.Target.RLock()
			}
			b, err = json.Marshal(obj)
			if obj.Target != nil {
				obj.Target.RUnlock()
			}
			if obj.Flow != nil {
				obj.Flow.RUnlock()
			}
			if obj.Client != nil {
				obj.Client.RUnlock()
			}
			obj.RUnlock()
		case *Client:
			if obj.NoStore {
				return true
			}
			obj.RLock()
			if obj.Flow != nil {
				obj.Flow.RLock()
			}
			b, err = json.Marshal(obj)
			if obj.Flow != nil {
				obj.Flow.RUnlock()
			}
			obj.RUnlock()
		case *User:
			obj.RLock()
			b, err = json.Marshal(obj)
			obj.RUnlock()
		//case *Glob:
		//	obj := value.(*Glob)
		//	b, err = json.Marshal(obj)
		default:
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

func storeGlobalToFile(m *Glob, filePath string) {
	file, err := os.Create(filePath + ".tmp")
	// first create a temporary file to store
	if err != nil {
		logs.Error(err, "create temp file err")
		return
	}
	defer func() {
		_ = file.Close()
	}()

	var b []byte
	b, err = json.Marshal(m)
	if err != nil {
		logs.Error(err, "marshal json err")
		return
	}
	_, err = file.Write(b)
	if err != nil {
		logs.Error(err, "write file err")
		return
	}
	_ = file.Sync()
	// must close file first, then rename it
	_ = file.Close()
	err = os.Rename(filePath+".tmp", filePath)
	if err != nil {
		logs.Error(err, "store to file err, data will lost")
	}
}
