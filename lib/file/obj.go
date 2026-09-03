package file

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/rate"
	"github.com/pkg/errors"
)

type Flow struct {
	ExportFlow int64
	InletFlow  int64
	FlowLimit  int64
	sync.RWMutex
	rateSampleAt  time.Time
	rateSampleIn  int64
	rateSampleOut int64
	rateIn        int64
	rateOut       int64
}

const flowRateSampleInterval = time.Second

func (s *Flow) Add(in, out int64) {
	s.Lock()
	s.InletFlow += int64(in)
	s.ExportFlow += int64(out)
	now := time.Now()
	if s.rateSampleAt.IsZero() {
		s.rateSampleAt = now
		s.rateSampleIn = s.InletFlow
		s.rateSampleOut = s.ExportFlow
	} else if elapsed := now.Sub(s.rateSampleAt); elapsed >= flowRateSampleInterval {
		deltaIn := s.InletFlow - s.rateSampleIn
		deltaOut := s.ExportFlow - s.rateSampleOut
		if deltaIn < 0 {
			deltaIn = 0
		}
		if deltaOut < 0 {
			deltaOut = 0
		}
		seconds := elapsed.Seconds()
		s.rateIn = int64(float64(deltaIn) / seconds)
		s.rateOut = int64(float64(deltaOut) / seconds)
		s.rateSampleAt = now
		s.rateSampleIn = s.InletFlow
		s.rateSampleOut = s.ExportFlow
	}
	s.Unlock()
}

// Snapshot returns a consistent view of the counters and configured limit.
// Callers that render status or enforce quotas should use this instead of
// reading the exported fields while traffic goroutines are updating them.
func (s *Flow) Snapshot() (inlet, export, limit int64) {
	if s == nil {
		return 0, 0, 0
	}
	s.RLock()
	inlet, export, limit = s.InletFlow, s.ExportFlow, s.FlowLimit
	s.RUnlock()
	return inlet, export, limit
}

// RateSnapshot returns the latest byte-rate sample. Samples are advanced by
// Add, so dashboard readers do not mutate shared baselines or interfere with
// one another. The counters remain the source of truth for persistence and
// quota enforcement; private sampling fields are intentionally not serialized.
func (s *Flow) RateSnapshot() (inRate, outRate int64) {
	if s == nil {
		return 0, 0
	}
	now := time.Now()
	s.RLock()
	sampleAt, inRate, outRate := s.rateSampleAt, s.rateIn, s.rateOut
	s.RUnlock()
	if sampleAt.IsZero() || now.Sub(sampleAt) > 2*flowRateSampleInterval {
		return 0, 0
	}
	return inRate, outRate
}

func (s *Flow) SetLimit(limit int64) {
	if s == nil {
		return
	}
	s.Lock()
	s.FlowLimit = limit
	s.Unlock()
}

func (s *Flow) Exceeded() bool {
	inlet, export, limit := s.Snapshot()
	return limit > 0 && (limit<<20) < inlet+export
}

type Config struct {
	U        string
	P        string
	Compress bool
	Crypt    bool
}

type Client struct {
	Cnf             *Config
	Id              int        //id
	UserId          int        //关联的普通用户 id
	UserName        string     `json:"-"` //关联用户名称，仅用于展示
	VerifyKey       string     //verify key
	Addr            string     //the ip of client
	LocalAddr       string     // client private/LAN addresses reported by npc
	Remark          string     //remark
	Status          bool       //is allow connect
	IsConnect       bool       //is the client connect
	RateLimit       int        //rate /kb
	Flow            *Flow      //flow setting
	Rate            *rate.Rate //rate limit
	NoStore         bool       //no store to file
	NoDisplay       bool       //no display on web
	MaxConn         int        //the max connection num of client allow
	NowConn         int32      //the connection num of now
	WebUserName     string     //the username of web login
	WebPassword     string     //the password of web login
	ConfigConnAllow bool       //is allow connected by config file
	MaxTunnelNum    int
	Version         string
	BlackIpList     []string
	CreateTime      string
	LastOnlineTime  string
	IpWhite         bool     // 是否启用ip白名单
	IpWhitePass     string   // ip授权密码
	IpWhiteList     []string // ip白名单
	ExpireTime      string   // 到期时间,留空表示永不过期,格式 2006-01-02 15:04:05
	sync.RWMutex
}

type User struct {
	Id       int
	UserName string
	Password string
	Status   bool
	Remark   string
	// MaxClientNum limits the number of clients owned by this user. A zero
	// value intentionally means unlimited so records written by older NPS
	// versions remain compatible when decoded from JSON.
	MaxClientNum int
	MaxTunnelNum int
	ExpireTime   string
	CreateTime   string
	sync.RWMutex
}

func NewClient(vKey string, noStore bool, noDisplay bool) *Client {
	return &Client{
		Cnf:       new(Config),
		Id:        0,
		VerifyKey: vKey,
		Addr:      "",
		LocalAddr: "",
		Remark:    "",
		Status:    true,
		IsConnect: false,
		RateLimit: 0,
		Flow:      new(Flow),
		Rate:      nil,
		NoStore:   noStore,
		RWMutex:   sync.RWMutex{},
		NoDisplay: noDisplay,
	}
}

func (s *Client) CutConn() {
	atomic.AddInt32(&s.NowConn, 1)
}

func (s *Client) AddConn() {
	atomic.AddInt32(&s.NowConn, -1)
}

func (s *Client) GetConn() bool {
	s.RLock()
	maxConn := int64(s.MaxConn)
	s.RUnlock()
	if maxConn <= 0 {
		s.CutConn()
		return true
	}
	for {
		current := atomic.LoadInt32(&s.NowConn)
		if int64(current) >= maxConn {
			return false
		}
		if atomic.CompareAndSwapInt32(&s.NowConn, current, current+1) {
			return true
		}
	}
}

func (s *Client) HasTunnel(t *Tunnel) (exist bool) {
	if s == nil || t == nil {
		return false
	}
	s.RLock()
	clientID := s.Id
	s.RUnlock()
	t.RLock()
	targetPort := t.Port
	t.RUnlock()
	GetDb().JsonDb.Tasks.Range(func(key, value interface{}) bool {
		v, ok := value.(*Tunnel)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		client, port := v.Client, v.Port
		v.RUnlock()
		if client == nil || targetPort == 0 {
			return true
		}
		client.RLock()
		candidateID := client.Id
		client.RUnlock()
		if candidateID == clientID && port == targetPort {
			exist = true
			return false
		}
		return true
	})
	return
}

func (s *Client) GetTunnelNum() (num int) {
	if s == nil {
		return 0
	}
	s.RLock()
	clientID := s.Id
	s.RUnlock()
	GetDb().JsonDb.Tasks.Range(func(key, value interface{}) bool {
		v, ok := value.(*Tunnel)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		client := v.Client
		v.RUnlock()
		if client == nil {
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

	GetDb().JsonDb.Hosts.Range(func(key, value interface{}) bool {
		v, ok := value.(*Host)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		client := v.Client
		v.RUnlock()
		if client == nil {
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
	return
}

func (s *Client) HasHost(h *Host) bool {
	if s == nil || h == nil {
		return false
	}
	s.RLock()
	clientID := s.Id
	s.RUnlock()
	h.RLock()
	hostName, location := h.Host, h.Location
	h.RUnlock()
	var has bool
	GetDb().JsonDb.Hosts.Range(func(key, value interface{}) bool {
		v, ok := value.(*Host)
		if !ok || v == nil {
			return true
		}
		v.RLock()
		client, candidateHost, candidateLocation := v.Client, v.Host, v.Location
		v.RUnlock()
		if client == nil {
			return true
		}
		client.RLock()
		candidateID := client.Id
		client.RUnlock()
		if candidateID == clientID && candidateHost == hostName && location == candidateLocation {
			has = true
			return false
		}
		return true
	})
	return has
}

type Tunnel struct {
	Id           int
	Port         int
	ServerIp     string
	Mode         string
	Status       bool
	RunStatus    bool
	Client       *Client
	Ports        string
	Flow         *Flow
	Password     string
	Remark       string
	TargetAddr   string
	NoStore      bool
	LocalPath    string
	StripPre     string
	ProtoVersion string
	Target       *Target
	MultiAccount *MultiAccount
	Health
	sync.RWMutex
}

type Health struct {
	HealthCheckTimeout  int
	HealthMaxFail       int
	HealthCheckInterval int
	HealthNextTime      time.Time
	HealthMap           map[string]int
	HttpHealthUrl       string
	HealthRemoveArr     []string
	HealthCheckType     string
	HealthCheckTarget   string
	sync.RWMutex
}

type Host struct {
	Id   int
	Host string //host
	// PlatformDomainID identifies a managed wildcard domain. An empty value
	// keeps historical hosts as user-managed custom domains.
	PlatformDomainID string
	HeaderChange     string //header change
	HostChange       string //host change
	Location         string //url router
	Remark           string //remark
	Scheme           string //http https all
	CertFilePath     string
	KeyFilePath      string
	NoStore          bool
	IsClose          bool
	AutoHttps        bool // 自动https
	Flow             *Flow
	Client           *Client
	Target           *Target //目标
	Health           `json:"-"`
	sync.RWMutex
}

type Target struct {
	nowIndex   int
	TargetStr  string
	TargetArr  []string
	LocalProxy bool
	sync.RWMutex
}

type MultiAccount struct {
	AccountMap map[string]string // multi account and pwd
}

func (s *Target) GetRandomTarget() (string, error) {
	s.Lock()
	if s.TargetArr == nil {
		arr := strings.Split(s.TargetStr, "\n")
		s.TargetArr = make([]string, 0, len(arr))
		for _, v := range arr {
			v = strings.TrimRight(v, "\r")
			if v != "" {
				s.TargetArr = append(s.TargetArr, v)
			}
		}
	}
	if len(s.TargetArr) == 0 {
		s.Unlock()
		return "", errors.New("all inward-bending targets are offline")
	}
	if s.nowIndex >= len(s.TargetArr)-1 {
		s.nowIndex = -1
	}
	s.nowIndex++
	addr := s.TargetArr[s.nowIndex]
	s.Unlock()
	return addr, nil
}

// PreviewTarget reports the backend that would be selected next without
// advancing the round-robin cursor. It is used by the management diagnostic
// view so inspecting a route never changes live request distribution.
func (s *Target) PreviewTarget() (string, error) {
	if s == nil {
		return "", errors.New("all inward-bending targets are offline")
	}
	s.RLock()
	targets := append([]string(nil), s.TargetArr...)
	targetStr, nextIndex := s.TargetStr, s.nowIndex+1
	s.RUnlock()

	if len(targets) == 0 {
		for _, target := range strings.Split(targetStr, "\n") {
			target = strings.TrimRight(target, "\r")
			if target != "" {
				targets = append(targets, target)
			}
		}
	}
	if len(targets) == 0 {
		return "", errors.New("all inward-bending targets are offline")
	}
	if nextIndex < 0 || nextIndex >= len(targets) {
		nextIndex = 0
	}
	return targets[nextIndex], nil
}

// PlatformDomain is an administrator-managed wildcard domain available to
// domain proxy users. ID is stable so changing the certificate paths does not
// change the relationship with existing hosts.
type PlatformDomain struct {
	ID           string
	Wildcard     string
	CertFilePath string
	KeyFilePath  string
}

type Glob struct {
	BlackIpList     []string
	ServerUrl       string
	PlatformDomains []PlatformDomain
	sync.RWMutex
}
