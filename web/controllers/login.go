package controllers

import (
	"github.com/astaxie/beego/cache"
	"github.com/astaxie/beego/utils/captcha"
	"net"
	"net/http"
	"sync"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"github.com/astaxie/beego"
)

type LoginController struct {
	beego.Controller
}

var ipRecord sync.Map
var cpt *captcha.Captcha

const (
	maxLoginFailures   = 10
	loginFailureWindow = time.Minute
)

type record struct {
	mu                sync.Mutex
	hasLoginFailTimes int
	lastLoginTime     time.Time
}

func init() {
	// use beego cache system store the captcha data
	store := cache.NewMemoryCache()
	cpt = captcha.NewWithFilter("/captcha/", store)
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
	self.Data["version"] = version.VERSION
	self.TplName = "login/index.html"
}

func (self *LoginController) Verify() {
	if !self.requirePost() {
		return
	}
	username := self.GetString("username")
	password := self.GetString("password")
	captchaOpen, _ := beego.AppConfig.Bool("open_captcha")
	if captchaOpen {
		if !cpt.VerifyReq(self.Ctx.Request) {
			self.Data["json"] = map[string]interface{}{"status": 0, "msg": "the verification code is wrong, please get it again and try again"}
			self.ServeJSON()
			return
		}
	}
	if self.doLogin(username, password, true) {
		self.Data["json"] = map[string]interface{}{"status": 1, "msg": "login success"}
	} else {
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": "username or password incorrect"}
	}
	self.ServeJSON()
}

func (self *LoginController) doLogin(username, password string, explicit bool) bool {
	ip := loginAttemptIP(self.Ctx.Request.RemoteAddr)
	if !allowLoginAttempt(ip, explicit, time.Now()) {
		return false
	}
	var auth bool
	if password == beego.AppConfig.String("web_password") && username == beego.AppConfig.String("web_username") {
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
			ipRecord.Delete(ip)
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

func allowLoginAttempt(ip string, explicit bool, now time.Time) bool {
	if !explicit {
		return true
	}
	clearIprecord()
	return reserveLoginAttempt(ip, now)
}

// reserveLoginAttempt atomically admits at most maxLoginFailures explicit
// attempts in one window. A successful login removes the reservation again.
// Implicit no-auth checks on the login page must never count as failures.
func reserveLoginAttempt(ip string, now time.Time) bool {
	v, _ := ipRecord.LoadOrStore(ip, &record{})
	return v.(*record).reserve(now)
}

func (r *record) reserve(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.lastLoginTime.IsZero() && now.Sub(r.lastLoginTime) >= loginFailureWindow {
		r.hasLoginFailTimes = 0
	}
	if r.hasLoginFailTimes >= maxLoginFailures {
		return false
	}
	r.hasLoginFailTimes++
	r.lastLoginTime = now
	return true
}

func (r *record) expired(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.lastLoginTime.IsZero() && now.Sub(r.lastLoginTime) >= loginFailureWindow
}

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
		self.Data["json"] = map[string]interface{}{"status": 0, "msg": err.Error()}
	} else {
		self.Data["json"] = map[string]interface{}{"status": 1, "msg": "register success"}
	}
	self.ServeJSON()
}

func (self *LoginController) Out() {
	clearAuthenticationSession(self.DelSession)
	self.Redirect(beego.AppConfig.String("web_base_url")+"/login/index", 302)
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

func clearIprecord() {
	now := time.Now()
	ipRecord.Range(func(key, value interface{}) bool {
		if v, ok := value.(*record); ok && v.expired(now) {
			ipRecord.CompareAndDelete(key, value)
		}
		return true
	})
}
