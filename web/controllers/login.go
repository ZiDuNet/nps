package controllers

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"github.com/astaxie/beego/cache"
	"github.com/astaxie/beego/utils/captcha"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"ehang.io/nps/web/audit"
	"github.com/astaxie/beego"
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
	count      int
	windowFrom time.Time
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

func (s *loginAttemptLimiter) cleanupLocked(now time.Time) {
	if now.Before(s.nextCleanupTime) {
		return
	}
	s.removeExpiredLocked(now)
	s.nextCleanupTime = now.Add(loginCleanupPeriod)
}

func (s *loginAttemptLimiter) removeExpiredLocked(now time.Time) {
	for key, entry := range s.byIP {
		if entry == nil || !now.Before(entry.windowFrom.Add(loginFailureWindow)) {
			delete(s.byIP, key)
		}
	}
	for key, entry := range s.byUsername {
		if entry == nil || !now.Before(entry.windowFrom.Add(loginFailureWindow)) {
			delete(s.byUsername, key)
		}
	}
}

func (s *loginAttemptLimiter) reserve(ip, username string, now time.Time) bool {
	ip = strings.TrimSpace(ip)
	username = loginUsernameKey(username)
	if ip == "" {
		ip = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	ipEntry, hasIP := s.byIP[ip]
	userEntry, hasUsername := s.byUsername[username]
	if hasIP && now.Before(ipEntry.windowFrom.Add(loginFailureWindow)) && ipEntry.count >= maxLoginFailures {
		return false
	}
	if hasUsername && now.Before(userEntry.windowFrom.Add(loginFailureWindow)) && userEntry.count >= maxLoginFailures {
		return false
	}
	if (!hasIP && len(s.byIP) >= s.capacity) || (!hasUsername && len(s.byUsername) >= s.capacity) {
		// A full store triggers one opportunistic expiry pass before failing
		// closed. Unlike the old code this is not an every-request full scan.
		s.removeExpiredLocked(now)
		ipEntry, hasIP = s.byIP[ip]
		userEntry, hasUsername = s.byUsername[username]
		if (!hasIP && len(s.byIP) >= s.capacity) || (!hasUsername && len(s.byUsername) >= s.capacity) {
			return false
		}
	}
	if !hasIP {
		ipEntry = &loginAttemptRecord{windowFrom: now}
		s.byIP[ip] = ipEntry
	} else if !now.Before(ipEntry.windowFrom.Add(loginFailureWindow)) {
		ipEntry.count, ipEntry.windowFrom = 0, now
	}
	if !hasUsername {
		userEntry = &loginAttemptRecord{windowFrom: now}
		s.byUsername[username] = userEntry
	} else if !now.Before(userEntry.windowFrom.Add(loginFailureWindow)) {
		userEntry.count, userEntry.windowFrom = 0, now
	}
	ipEntry.count++
	userEntry.count++
	return true
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
	s.removeExpiredLocked(now)
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
	// Try login implicitly, will succeed if it's configured as no-auth(empty username&password).
	webBaseUrl := beego.AppConfig.String("web_base_url")
	if self.doLogin("", "", false) {
		self.Redirect(webBaseUrl+"/index/index", 302)
	}
	self.Data["web_base_url"] = webBaseUrl
	self.Data["register_allow"], _ = beego.AppConfig.Bool("allow_user_register")
	self.Data["captcha_open"], _ = beego.AppConfig.Bool("open_captcha")
	self.Data["totp_enabled"] = strings.TrimSpace(beego.AppConfig.String("totp_secret")) != ""
	self.Data["pow_enabled"] = beego.AppConfig.DefaultBool("force_pow", false)
	self.Data["pow_bits"] = powDifficulty()
	self.Data["version"] = version.VERSION
	self.TplName = "login/index.html"
}

func (self *LoginController) Verify() {
	if !self.requirePost() {
		return
	}
	username := self.GetString("username")
	password := self.GetString("password")
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
	if self.doLoginWithTOTP(username, password, totpCode, true) {
		appendLoginAudit(self, username, true, "")
		self.Data["json"] = map[string]interface{}{"status": 1, "msg": "login success"}
	} else {
		appendLoginAudit(self, username, false, "username or password incorrect")
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "username or password incorrect"}
	}
	self.ServeJSON()
}

// Pow returns a short-lived login challenge. It is intentionally a separate
// endpoint so the browser can compute the solution without embedding a
// server-side secret in the login page.
func (self *LoginController) Pow() {
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
	if err == nil && b && !auth {
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
	return limiter.reserve(ip, username, now)
}

// reserveLoginAttempt atomically admits at most maxLoginFailures explicit
// attempts in one window. A successful login removes the reservation again.
// Implicit no-auth checks on the login page must never count as failures.
func (self *LoginController) Register() {
	if self.Ctx.Request.Method == "GET" {
		self.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
		self.Data["version"] = version.VERSION
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
	if self.GetString("username") == "" || self.GetString("password") == "" || len(self.GetString("password")) < 6 || self.GetString("username") == beego.AppConfig.String("web_username") {
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "please check your input (password min 6 chars)"}
		self.ServeJSON()
		return
	}
	t := &file.Client{
		Id:          int(file.GetDb().JsonDb.GetClientId()),
		Status:      true,
		Cnf:         &file.Config{},
		WebUserName: self.GetString("username"),
		WebPassword: self.GetString("password"),
		Flow:        &file.Flow{},
	}
	if err := file.GetDb().NewClient(t); err != nil {
		appendAuthAudit(self, "auth.register", self.GetString("username"), false, err.Error())
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": err.Error()}
	} else {
		appendAuthAudit(self, "auth.register", self.GetString("username"), true, "")
		self.Data["json"] = map[string]interface{}{"status": 1, "msg": "register success"}
	}
	self.ServeJSON()
}

func (self *LoginController) Out() {
	appendAuthAudit(self, "auth.logout", "", true, "")
	clearAuthenticationSession(self.DelSession)
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
	self.StartSession()
	self.SessionRegenerateID()
}

func (self *LoginController) requirePost() bool {
	if self.Ctx.Request.Method != http.MethodPost {
		self.Ctx.Output.SetStatus(http.StatusMethodNotAllowed)
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "method not allowed"}
		self.ServeJSON()
		return false
	}
	if !isSameOriginRequest(self.Ctx.Request) {
		self.Ctx.Output.SetStatus(http.StatusForbidden)
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "invalid request origin"}
		self.ServeJSON()
		return false
	}
	return true
}
