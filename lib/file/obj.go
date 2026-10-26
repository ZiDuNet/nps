package file

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/acl"
	"ehang.io/nps/lib/rate"
	"github.com/astaxie/beego/logs"
	"github.com/pkg/errors"
)

type Flow struct {
	ExportFlow        int64
	InletFlow         int64
	FlowLimit         int64
	LegacyTotal       int64
	AccountingVersion int
	sync.RWMutex
	rateSampleAt  time.Time
	rateSampleIn  int64
	rateSampleOut int64
	rateIn        int64
	rateOut       int64
}

const directionalFlowAccountingVersion = 2

// flowLimitActual controls the global client quota accounting mode.  It is
// configured once during server startup from flow_limit_type and kept here
// rather than importing beego into the persistence package (which would
// create an import cycle).  The default remains the historical total mode.
var flowLimitActual atomic.Bool

// SetFlowLimitType selects how client flow limits are enforced.  "actual"
// counts each byte in both directions (the effective consumption is doubled),
// while any other value keeps the legacy total accounting.
func SetFlowLimitType(value string) {
	flowLimitActual.Store(strings.EqualFold(strings.TrimSpace(value), "actual"))
}

func FlowLimitType() string {
	if flowLimitActual.Load() {
		return "actual"
	}
	return "total"
}

func NewFlow() *Flow {
	return &Flow{AccountingVersion: directionalFlowAccountingVersion}
}

// NormalizeLegacyAccounting folds counters written by versions that recorded
// every transferred byte in both fields into one aggregate bucket. The old
// direction cannot be recovered, but quota consumption stays accurate and all
// traffic written after migration uses the directional counters.
func (s *Flow) NormalizeLegacyAccounting() bool {
	if s == nil {
		return false
	}
	s.Lock()
	defer s.Unlock()
	if s.AccountingVersion >= directionalFlowAccountingVersion {
		return false
	}
	legacy := s.InletFlow
	if s.ExportFlow > legacy {
		legacy = s.ExportFlow
	}
	if legacy > 0 {
		s.LegacyTotal += legacy
		s.InletFlow = 0
		s.ExportFlow = 0
	}
	s.AccountingVersion = directionalFlowAccountingVersion
	s.rateSampleAt = time.Time{}
	s.rateSampleIn = 0
	s.rateSampleOut = 0
	s.rateIn = 0
	s.rateOut = 0
	return true
}

// Total returns real aggregate transfer volume. LegacyTotal holds the
// directionless usage migrated from releases that recorded both sides twice.
func (s *Flow) Total() int64 {
	if s == nil {
		return 0
	}
	s.RLock()
	total := s.LegacyTotal + s.InletFlow + s.ExportFlow
	s.RUnlock()
	return total
}

func (s *Flow) LegacySnapshot() int64 {
	if s == nil {
		return 0
	}
	s.RLock()
	legacy := s.LegacyTotal
	s.RUnlock()
	return legacy
}

const flowRateSampleInterval = time.Second

func (s *Flow) Add(in, out int64) {
	s.Lock()
	if s.AccountingVersion == 0 {
		s.AccountingVersion = directionalFlowAccountingVersion
	}
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
	if s == nil {
		return false
	}
	s.RLock()
	usage := s.LegacyTotal + s.InletFlow + s.ExportFlow
	if flowLimitActual.Load() {
		usage = s.LegacyTotal + 2*(s.InletFlow+s.ExportFlow)
	}
	exceeded := s.FlowLimit > 0 && (s.FlowLimit<<20) < usage
	s.RUnlock()
	return exceeded
}

type Config struct {
	U        string
	P        string
	Compress bool
	Crypt    bool
}

type Client struct {
	SourceLists       `json:"-"`
	Cnf               *Config
	Id                int        //id
	UserId            int        //关联的普通用户 id
	UserName          string     `json:"-"` //关联用户名称，仅用于展示
	VerifyKey         string     //verify key
	Addr              string     //the ip of client
	LocalAddr         string     // client private/LAN addresses reported by npc
	Remark            string     //remark
	Status            bool       //is allow connect
	IsConnect         bool       //is the client connect
	RateLimit         int        //rate /kb
	Flow              *Flow      //flow setting
	Rate              *rate.Rate //rate limit
	NoStore           bool       //no store to file
	NoDisplay         bool       //no display on web
	MaxConn           int        //the max connection num of client allow
	NowConn           int32      //the connection num of now
	WebUserName       string     //the username of web login
	WebPassword       string     //the password of web login
	ConfigConnAllow   bool       //is allow connected by config file
	MaxTunnelNum      int
	Version           string
	ConnectionHistory *ConnectionHistory `json:",omitempty"`
	CreateTime        string
	LastOnlineTime    string
	IpWhite           bool     // 是否启用ip白名单
	IpWhitePass       string   // ip授权密码
	IpWhiteList       []string // ip白名单
	// BlackIpList remains a direct field for source compatibility with older
	// embedders. The unified ACL mirrors it into SourceLists when records are
	// loaded or edited.
	BlackIpList    []string
	WhiteIpList    []string
	IpListPriority int
	// RegisterKey allows a visitor to register its source IP from the public
	// registration page.  It is intentionally separate from VerifyKey so a
	// deployment can share a registration link without exposing the client
	// control credential.
	RegisterKey      string
	RegisterMaxHours int
	// RegisteredOnly requires source addresses to have an active registration
	// for this client (or the administrator/public registration bucket).
	RegisteredOnly bool
	ExpireTime     string // 到期时间,留空表示永不过期,格式 2006-01-02 15:04:05
	sync.RWMutex
}

// LocalProxyAllowed reports whether this client may ask the server to connect
// through its own network namespace. The global switch is always required;
// user-owned clients also require the administrator's explicit tenant-level
// opt-in. Synthetic negative IDs are internal server services.
func (c *Client) LocalProxyAllowed(allowLocalProxy, allowUserLocal bool) bool {
	if c == nil {
		return false
	}
	if c.Id < 0 {
		return true
	}
	if !allowLocalProxy {
		return false
	}
	c.RLock()
	userID := c.UserId
	c.RUnlock()
	return userID <= 0 || allowUserLocal
}

type User struct {
	Id         int
	UserName   string
	Password   string
	APIKeyHash string `json:"api_key_hash,omitempty"`
	// DashboardKeyHash stores only the SHA-256 digest of the read-only
	// resource topology credential. DashboardKeyCiphertext stores the same
	// credential encrypted at rest for authorized owners who need to reopen it.
	DashboardKeyHash       string `json:"dashboard_key_hash,omitempty"`
	DashboardKeyCiphertext string `json:"dashboard_key_ciphertext,omitempty"`
	// APIKeyCiphertext stores the user API token encrypted at rest so the
	// account owner can view it again after generation. APIKeyHash remains the
	// digest used for authentication; the ciphertext is only used for display.
	APIKeyCiphertext string `json:"api_key_ciphertext,omitempty"`
	Status           bool
	Remark           string
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
		Cnf:               new(Config),
		Id:                0,
		VerifyKey:         vKey,
		Addr:              "",
		LocalAddr:         "",
		Remark:            "",
		Status:            true,
		IsConnect:         false,
		RateLimit:         0,
		Flow:              NewFlow(),
		ConnectionHistory: &ConnectionHistory{},
		Rate:              nil,
		NoStore:           noStore,
		RWMutex:           sync.RWMutex{},
		NoDisplay:         noDisplay,
	}
}

func (s *Client) CutConn() {
	atomic.AddInt32(&s.NowConn, 1)
}

func (s *Client) AddConn() {
	atomic.AddInt32(&s.NowConn, -1)
	releaseGlobalConn()
}

func (s *Client) GetConn() bool {
	s.RLock()
	maxConn := int64(s.MaxConn)
	s.RUnlock()
	if maxConn <= 0 {
		s.CutConn()
		if acquireGlobalConn() {
			return true
		}
		atomic.AddInt32(&s.NowConn, -1)
		return false
	}
	for {
		current := atomic.LoadInt32(&s.NowConn)
		if int64(current) >= maxConn {
			return false
		}
		if atomic.CompareAndSwapInt32(&s.NowConn, current, current+1) {
			if acquireGlobalConn() {
				return true
			}
			atomic.AddInt32(&s.NowConn, -1)
			return false
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
	SourceLists
	RateLimits
	Id                 int
	Port               int
	ServerIp           string
	Mode               string
	Status             bool
	RunStatus          bool
	RunError           string
	CurrentConnections int32 `json:"CurrentConnections,omitempty"`
	Client             *Client
	Ports              string
	Flow               *Flow
	// MaxConn limits concurrent connections admitted by this tunnel. Zero
	// means unlimited and matches historical records without the field.
	MaxConn  int
	Password string
	Remark   string
	// TargetType selects the protocol for secret/P2P visitors: tcp (default),
	// udp, or all. TCP targets use stream forwarding while UDP targets use
	// length-framed datagrams so packet boundaries survive the Bridge.
	TargetType   string
	TargetAddr   string
	NoStore      bool
	LocalPath    string
	StripPre     string
	ProtoVersion string
	Target       *Target
	MultiAccount *MultiAccount
	// mixProxy exposes HTTP proxy and SOCKS5 on one TCP port. These flags are
	// ignored for the separate legacy httpProxy and socks5 modes.
	HttpProxy    bool
	Socks5Proxy  bool
	DestAclMode  int    // 0=off, 1=whitelist, 2=blacklist
	DestAclRules string // one destination rule per line
	destACL      *acl.Dest
	Health
	// RegisteredOnly requires the visitor IP to be registered for the owning
	// client before a connection is opened.
	RegisteredOnly bool
	sync.RWMutex
}

const (
	AclOff       = 0
	AclWhitelist = 1
	AclBlacklist = 2
)

// SetDestinationACL validates and installs a destination policy. Web/API
// callers should use this before persisting a tunnel so invalid new rules do
// not silently change the effective policy.
func (t *Tunnel) SetDestinationACL(mode int, rules string) error {
	if t == nil {
		return errors.New("隧道记录无效")
	}
	if mode != AclOff && mode != AclWhitelist && mode != AclBlacklist {
		return errors.New("目标 ACL 模式无效")
	}
	compiled, err := acl.CompileDest(mode, rules)
	if err != nil {
		return err
	}
	t.Lock()
	t.DestAclMode = mode
	t.DestAclRules = strings.TrimSpace(strings.ReplaceAll(rules, "\r\n", "\n"))
	t.destACL = compiled
	t.Unlock()
	return nil
}

// CompileDestinationACL compiles persisted settings at startup. Invalid old
// lines are logged and disable the complete policy until the saved rules are
// corrected; silently skipping a bad rule could broaden a deny/allow policy.
func (t *Tunnel) CompileDestinationACL() {
	if t == nil {
		return
	}
	t.RLock()
	mode, rules, id := t.DestAclMode, t.DestAclRules, t.Id
	t.RUnlock()
	if mode != AclOff && mode != AclWhitelist && mode != AclBlacklist {
		mode = AclOff
	}
	compiled, err := acl.CompileDest(mode, rules)
	if err != nil {
		logs.Warn("Destination list of tunnel %d: policy disabled because validation failed: %v", id, err)
	}
	t.Lock()
	t.DestAclMode = mode
	t.destACL = compiled
	t.Unlock()
}

// AllowsDestination reports whether the configured destination ACL permits
// addr, expressed as host:port or an IP address.
func (t *Tunnel) AllowsDestination(addr string) bool {
	if t == nil {
		return true
	}
	t.RLock()
	mode, compiled := t.DestAclMode, t.destACL
	t.RUnlock()
	if mode != AclWhitelist && mode != AclBlacklist {
		return true
	}
	if compiled == nil {
		t.CompileDestinationACL()
		t.RLock()
		compiled = t.destACL
		t.RUnlock()
	}
	return compiled != nil && compiled.Allows(addr)
}

// TryAcquireConn atomically admits one connection when the tunnel has a
// configured MaxConn limit. It reuses CurrentConnections so topology views
// keep reporting the same live count. A zero or negative limit is unlimited.
func (t *Tunnel) TryAcquireConn() error {
	if t == nil {
		return nil
	}
	t.RLock()
	limit := t.MaxConn
	t.RUnlock()
	if limit <= 0 {
		return nil
	}
	for {
		current := atomic.LoadInt32(&t.CurrentConnections)
		if int(current) >= limit {
			return errors.New("隧道连接数已达上限")
		}
		if atomic.CompareAndSwapInt32(&t.CurrentConnections, current, current+1) {
			return nil
		}
	}
}

// ReleaseConn releases one connection acquired by TryAcquireConn.
func (t *Tunnel) ReleaseConn() {
	if t == nil {
		return
	}
	if atomic.AddInt32(&t.CurrentConnections, -1) < 0 {
		atomic.StoreInt32(&t.CurrentConnections, 0)
	}
}

type Health struct {
	HealthCheckTimeout  int            `json:"HealthCheckTimeout,omitempty"`
	HealthMaxFail       int            `json:"HealthMaxFail,omitempty"`
	HealthCheckInterval int            `json:"HealthCheckInterval,omitempty"`
	HealthNextTime      time.Time      `json:"-"`
	HealthMap           map[string]int `json:"-"`
	HttpHealthUrl       string         `json:"HttpHealthUrl,omitempty"`
	HealthRemoveArr     []string       `json:"-"`
	HealthCheckType     string         `json:"HealthCheckType,omitempty"`
	HealthCheckTarget   string         `json:"HealthCheckTarget,omitempty"`
	// mu is deliberately a pointer rather than an embedded sync.RWMutex.
	// Health profiles are copied when they are normalized and distributed to
	// NPCs; embedding a mutex makes those safe value snapshots trigger
	// copylocks diagnostics and invites accidental lock copying.
	mu *sync.RWMutex
}

var healthMutexInitMu sync.Mutex

func (h *Health) mutex() *sync.RWMutex {
	if h == nil {
		return nil
	}
	healthMutexInitMu.Lock()
	if h.mu == nil {
		h.mu = &sync.RWMutex{}
	}
	mu := h.mu
	healthMutexInitMu.Unlock()
	return mu
}

// HealthLock/HealthRLock and their counterparts keep the runtime lock out of
// JSON snapshots and value copies without colliding with Host/Tunnel locks.
func (h *Health) HealthLock()    { h.mutex().Lock() }
func (h *Health) HealthUnlock()  { h.mutex().Unlock() }
func (h *Health) HealthRLock()   { h.mutex().RLock() }
func (h *Health) HealthRUnlock() { h.mutex().RUnlock() }

type Host struct {
	SourceLists
	RateLimits
	Id   int
	Host string //host
	// PlatformDomainID identifies a managed wildcard domain. An empty value
	// keeps historical hosts as user-managed custom domains.
	PlatformDomainID     string
	HeaderChange         string //header change
	ResponseHeaderChange string //response header change
	HostChange           string //host change
	Location             string //url router
	PathRewrite          string //path rewrite rules
	RedirectURL          string //redirect target
	Remark               string //remark
	// Auth stores optional host-level Basic accounts as newline-separated
	// username:password pairs. It is never included in list/API projections.
	Auth         string
	Scheme       string //http https all
	CertFilePath string
	KeyFilePath  string
	NoStore      bool
	IsClose      bool
	AutoHttps    bool // 自动https
	AutoCORS     bool // add permissive CORS response headers
	CompatMode   bool // use net/http reverse proxy compatibility path
	// TargetIsHTTPS makes the server establish an inner TLS connection to the
	// private target after the public HTTP(S) request has been terminated.
	TargetIsHTTPS bool
	// HTTPSJustProxy keeps the public TLS stream opaque and forwards it to the
	// backend unchanged.
	HTTPSJustProxy bool
	// AutoSSL allows this host to participate in the optional ACME certificate
	// manager when force_auto_ssl is enabled globally.
	AutoSSL bool
	// TLSOffload forwards a terminated TLS stream as raw bytes to the backend.
	TLSOffload bool
	Flow       *Flow
	Client     *Client
	Target     *Target //目标
	Health
	// RegisteredOnly requires the visitor IP to be registered for the owning
	// client before a request is proxied.
	RegisteredOnly bool
	sync.RWMutex
}

type Target struct {
	nowIndex      int
	TargetStr     string
	TargetArr     []string
	LocalProxy    bool
	ProxyProtocol int // 0=off, 1=PROXY protocol v1, 2=PROXY protocol v2
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
	SourceLists     `json:"-"`
	BlackIpList     []string
	WhiteIpList     []string
	IpListPriority  int
	MaxConn         int
	RateLimit       int // aggregate forwarding rate in KB/s; zero means unlimited
	ServerUrl       string
	PlatformDomains []PlatformDomain
	// DashboardKeyHash is the administrator's read-only topology credential.
	DashboardKeyHash       string `json:"dashboard_key_hash,omitempty"`
	DashboardKeyCiphertext string `json:"dashboard_key_ciphertext,omitempty"`
	// AdminAPIKeyHash/AdminAPIKeyCiphertext hold the optional web-managed
	// administrator API credential. It is equivalent to auth_key in nps.conf:
	// both authorize the signed md5(auth_key + timestamp) protocol, and the
	// managed key can be rotated without touching the configuration file.
	AdminAPIKeyHash       string `json:"admin_api_key_hash,omitempty"`
	AdminAPIKeyCiphertext string `json:"admin_api_key_ciphertext,omitempty"`
	sync.RWMutex
}
