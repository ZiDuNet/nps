package file

import (
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/rate"
)

type DbUtils struct {
	JsonDb *JsonDb
}

var (
	Db   *DbUtils
	once sync.Once
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

func GetMapKeys(m sync.Map, isSort bool, sortKey, order string) (keys []int) {
	if sortKey != "" && isSort {
		return sortClientByKey(m, sortKey, order)
	}
	m.Range(func(key, value interface{}) bool {
		keys = append(keys, key.(int))
		return true
	})
	sort.Ints(keys)
	return
}

func (s *DbUtils) GetClientList(start, length int, search, sort, order string, clientId int) ([]*Client, int) {
	list := make([]*Client, 0)
	var cnt int
	keys := GetMapKeys(s.JsonDb.Clients, true, sort, order)
	for _, key := range keys {
		if value, ok := s.JsonDb.Clients.Load(key); ok {
			v := value.(*Client)
			if v.NoDisplay {
				continue
			}
			if clientId != 0 && clientId != v.Id {
				continue
			}
			if search != "" && !(v.Id == common.GetIntNoErrByStr(search) || strings.Contains(v.VerifyKey, search) || strings.Contains(v.Remark, search)) {
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
		v := value.(*User)
		if search != "" && !(v.Id == common.GetIntNoErrByStr(search) || strings.Contains(v.UserName, search) || strings.Contains(v.Remark, search)) {
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
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		c := value.(*Client)
		if c.UserId == id {
			c.UserId = 0
		}
		return true
	})
	s.JsonDb.Users.Delete(id)
	s.JsonDb.StoreUsersToJsonFile()
	s.JsonDb.StoreClientsToJsonFile()
	return nil
}

func (s *DbUtils) GetUser(id int) (*User, error) {
	if v, ok := s.JsonDb.Users.Load(id); ok {
		return v.(*User), nil
	}
	return nil, errors.New("未找到用户")
}

func (s *DbUtils) GetUserByName(username string) (*User, error) {
	var user *User
	s.JsonDb.Users.Range(func(key, value interface{}) bool {
		v := value.(*User)
		if v.UserName == username {
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
		v := value.(*User)
		if v.UserName == username && v.Id != id {
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
	byName := make(map[string]credential)
	s.JsonDb.Users.Range(func(key, value interface{}) bool {
		u := value.(*User)
		byName[u.UserName] = credential{userId: u.Id, password: u.Password}
		return true
	})

	changedUsers := false
	changedClients := false
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		c := value.(*Client)
		if c.UserId != 0 || c.WebUserName == "" || c.WebPassword == "" {
			return true
		}
		name := c.WebUserName
		cred, ok := byName[name]
		if ok && cred.password != c.WebPassword {
			name = fmt.Sprintf("%s_%d", c.WebUserName, c.Id)
			ok = false
		}
		if !ok {
			u := &User{
				Id:         int(s.JsonDb.GetUserId()),
				UserName:   name,
				Password:   c.WebPassword,
				Status:     true,
				Remark:     c.Remark,
				CreateTime: time.Now().Format("2006-01-02 15:04:05"),
			}
			s.JsonDb.Users.Store(u.Id, u)
			cred = credential{userId: u.Id, password: u.Password}
			byName[name] = cred
			changedUsers = true
		}
		c.UserId = cred.userId
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
		c := value.(*Client)
		if c.UserId == userId {
			ids[c.Id] = struct{}{}
		}
		return true
	})
	return ids
}

func (s *DbUtils) IsClientBelongToUser(clientId, userId int) bool {
	c, err := s.GetClient(clientId)
	return err == nil && c.UserId == userId
}

func (s *DbUtils) IsUserActive(userId int) bool {
	user, err := s.GetUser(userId)
	if err != nil || !user.Status {
		return false
	}
	if user.ExpireTime == "" {
		return true
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", user.ExpireTime, time.Local)
	return err != nil || time.Now().Before(t)
}

func (s *DbUtils) GetUserTunnelNum(userId int) int {
	clientIds := s.UserClientIds(userId)
	num := 0
	s.JsonDb.Tasks.Range(func(key, value interface{}) bool {
		t := value.(*Tunnel)
		if t.Client != nil {
			if _, ok := clientIds[t.Client.Id]; ok {
				num++
			}
		}
		return true
	})
	s.JsonDb.Hosts.Range(func(key, value interface{}) bool {
		h := value.(*Host)
		if h.Client != nil {
			if _, ok := clientIds[h.Client.Id]; ok {
				num++
			}
		}
		return true
	})
	return num
}

func (s *DbUtils) IsUserTunnelLimitReached(userId int) bool {
	user, err := s.GetUser(userId)
	if err != nil || user.MaxTunnelNum == 0 {
		return false
	}
	return s.GetUserTunnelNum(userId) >= user.MaxTunnelNum
}

func (s *DbUtils) GetIdByVerifyKey(vKey string, addr string) (id int, err error) {
	var exist bool
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v := value.(*Client)
		if common.Getverifyval(v.VerifyKey) == vKey && v.Status {
			v.Addr = common.GetIpByAddr(addr)
			id = v.Id
			exist = true
			return false
		}
		return true
	})
	if exist {
		return
	}
	return 0, errors.New("not found")
}

func (s *DbUtils) NewTask(t *Tunnel) (err error) {
	s.JsonDb.Tasks.Range(func(key, value interface{}) bool {
		v := value.(*Tunnel)
		if (v.Mode == "secret" || v.Mode == "p2p") && v.Password == t.Password && t.Password != "" {
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
	s.JsonDb.Tasks.Store(t.Id, t)
	s.JsonDb.StoreTasksToJsonFile()
	return nil
}

func (s *DbUtils) SaveGlobal(t *Glob) error {
	s.JsonDb.Global = t
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
		if crypt.Md5(value.(*Tunnel).Password) == p {
			t = value.(*Tunnel)
			return false
		}
		return true
	})
	return
}

func (s *DbUtils) GetTask(id int) (t *Tunnel, err error) {
	if v, ok := s.JsonDb.Tasks.Load(id); ok {
		t = v.(*Tunnel)
		return
	}
	err = errors.New("not found")
	return
}

func (s *DbUtils) DelHost(id int) error {
	s.JsonDb.Hosts.Delete(id)
	s.JsonDb.StoreHostToJsonFile()
	return nil
}

func (s *DbUtils) IsHostExist(h *Host) bool {
	var exist bool
	s.JsonDb.Hosts.Range(func(key, value interface{}) bool {
		v := value.(*Host)
		if v.Id != h.Id && v.Host == h.Host && h.Location == v.Location && (v.Scheme == "all" || v.Scheme == h.Scheme) {
			exist = true
			return false
		}
		return true
	})
	return exist
}

func (s *DbUtils) NewHost(t *Host) error {
	if t.Location == "" {
		t.Location = "/"
	}
	if s.IsHostExist(t) {
		return errors.New("host has exist")
	}
	t.Flow = new(Flow)
	s.JsonDb.Hosts.Store(t.Id, t)
	s.JsonDb.StoreHostToJsonFile()
	return nil
}

func (s *DbUtils) GetHost(start, length int, id int, search string) ([]*Host, int) {
	return s.GetHostByAllowedClients(start, length, id, search, nil)
}

func (s *DbUtils) GetHostByAllowedClients(start, length int, id int, search string, allowedClientIds map[int]struct{}) ([]*Host, int) {
	list := make([]*Host, 0)
	var cnt int
	keys := GetMapKeys(s.JsonDb.Hosts, false, "", "")
	for _, key := range keys {
		if value, ok := s.JsonDb.Hosts.Load(key); ok {
			v := value.(*Host)
			if allowedClientIds != nil {
				if _, ok := allowedClientIds[v.Client.Id]; !ok {
					continue
				}
			}
			if search != "" && !(v.Id == common.GetIntNoErrByStr(search) || strings.Contains(v.Host, search) || strings.Contains(v.Remark, search) || strings.Contains(v.Client.VerifyKey, search)) {
				continue
			}
			if id == 0 || v.Client.Id == id {
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
	s.JsonDb.Clients.Delete(id)
	s.JsonDb.StoreClientsToJsonFile()
	return nil
}

func (s *DbUtils) NewClient(c *Client) error {
	var isNotSet bool
reset:
	if c.VerifyKey == "" || isNotSet {
		isNotSet = true
		c.VerifyKey = crypt.GetVkey()
	}
	if c.RateLimit == 0 {
		c.Rate = rate.NewRate((2 << 23) * 1024)
	} else if c.Rate == nil {
		c.Rate = rate.NewRate(int64(c.RateLimit * 1024))
	}
	c.Rate.Start()
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
	s.JsonDb.Clients.Store(c.Id, c)
	s.JsonDb.StoreClientsToJsonFile()
	return nil
}

func (s *DbUtils) VerifyVkey(vkey string, id int) (res bool) {
	res = true
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v := value.(*Client)
		if v.VerifyKey == vkey && v.Id != id {
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
		v := value.(*Client)
		if v.WebUserName == username && v.Id != id {
			res = false
			return false
		}
		return true
	})
	return res
}

func (s *DbUtils) UpdateClient(t *Client) error {
	// 先 Stop 旧的 Rate，防止内存泄漏
	if old, ok := s.JsonDb.Clients.Load(t.Id); ok {
		oldClient := old.(*Client)
		if oldClient.Rate != nil {
			oldClient.Rate.Stop()
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
	if err == nil {
		return client.NoDisplay
	}
	return false
}

func (s *DbUtils) GetClient(id int) (c *Client, err error) {
	if v, ok := s.JsonDb.Clients.Load(id); ok {
		c = v.(*Client)
		return
	}
	err = errors.New("未找到客户端")
	return
}

func (s *DbUtils) GetGlobal() (c *Glob) {
	return s.JsonDb.Global
}

func (s *DbUtils) GetClientIdByVkey(vkey string) (id int, err error) {
	var exist bool
	s.JsonDb.Clients.Range(func(key, value interface{}) bool {
		v := value.(*Client)
		if crypt.Md5(v.VerifyKey) == vkey {
			exist = true
			id = v.Id
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
		v := value.(*Client)
		if fmt.Sprintf("%x", md5.Sum([]byte(v.VerifyKey))) == vkey {
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
		h = v.(*Host)
		return
	}
	err = errors.New("The host could not be parsed")
	return
}

// get key by host from x
func (s *DbUtils) GetInfoByHost(host string, r *http.Request) (h *Host, err error) {
	var hosts []*Host
	//Handling Ported Access
	host = common.GetIpByAddr(host)
	s.JsonDb.Hosts.Range(func(key, value interface{}) bool {
		v := value.(*Host)
		if v.IsClose {
			return true
		}
		//Remove http(s) http(s)://a.proxy.com
		//*.proxy.com *.a.proxy.com  Do some pan-parsing
		if v.Scheme != "all" && v.Scheme != r.URL.Scheme {
			return true
		}
		tmpHost := v.Host
		if strings.Contains(tmpHost, "*") {
			tmpHost = strings.Replace(tmpHost, "*", "", -1)
			if strings.Contains(host, tmpHost) {
				hosts = append(hosts, v)
			}
		} else if v.Host == host {
			hosts = append(hosts, v)
		}
		return true
	})

	for _, v := range hosts {
		//If not set, default matches all
		if v.Location == "" {
			v.Location = "/"
		}
		// "*" means SNI-based HTTPS lookup where actual URI is unknown, skip location filter
		if r.RequestURI == "*" {
			if h == nil || (len(v.Location) > len(h.Location)) {
				h = v
			}
			continue
		}
		if strings.Index(r.RequestURI, v.Location) == 0 {
			if h == nil || (len(v.Location) > len(h.Location)) {
				h = v
			}
		}
	}
	if h != nil {
		return
	}
	err = errors.New("The host could not be parsed")
	return
}
