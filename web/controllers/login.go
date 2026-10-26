package controllers

import (
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"github.com/astaxie/beego/cache"
	"github.com/astaxie/beego/utils/captcha"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/ipreg"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
	beecontext "github.com/astaxie/beego/context"
	"github.com/astaxie/beego/session"
)

type LoginController struct {
	beego.Controller
}

// Login attempt state is deliberately kept in a bounded store.  The old
// implementation used a sync.Map and scanned every entry on every request;
// an attacker could therefore grow the map indefinitely and turn the cleanup
// path into request-amplified work.  The store keeps the same small API used
// by the tests and the login flow while enforcing a hard capacity and doing
// expiry cleanup at most once per interval.
var loginAttempts = newLoginAttemptLimiter(maxLoginRecords)
var powChallenges = newPoWChallengeStore(maxPoWChallenges)
var cpt *captcha.Captcha

const (
	maxLoginFailures   = 10
	loginFailureWindow = time.Minute
	maxLoginRecords    = 4096
	loginCleanupPeriod = 15 * time.Second
	maxPoWChallenges   = 4096
)

const powChallengeTTL = 2 * time.Minute

type loginAttemptRecord struct {
	key         string
	count       int
	windowFrom  time.Time
	lastAttempt time.Time
}

type loginPolicy struct {
	minInterval time.Duration
	ipWindow    time.Duration
	userWindow  time.Duration
	maxFails    int
}

func defaultLoginPolicy() loginPolicy {
	return loginPolicy{ipWindow: loginFailureWindow, userWindow: loginFailureWindow, maxFails: maxLoginFailures}
}

func configuredLoginPolicy() loginPolicy {
	policy := loginPolicy{
		minInterval: time.Duration(maxInt(beego.AppConfig.DefaultInt("login_ban_time", 5), 0)) * time.Second,
		ipWindow:    time.Duration(maxInt(beego.AppConfig.DefaultInt("login_ip_ban_time", 180), 1)) * time.Second,
		userWindow:  time.Duration(maxInt(beego.AppConfig.DefaultInt("login_user_ban_time", 3600), 1)) * time.Second,
		maxFails:    maxInt(beego.AppConfig.DefaultInt("login_max_fail_times", maxLoginFailures), 1),
	}
	return policy
}

func maxInt(value, fallback int) int {
	if value < fallback {
		return fallback
	}
	return value
}

type loginAttemptLimiter struct {
	mu              sync.Mutex
	byIP            map[string]*loginAttemptRecord
	byUsername      map[string]*loginAttemptRecord
	capacity        int
	nextCleanupTime time.Time
}

func newLoginAttemptLimiter(capacity int) *loginAttemptLimiter {
	if capacity < 1 {
		capacity = 1
	}
	return &loginAttemptLimiter{
		byIP:       make(map[string]*loginAttemptRecord),
		byUsername: make(map[string]*loginAttemptRecord),
		capacity:   capacity,
	}
}

func (s *loginAttemptLimiter) cleanupLocked(now time.Time, policy loginPolicy) {
	if now.Before(s.nextCleanupTime) {
		return
	}
	s.removeExpiredLocked(now, policy)
	s.nextCleanupTime = now.Add(loginCleanupPeriod)
}

func (s *loginAttemptLimiter) removeExpiredLocked(now time.Time, policy loginPolicy) {
	for key, entry := range s.byIP {
		if entry == nil || !now.Before(entry.windowFrom.Add(policy.ipWindow)) {
			delete(s.byIP, key)
		}
	}
	for key, entry := range s.byUsername {
		if entry == nil || !now.Before(entry.windowFrom.Add(policy.userWindow)) {
			delete(s.byUsername, key)
		}
	}
}

func (s *loginAttemptLimiter) reserve(ip, username string, now time.Time) bool {
	return s.reserveWith(ip, username, now, defaultLoginPolicy())
}

func (s *loginAttemptLimiter) reserveWith(ip, username string, now time.Time, policy loginPolicy) bool {
	ip = strings.TrimSpace(ip)
	username = loginUsernameKey(username)
	if ip == "" {
		ip = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now, policy)
	ipEntry, hasIP := s.byIP[ip]
	userEntry, hasUsername := s.byUsername[username]
	if hasIP && now.Before(ipEntry.lastAttempt.Add(policy.minInterval)) {
		return false
	}
	if hasUsername && now.Before(userEntry.lastAttempt.Add(policy.minInterval)) {
		return false
	}
	if hasIP && now.Before(ipEntry.windowFrom.Add(policy.ipWindow)) && ipEntry.count >= policy.maxFails {
		return false
	}
	if hasUsername && now.Before(userEntry.windowFrom.Add(policy.userWindow)) && userEntry.count >= policy.maxFails {
		return false
	}
	if (!hasIP && len(s.byIP) >= s.capacity) || (!hasUsername && len(s.byUsername) >= s.capacity) {
		// A full store triggers one opportunistic expiry pass before failing
		// closed. Unlike the old code this is not an every-request full scan.
		s.removeExpiredLocked(now, policy)
		ipEntry, hasIP = s.byIP[ip]
		userEntry, hasUsername = s.byUsername[username]
		if (!hasIP && len(s.byIP) >= s.capacity) || (!hasUsername && len(s.byUsername) >= s.capacity) {
			return false
		}
	}
	if !hasIP {
		ipEntry = &loginAttemptRecord{key: ip, windowFrom: now}
		s.byIP[ip] = ipEntry
	} else if !now.Before(ipEntry.windowFrom.Add(policy.ipWindow)) {
		ipEntry.count, ipEntry.windowFrom = 0, now
	}
	if !hasUsername {
		userEntry = &loginAttemptRecord{key: username, windowFrom: now}
		s.byUsername[username] = userEntry
	} else if !now.Before(userEntry.windowFrom.Add(policy.userWindow)) {
		userEntry.count, userEntry.windowFrom = 0, now
	}
	ipEntry.count++
	userEntry.count++
	ipEntry.lastAttempt = now
	userEntry.lastAttempt = now
	return true
}

// loginBanRecord is the administrator-facing snapshot of the bounded login
// protection state. Username keys are intentionally stored as a stable hash in
// memory; the UI exposes that opaque key so an administrator can still remove
// a record without revealing credentials or retaining plaintext usernames.
type loginBanRecord struct {
	Key           string `json:"key"`
	FailTimes     int    `json:"fail_times"`
	LastLoginTime string `json:"last_login_time"`
	IsBanned      bool   `json:"is_banned"`
	BanType       string `json:"ban_type"`
}

func (s *loginAttemptLimiter) banList(policy loginPolicy, now time.Time) []loginBanRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked(now, policy)
	rows := make([]loginBanRecord, 0, len(s.byIP)+len(s.byUsername))
	appendRecord := func(entry *loginAttemptRecord, kind string, window time.Duration) {
		if entry == nil {
			return
		}
		banned := now.Before(entry.lastAttempt.Add(policy.minInterval)) ||
			(entry.count >= policy.maxFails && now.Before(entry.windowFrom.Add(window)))
		rows = append(rows, loginBanRecord{
			Key:           entry.key,
			FailTimes:     entry.count,
			LastLoginTime: entry.lastAttempt.Local().Format("2006-01-02 15:04:05"),
			IsBanned:      banned,
			BanType:       kind,
		})
	}
	for _, entry := range s.byIP {
		appendRecord(entry, "ip", policy.ipWindow)
	}
	for _, entry := range s.byUsername {
		appendRecord(entry, "username", policy.userWindow)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].IsBanned != rows[j].IsBanned {
			return rows[i].IsBanned
		}
		return rows[i].LastLoginTime > rows[j].LastLoginTime
	})
	return rows
}

func (s *loginAttemptLimiter) removeBan(key string) bool {
	if s == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	if _, ok := s.byIP[key]; ok {
		delete(s.byIP, key)
		removed = true
	}
	hashed := loginUsernameKey(key)
	if _, ok := s.byUsername[hashed]; ok {
		delete(s.byUsername, hashed)
		removed = true
	}
	for hashedKey, entry := range s.byUsername {
		if entry != nil && entry.key == key {
			delete(s.byUsername, hashedKey)
			removed = true
		}
	}
	return removed
}

func (s *loginAttemptLimiter) removeAllBans() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.byIP = make(map[string]*loginAttemptRecord)
	s.byUsername = make(map[string]*loginAttemptRecord)
	s.nextCleanupTime = time.Time{}
	s.mu.Unlock()
}

func (s *loginAttemptLimiter) cleanBans(policy loginPolicy, now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.removeExpiredLocked(now, policy)
	s.nextCleanupTime = now.Add(loginCleanupPeriod)
	s.mu.Unlock()
}

func (s *loginAttemptLimiter) clear(ip, username string) {
	ip = strings.TrimSpace(ip)
	username = loginUsernameKey(username)
	s.mu.Lock()
	delete(s.byIP, ip)
	delete(s.byUsername, username)
	s.mu.Unlock()
}

func (s *loginAttemptLimiter) clearExpired(now time.Time) {
	s.mu.Lock()
	s.removeExpiredLocked(now, defaultLoginPolicy())
	s.nextCleanupTime = now.Add(loginCleanupPeriod)
	s.mu.Unlock()
}

func (s *loginAttemptLimiter) lengths() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byIP), len(s.byUsername)
}

func loginUsernameKey(username string) string {
	canonical := strings.ToLower(strings.TrimSpace(username))
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

// InitCaptcha registers the captcha route after the application configuration
// has been loaded. This keeps captcha requests under web_base_url when the
// panel is mounted behind a reverse proxy sub-path.
func InitCaptcha() {
	// use beego cache system store the captcha data
	store := cache.NewMemoryCache()
	cpt = captcha.NewWithFilter(captchaFilterPath(beego.AppConfig.String("web_base_url")), store)
}

func captchaFilterPath(webBaseURL string) string {
	base := strings.TrimSuffix(strings.TrimSpace(webBaseURL), "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return base + "/captcha/"
}

func (self *LoginController) Index() {
	setNoStore(self.Ctx)
	// Try login implicitly, will succeed if it's configured as no-auth(empty username&password).
	webBaseUrl := beego.AppConfig.String("web_base_url")
	if self.doLogin("", "", false) {
		self.Redirect(webBaseUrl+"/index/index", 302)
	}
	loginNonce := crypt.GetRandomString(16)
	self.SetSession("login_nonce", loginNonce)
	// Cookie-backed sessions must be released while the response headers are
	// still writable. Beego's deferred release runs after template rendering,
	// at which point the response may already have started and the nonce cookie
	// would be dropped. Without this explicit release the next login attempt
	// cannot validate the page nonce and is reported as expired.
	self.releaseCookieSession()
	publicKey, _ := crypt.GetRSAPublicKeyPEM()
	self.Data["web_base_url"] = webBaseUrl
	self.Data["register_allow"], _ = beego.AppConfig.Bool("allow_user_register")
	self.Data["captcha_open"], _ = beego.AppConfig.Bool("open_captcha")
	self.Data["totp_enabled"] = strings.TrimSpace(beego.AppConfig.String("totp_secret")) != ""
	self.Data["pow_enabled"] = beego.AppConfig.DefaultBool("force_pow", false)
	self.Data["pow_bits"] = powDifficulty()
	self.Data["login_nonce"] = loginNonce
	self.Data["public_key"] = publicKey
	self.Data["login_max_skew"] = configuredLoginMaxSkew()
	// Keep the public entry point discoverable even before an administrator has
	// configured a public registration key. The page explains the disabled
	// state instead of making the feature appear to be missing entirely.
	self.Data["ip_register_enabled"] = true
	self.Data["ip_registration_configured"] = ipRegistrationConfigured()
	self.Data["version"] = version.VERSION
	self.TplName = "login/index.html"
}

// IPRegister is a public, short-lived IP registration page.  It deliberately
// lives on the login controller so it does not require an authenticated panel
// session; the submitted registration key is the only credential needed.
func (self *LoginController) IPRegister() {
	setNoStore(self.Ctx)
	webBaseURL := beego.AppConfig.String("web_base_url")
	if self.Ctx.Request.Method == http.MethodGet {
		visitorIP, visitorSource := requestVisitorIPDetails(self.Ctx.Request)
		self.Data["web_base_url"] = webBaseURL
		self.Data["version"] = version.VERSION
		self.Data["ip"] = visitorIP
		self.Data["ip_source"] = visitorSource
		self.Data["ip_proxy_warning"] = isProxyVisibleIP(visitorIP)
		self.Data["default_hours"] = registrationDefaultHours()
		self.Data["ip_registration_configured"] = ipRegistrationConfigured()
		self.Data["login_url"] = webBaseURL + "/login/index"
		self.TplName = "login/ipregister.html"
		return
	}
	if !self.requirePost() {
		return
	}
	hours, _ := strconv.Atoi(strings.TrimSpace(self.GetString("hours")))
	self.Data["json"] = registerVisitorIP(self.GetString("key"), hours, requestVisitorIP(self.Ctx))
	self.ServeJSON()
}

// IPRegisterStandaloneHandler returns a small public-only HTTP handler for
// deployments that want the IP registration page on its own listener instead
// of exposing the full management panel. It uses the same trusted-proxy rules
// as the management-panel endpoint; by default it still trusts only the TCP
// peer address on that listener.
func IPRegisterStandaloneHandler() http.Handler {
	mux := http.NewServeMux()
	page := template.Must(template.New("ip-register-standalone").Parse(ipRegisterStandaloneHTML))
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/ip/register" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			remoteIP, remoteSource := requestVisitorIPDetails(r)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if err := page.Execute(w, struct {
				IP           string
				IPSource     string
				ProxyWarning bool
				DefaultHours int
			}{IP: remoteIP, IPSource: remoteSource, ProxyWarning: isProxyVisibleIP(remoteIP), DefaultHours: registrationDefaultHours()}); err != nil {
				http.Error(w, "页面暂时无法显示", http.StatusInternalServerError)
			}
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "请求内容无效", http.StatusBadRequest)
			return
		}
		remoteIP := requestVisitorIPFromRequest(r)
		hours, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("hours")))
		result := registerVisitorIP(r.Form.Get("key"), hours, remoteIP)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	}
	mux.HandleFunc("/", handler)
	return mux
}

const ipRegisterStandaloneHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>IP 地址登记</title><style>
:root{color-scheme:light;--ink:#182230;--muted:#687386;--line:#dce3ec;--accent:#1769e0;--bg:#f3f6fb}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(ellipse at 15% 5%,#e1edff,transparent 42%),var(--bg);color:var(--ink);font:15px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif}
.card{width:min(100%,440px);padding:32px;border:1px solid #fff;border-radius:20px;background:#ffffffed;box-shadow:0 20px 60px #19345516}.eyebrow{margin:0 0 8px;color:var(--accent);font-size:12px;font-weight:700;letter-spacing:.12em;text-transform:uppercase}h1{margin:0;font-size:25px;letter-spacing:-.03em}.intro{margin:8px 0 24px;color:var(--muted)}label{display:block;margin:17px 0 7px;font-size:13px;font-weight:650}input{width:100%;height:44px;padding:0 12px;border:1px solid var(--line);border-radius:10px;background:#fff;color:var(--ink);font:inherit}input[readonly]{background:#f5f7fa;color:var(--muted)}button{width:100%;height:46px;margin-top:22px;border:0;border-radius:10px;background:var(--accent);color:#fff;font:inherit;font-weight:700;cursor:pointer}button:disabled{opacity:.65;cursor:wait}.hint{margin:14px 0 0;color:var(--muted);font-size:12px}.result{min-height:24px;margin-top:14px;font-size:13px}.result.error{color:#b42318}.result.success{color:#147a45}
.warn{margin:0 0 20px;padding:10px 12px;border:1px solid #f2c14e;border-radius:10px;background:#fff7e0;color:#8a5b00;font-size:12.5px;line-height:1.55}
</style></head><body><main class="card"><p class="eyebrow">NPS · ACCESS</p><h1>登记访问 IP</h1><p class="intro">登记后，管理员启用“仅允许已登记 IP”的资源才会接受此地址的访问。</p>
{{if .ProxyWarning}}<p class="warn">检测到的是代理或内网地址（{{.IP}}）。若此页面经过反向代理，登记后所有经该代理的访问者都会被当成同一地址，“仅允许已登记 IP”无法区分单个用户。请让代理覆盖传递 X-Real-IP / X-Forwarded-For，并确认服务端已开启 allow_x_real_ip（默认开启）后重新打开本页。</p>{{end}}
<form id="register-form"><label for="visitor-ip">当前访问地址</label><input id="visitor-ip" value="{{.IP}}" readonly><p class="hint">地址来源：{{.IPSource}}。仅当服务端允许真实 IP 头（allow_x_real_ip，默认开启）且前置代理覆盖传递时，才会显示反代传递的公网 IP。</p>
<label for="register-key">登记密钥</label><input id="register-key" name="key" type="password" autocomplete="off" required placeholder="输入管理员或客户端提供的登记密钥">
<label for="register-hours">有效时长（小时）</label><input id="register-hours" name="hours" type="number" min="1" max="{{.DefaultHours}}" value="{{.DefaultHours}}">
<button id="register-submit" type="submit">登记此 IP</button><p class="hint">登记有效时长受服务端配置限制。此页不会保存密钥。</p><div id="register-result" class="result" role="status" aria-live="polite"></div></form></main>
<script>(function(){var form=document.getElementById('register-form'),button=document.getElementById('register-submit'),result=document.getElementById('register-result');form.addEventListener('submit',function(event){event.preventDefault();button.disabled=true;result.className='result';result.textContent='正在登记…';fetch('/ip/register',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded;charset=UTF-8'},body:new URLSearchParams(new FormData(form))}).then(function(response){if(!response.ok)throw new Error('请求失败（HTTP '+response.status+'）');return response.json()}).then(function(data){result.className='result '+(data.status?'success':'error');result.textContent=data.status?'登记成功：'+data.ip+'，有效至 '+data.until:(data.msg||'登记失败')}).catch(function(error){result.className='result error';result.textContent=error.message||'登记请求失败'}).finally(function(){button.disabled=false})})})();</script></body></html>`

func registerVisitorIP(key string, hours int, visitorIP string) map[string]interface{} {
	ip, validIP := ipreg.ParseIP(strings.TrimSpace(visitorIP))
	if !validIP {
		return map[string]interface{}{"status": 0, "msg": "无法识别访问者 IP"}
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return map[string]interface{}{"status": 0, "msg": "登记密钥不能为空"}
	}
	clientID, limit, ok := registrationOwner(key)
	if !ok {
		return map[string]interface{}{"status": 0, "msg": "登记密钥无效"}
	}
	if hours <= 0 || hours > limit {
		hours = limit
	}
	until := ipreg.Register(clientID, ip, time.Duration(hours)*time.Hour)
	if until.IsZero() {
		return map[string]interface{}{"status": 0, "msg": "IP 地址无效"}
	}
	return map[string]interface{}{
		"status":    1,
		"msg":       "IP 登记成功",
		"ip":        ip.String(),
		"until":     until.Format(time.DateTime),
		"client_id": clientID,
	}
}

func ipRegistrationConfigured() bool {
	if strings.TrimSpace(beego.AppConfig.String("public_register_key")) != "" || strings.TrimSpace(beego.AppConfig.String("public_vkey")) != "" {
		return true
	}
	found := false
	file.GetDb().JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*file.Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		found = strings.TrimSpace(client.RegisterKey) != "" || strings.TrimSpace(client.VerifyKey) != ""
		client.RUnlock()
		return !found
	})
	return found
}

func registrationDefaultHours() int {
	hours := beego.AppConfig.DefaultInt("ip_register_max_hours", 2)
	if hours <= 0 {
		return 2
	}
	if hours > 8760 {
		return 8760
	}
	return hours
}

// registrationOwner validates the public key or a client registration/verify
// key and returns the registration bucket plus its maximum duration.
func registrationOwner(key string) (int, int, bool) {
	defaultHours := registrationDefaultHours()
	for _, publicKey := range []string{beego.AppConfig.String("public_register_key"), beego.AppConfig.String("public_vkey")} {
		publicKey = strings.TrimSpace(publicKey)
		if publicKey != "" && subtle.ConstantTimeCompare([]byte(publicKey), []byte(key)) == 1 {
			return ipreg.AnyClient, defaultHours, true
		}
	}
	var ownerID, ownerLimit int
	file.GetDb().JsonDb.Clients.Range(func(_, value interface{}) bool {
		client, ok := value.(*file.Client)
		if !ok || client == nil {
			return true
		}
		client.RLock()
		matches := client.Status && !client.NoDisplay &&
			((client.RegisterKey != "" && subtle.ConstantTimeCompare([]byte(client.RegisterKey), []byte(key)) == 1) ||
				(client.VerifyKey != "" && subtle.ConstantTimeCompare([]byte(client.VerifyKey), []byte(key)) == 1))
		ownerID = client.Id
		ownerLimit = client.RegisterMaxHours
		client.RUnlock()
		if !matches {
			ownerID, ownerLimit = 0, 0
		}
		return !matches
	})
	if ownerID == 0 {
		return 0, 0, false
	}
	if ownerLimit <= 0 || ownerLimit > 8760 {
		ownerLimit = defaultHours
	}
	return ownerID, ownerLimit, true
}

func requestVisitorIP(ctx *beecontext.Context) string {
	if ctx == nil || ctx.Request == nil {
		return ""
	}
	return requestVisitorIPFromRequest(ctx.Request)
}

// requestVisitorIPFromRequest returns the address used by IP registration.
// Forwarded headers are enabled by default through allow_x_real_ip. The
// setting targets the common Nginx/Docker deployment and therefore does not
// require a second trusted-proxy list; set it to false when the panel can be
// reached directly and clients must not be able to spoof these headers.
// X-NPS-Http-Only remains an explicit per-request override for deployments
// that cannot change the global setting.
func requestVisitorIPFromRequest(request *http.Request) string {
	ip, _ := requestVisitorIPDetails(request)
	return ip
}

func requestVisitorIPDetails(request *http.Request) (string, string) {
	if request == nil {
		return "", ""
	}
	peer := ""
	if parsed, ok := ipreg.ParseIP(strings.TrimSpace(request.RemoteAddr)); ok {
		peer = parsed.String()
	}
	if peer == "" {
		return "", "无法识别 TCP 对端地址"
	}
	if !requestMayUseForwardedIP(request) {
		return peer, "NPS 直接看到的 TCP 对端地址"
	}
	effective := common.EffectiveHTTPClientIP(
		request.RemoteAddr,
		request.Header.Get("X-Real-IP"),
		request.Header.Get("X-Forwarded-For"),
		true,
	)
	for _, candidate := range []struct {
		value  string
		source string
	}{
		{value: request.Header.Get("X-Real-IP"), source: "可信反代的 X-Real-IP"},
		{value: firstHeaderValue(request.Header.Get("X-Forwarded-For")), source: "可信反代的 X-Forwarded-For"},
	} {
		if parsed, ok := ipreg.ParseIP(strings.TrimSpace(candidate.value)); ok {
			if parsed.String() == effective {
				return effective, candidate.source
			}
		}
	}
	return peer, "可信反代未提供有效真实 IP，回退到 TCP 对端地址"
}

// isProxyVisibleIP reports whether the address shown on the registration page
// is a loopback, private or otherwise non-public address.  Such an address
// almost always means the page sits behind a reverse proxy or NAT: every
// visitor routed through that hop is indistinguishable, so registering it
// cannot provide per-visitor access control and the page must say so.
func isProxyVisibleIP(value string) bool {
	addr, ok := ipreg.ParseIP(strings.TrimSpace(value))
	if !ok || !addr.IsValid() {
		return false
	}
	return addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast()
}

func requestMayUseForwardedIP(request *http.Request) bool {
	if request == nil {
		return false
	}
	if beego.AppConfig.DefaultBool("allow_x_real_ip", true) {
		return true
	}
	secret := strings.TrimSpace(beego.AppConfig.String("x_nps_http_only"))
	provided := strings.TrimSpace(request.Header.Get("X-NPS-Http-Only"))
	return secret != "" && provided != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(provided)) == 1
}

func (self *LoginController) Verify() {
	setNoStore(self.Ctx)
	if !self.requirePost() {
		return
	}
	maxBody := int64(beego.AppConfig.DefaultInt("login_max_body", 1024))
	if maxBody <= 0 {
		maxBody = 1024
	}
	if self.Ctx != nil && self.Ctx.Request != nil {
		if self.Ctx.Request.ContentLength > maxBody {
			self.Ctx.Output.SetStatus(http.StatusRequestEntityTooLarge)
			self.Data["json"] = map[string]interface{}{"status": 0, "msg": "登录请求过大"}
			self.ServeJSON()
			return
		}
		self.Ctx.Request.Body = http.MaxBytesReader(self.Ctx.ResponseWriter, self.Ctx.Request.Body, maxBody)
		if err := self.Ctx.Request.ParseForm(); err != nil {
			self.Ctx.Output.SetStatus(http.StatusRequestEntityTooLarge)
			self.Data["json"] = map[string]interface{}{"status": 0, "msg": "登录请求过大"}
			self.ServeJSON()
			return
		}
	}
	username := self.GetString("username")
	rawPassword := self.GetString("password")
	password := rawPassword
	totpCode := strings.TrimSpace(self.GetString("totp"))
	if beego.AppConfig.DefaultBool("force_pow", false) && !verifyLoginPoW(username, self.GetString("pow_nonce"), self.GetString("pow_solution")) {
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "工作量证明校验失败，请重试"}
		self.ServeJSON()
		return
	}
	captchaOpen, _ := beego.AppConfig.Bool("open_captcha")
	if captchaOpen {
		if !cpt.VerifyReq(self.Ctx.Request) {
			self.Data["json"] = map[string]interface{}{"status": 0, "msg": "the verification code is wrong, please get it again and try again"}
			self.ServeJSON()
			return
		}
	}
	if payload, err := crypt.ParseLoginPayload(rawPassword); err == nil {
		if !self.verifyLoginPayload(payload) {
			self.Data["json"] = self.loginPayloadError("登录请求已过期，请刷新页面")
			self.releaseCookieSession()
			self.ServeJSON()
			return
		}
		password = payload.Password
	}
	if self.doLoginWithTOTP(username, password, totpCode, true) {
		appendLoginAudit(self, username, true, "")
		self.Data["json"] = map[string]interface{}{"status": 1, "msg": "login success"}
	} else {
		appendLoginAudit(self, username, false, "username or password incorrect")
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "username or password incorrect"}
	}
	self.ServeJSON()
}

// configuredLoginMaxSkew returns the browser login timestamp tolerance in
// milliseconds. A non-positive value disables the timestamp check while the
// encrypted nonce envelope remains accepted for compatibility.
func configuredLoginMaxSkew() time.Duration {
	value := beego.AppConfig.DefaultInt("login_max_skew", 300000)
	if value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Millisecond
}

func (self *LoginController) verifyLoginPayload(payload *crypt.LoginPayload) bool {
	if payload == nil || strings.TrimSpace(payload.Nonce) == "" || payload.Password == "" || payload.Timestamp <= 0 {
		return false
	}
	storedNonce, _ := self.GetSession("login_nonce").(string)
	if storedNonce == "" || subtle.ConstantTimeCompare([]byte(storedNonce), []byte(payload.Nonce)) != 1 {
		return false
	}
	maxSkew := configuredLoginMaxSkew()
	if maxSkew > 0 {
		now := time.Now()
		stamp := time.UnixMilli(payload.Timestamp)
		if stamp.Before(now.Add(-maxSkew)) || stamp.After(now.Add(maxSkew)) {
			return false
		}
	}
	return true
}

func (self *LoginController) loginPayloadError(message string) map[string]interface{} {
	nonce := crypt.GetRandomString(16)
	self.SetSession("login_nonce", nonce)
	publicKey, _ := crypt.GetRSAPublicKeyPEM()
	return map[string]interface{}{
		"status": 0,
		"msg":    message,
		"nonce":  nonce,
		"cert":   publicKey,
	}
}

// Pow returns a short-lived login challenge. It is intentionally a separate
// endpoint so the browser can compute the solution without embedding a
// server-side secret in the login page.
func (self *LoginController) Pow() {
	setNoStore(self.Ctx)
	if self.Ctx.Request.Method != http.MethodGet {
		self.Ctx.Output.SetStatus(http.StatusMethodNotAllowed)
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "method not allowed"}
		self.ServeJSON()
		return
	}
	if !beego.AppConfig.DefaultBool("force_pow", false) {
		self.Data["json"] = map[string]interface{}{"status": 1, "enabled": false}
		self.ServeJSON()
		return
	}
	nonce, err := newPoWNonce()
	if err != nil {
		self.Ctx.Output.SetStatus(http.StatusInternalServerError)
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "生成登录挑战失败"}
		self.ServeJSON()
		return
	}
	now := time.Now()
	if !powChallenges.store(nonce, now.Add(powChallengeTTL), now) {
		self.Ctx.Output.SetStatus(http.StatusServiceUnavailable)
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "登录校验繁忙，请稍后重试"}
		self.ServeJSON()
		return
	}
	self.Data["json"] = map[string]interface{}{"status": 1, "enabled": true, "nonce": nonce, "bits": powDifficulty()}
	self.ServeJSON()
}

type powChallengeStore struct {
	mu              sync.Mutex
	entries         map[string]time.Time
	capacity        int
	nextCleanupTime time.Time
}

func newPoWChallengeStore(capacity int) *powChallengeStore {
	if capacity < 1 {
		capacity = 1
	}
	return &powChallengeStore{
		entries:  make(map[string]time.Time),
		capacity: capacity,
	}
}

func (s *powChallengeStore) store(nonce string, expires, now time.Time) bool {
	if nonce == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !now.Before(s.nextCleanupTime) || len(s.entries) >= s.capacity {
		s.clearExpiredLocked(now)
		s.nextCleanupTime = now.Add(15 * time.Second)
	}
	if _, exists := s.entries[nonce]; !exists && len(s.entries) >= s.capacity {
		return false
	}
	s.entries[nonce] = expires
	return true
}

func (s *powChallengeStore) consume(nonce string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, exists := s.entries[nonce]
	delete(s.entries, nonce)
	return exists && now.Before(expires)
}

func (s *powChallengeStore) clearExpired(now time.Time) {
	s.mu.Lock()
	s.clearExpiredLocked(now)
	s.nextCleanupTime = now.Add(15 * time.Second)
	s.mu.Unlock()
}

func (s *powChallengeStore) clearExpiredLocked(now time.Time) {
	for nonce, expires := range s.entries {
		if !now.Before(expires) {
			delete(s.entries, nonce)
		}
	}
}

func (s *powChallengeStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func newPoWNonce() (string, error) {
	var raw [16]byte
	if _, err := cryptoRandRead(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

var cryptoRandRead = func(p []byte) (int, error) {
	return crand.Read(p)
}

func powDifficulty() int {
	bits := beego.AppConfig.DefaultInt("pow_bits", 20)
	if bits < 1 {
		return 1
	}
	if bits > 248 {
		return 248
	}
	return bits
}

func verifyLoginPoW(_ string, nonce, solution string) bool {
	nonce, solution = strings.TrimSpace(nonce), strings.TrimSpace(solution)
	if nonce == "" || solution == "" || len(solution) > 64 {
		return false
	}
	if !powChallenges.consume(nonce, time.Now()) {
		return false
	}
	digest := sha256.Sum256([]byte(nonce + "\x00" + solution))
	remaining := powDifficulty()
	for _, b := range digest[:] {
		if remaining >= 8 {
			if b != 0 {
				return false
			}
			remaining -= 8
			continue
		}
		if remaining == 0 {
			return true
		}
		return b&(0xff<<(8-remaining)) == 0
	}
	return true
}

func clearExpiredPoWChallenges(now time.Time) {
	powChallenges.clearExpired(now)
}

func (self *LoginController) doLogin(username, password string, explicit bool) bool {
	return self.doLoginWithTOTP(username, password, "", explicit)
}

func (self *LoginController) doLoginWithTOTP(username, password, totpCode string, explicit bool) bool {
	ip := loginAttemptIP(self.Ctx.Request.RemoteAddr)
	if !allowLoginAttempt(ip, username, explicit, time.Now()) {
		return false
	}
	var auth bool
	if password == beego.AppConfig.String("web_password") && username == beego.AppConfig.String("web_username") {
		if secret := strings.TrimSpace(beego.AppConfig.String("totp_secret")); secret != "" {
			valid, err := crypt.ValidateTOTPCode(secret, totpCode)
			if err != nil || !valid {
				return false
			}
		}
		self.regenerateSession()
		clearAuthenticationSession(self.DelSession)
		self.SetSession("isAdmin", true)
		auth = true
		server.Bridge.Register.Store(common.GetIpByAddr(self.Ctx.Input.IP()), time.Now().Add(time.Hour*time.Duration(2)))
	}
	b, err := beego.AppConfig.Bool("allow_user_login")
	if err == nil && b && beego.AppConfig.DefaultBool("allow_user_vkey_login", true) && !auth {
		if user, err := file.GetDb().GetUserByName(username); err == nil {
			user.RLock()
			userID, userName, userPassword := user.Id, user.UserName, user.Password
			user.RUnlock()
			if file.GetDb().IsUserActive(userID) && userPassword == password {
				auth = true
				self.regenerateSession()
				clearAuthenticationSession(self.DelSession)
				self.SetSession("isAdmin", false)
				self.SetSession(sessionPrincipalKey, sessionPrincipalUser)
				self.SetSession("userId", userID)
				self.SetSession("username", userName)
			}
		}
	}
	if err == nil && b && !auth {
		file.GetDb().JsonDb.Clients.Range(func(key, value interface{}) bool {
			v, ok := value.(*file.Client)
			if !ok || v == nil {
				return true
			}
			v.RLock()
			status, noDisplay, userID := v.Status, v.NoDisplay, v.UserId
			webUserName, webPassword, verifyKey, clientID := v.WebUserName, v.WebPassword, v.VerifyKey, v.Id
			v.RUnlock()
			if !status || noDisplay || (userID != 0 && !file.GetDb().IsUserActive(userID)) {
				return true
			}
			if webUserName == "" && webPassword == "" {
				if username != "user" || verifyKey != password {
					return true
				} else {
					auth = true
				}
			}
			if !auth && webPassword == password && webUserName == username {
				auth = true
			}
			if auth {
				self.regenerateSession()
				clearAuthenticationSession(self.DelSession)
				self.SetSession("isAdmin", false)
				self.SetSession(sessionPrincipalKey, sessionPrincipalClient)
				self.SetSession("clientId", clientID)
				self.SetSession("clientIds", map[int]struct{}{clientID: {}})
				self.SetSession("username", webUserName)
				return false
			}
			return true
		})
	}
	if auth {
		self.SetSession("auth", true)
		// Cookie sessions are serialized by Beego's deferred router hook.  That
		// hook runs after ServeJSON/Redirect has already started the response, so
		// the Set-Cookie header would be discarded.  Persist the authenticated
		// cookie while headers are still writable; the deferred release remains
		// harmless and keeps other providers unchanged.
		self.releaseCookieSession()
		if explicit {
			loginAttempts.clear(ip, username)
		}
		return true

	}
	return false
}

func loginAttemptIP(remoteAddr string) string {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && ip != "" {
		return ip
	}
	return remoteAddr
}

func allowLoginAttempt(ip, username string, explicit bool, now time.Time) bool {
	return allowLoginAttemptWith(loginAttempts, ip, username, explicit, now)
}

func allowLoginAttemptWith(limiter *loginAttemptLimiter, ip, username string, explicit bool, now time.Time) bool {
	if !explicit {
		return true
	}
	return limiter.reserveWith(ip, username, now, configuredLoginPolicy())
}

func currentLoginBanList() []loginBanRecord {
	return loginAttempts.banList(configuredLoginPolicy(), time.Now())
}

func removeLoginBan(key string) bool {
	return loginAttempts.removeBan(key)
}

func removeAllLoginBans() {
	loginAttempts.removeAllBans()
}

func cleanLoginBans() {
	loginAttempts.cleanBans(configuredLoginPolicy(), time.Now())
}

// reserveLoginAttempt atomically admits at most maxLoginFailures explicit
// attempts in one window. A successful login removes the reservation again.
// Implicit no-auth checks on the login page must never count as failures.
func (self *LoginController) Register() {
	setNoStore(self.Ctx)
	if self.Ctx.Request.Method == "GET" {
		self.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
		self.Data["version"] = version.VERSION
		self.Data["login_nonce"] = crypt.GetRandomString(16)
		self.SetSession("login_nonce", self.Data["login_nonce"])
		self.releaseCookieSession()
		self.Data["public_key"], _ = crypt.GetRSAPublicKeyPEM()
		self.TplName = "login/register.html"
		return
	}
	if !self.requirePost() {
		return
	}
	if b, err := beego.AppConfig.Bool("allow_user_register"); err != nil || !b {
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "register is not allow"}
		self.ServeJSON()
		return
	}
	username := self.GetString("username")
	rawPassword := self.GetString("password")
	password := rawPassword
	if payload, err := crypt.ParseLoginPayload(rawPassword); err == nil {
		if !self.verifyLoginPayload(payload) {
			self.Data["json"] = self.loginPayloadError("注册请求已过期，请刷新页面")
			self.releaseCookieSession()
			self.ServeJSON()
			return
		}
		password = payload.Password
	}
	if username == "" || password == "" || len(password) < 6 || username == beego.AppConfig.String("web_username") {
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "please check your input (password min 6 chars)"}
		self.ServeJSON()
		return
	}
	t := &file.Client{
		Id:          int(file.GetDb().JsonDb.GetClientId()),
		Status:      true,
		Cnf:         &file.Config{},
		WebUserName: username,
		WebPassword: password,
		Flow:        &file.Flow{},
	}
	if err := file.GetDb().NewClient(t); err != nil {
		appendAuthAudit(self, "auth.register", username, false, err.Error())
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": err.Error()}
	} else {
		appendAuthAudit(self, "auth.register", username, true, "")
		self.Data["json"] = map[string]interface{}{"status": 1, "msg": "register success"}
	}
	self.ServeJSON()
}

func (self *LoginController) Out() {
	setNoStore(self.Ctx)
	appendAuthAudit(self, "auth.logout", "", true, "")
	clearAuthenticationSession(self.DelSession)
	self.releaseCookieSession()
	self.Redirect(beego.AppConfig.String("web_base_url")+"/login/index", 302)
}

func appendLoginAudit(controller *LoginController, username string, success bool, failure string) {
	appendAuthAudit(controller, "auth.login", username, success, failure)
}

func appendAuthAudit(controller *LoginController, action, username string, success bool, failure string) {
	audit.Configure(beego.AppConfig.String("audit_log_path"))
	result := audit.ResultSuccess
	if !success {
		result = audit.ResultFailure
	}
	ip := ""
	method, path := "", ""
	if controller != nil && controller.Ctx != nil && controller.Ctx.Request != nil {
		ip = controller.Ctx.Input.IP()
		method = controller.Ctx.Request.Method
		path = controller.Ctx.Request.URL.Path
	}
	_ = audit.Append(audit.Event{
		ActorType: "anonymous", ActorName: username, SourceIP: ip,
		Method: method, Path: path, Action: action, ResourceType: "session",
		Result: result, Error: failure,
	})
}

// regenerateSession prevents a session identifier supplied before login from
// being promoted to an authenticated session. Beego's test helpers may invoke
// doLogin without initializing the global manager, so retain a no-op fallback
// for that environment.
func (self *LoginController) regenerateSession() {
	if beego.GlobalSessions == nil {
		return
	}

	// Beego's cookie provider does not implement SessionRegenerate: it returns
	// a nil store because cookie sessions do not have server-side identifiers.
	// Calling Controller.SessionRegenerateID with that provider therefore clears
	// CruSession and the first SetSession/DelSession after a successful login
	// panics.  A cookie session is already authenticated by its signed/encrypted
	// contents, so flushing the pre-login values and releasing the store again
	// after authentication provides the equivalent rotation.
	store := self.StartSession()
	if _, isCookieStore := store.(*session.CookieSessionStore); isCookieStore {
		_ = store.Flush()
		return
	}
	if self.Ctx == nil {
		return
	}

	self.SessionRegenerateID()
	if self.CruSession == nil {
		// Custom providers are allowed to return a nil store from
		// SessionRegenerate. Keep the existing store as a safe fallback instead
		// of allowing the subsequent authentication writes to panic.
		if store != nil {
			_ = store.Flush()
			self.CruSession = store
			self.Ctx.Input.CruSession = store
		}
	}
}

// releaseCookieSession writes cookie-backed session state before a controller
// action emits its body or redirect. Beego's router defers SessionRelease,
// which is too late for Set-Cookie once the response has started.
func (self *LoginController) releaseCookieSession() {
	if self == nil || self.CruSession == nil || self.Ctx == nil || self.Ctx.ResponseWriter == nil {
		return
	}
	if _, ok := self.CruSession.(*session.CookieSessionStore); !ok {
		return
	}
	self.CruSession.SessionRelease(self.Ctx.ResponseWriter)
}

func (self *LoginController) requirePost() bool {
	if self.Ctx.Request.Method != http.MethodPost {
		setNoStore(self.Ctx)
		self.Ctx.Output.SetStatus(http.StatusMethodNotAllowed)
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "method not allowed"}
		self.ServeJSON()
		return false
	}
	return true
}

// setNoStore marks authentication pages and responses as private, dynamic
// content. A cached login page can replay an old nonce after a restart or
// across users, which surfaces to the browser as "登录请求已过期" and is
// especially common when the panel is behind Nginx/CDN caching.
func setNoStore(ctx *beecontext.Context) {
	if ctx == nil || ctx.ResponseWriter == nil {
		return
	}
	header := ctx.ResponseWriter.Header()
	header.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
}
