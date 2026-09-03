package controllers

import (
	"crypto/subtle"
	"errors"
	"html"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehang.io/nps/bridge"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"ehang.io/nps/lib/version"
	"ehang.io/nps/server"
	"github.com/astaxie/beego"
)

type BaseController struct {
	beego.Controller
	controllerName string
	actionName     string
	apiAuthorized  bool
}

const (
	sessionPrincipalKey    = "authPrincipal"
	sessionPrincipalUser   = "user"
	sessionPrincipalClient = "client"
)

// accountQuotaSummary is presentation data for a normal dashboard User. It
// intentionally excludes legacy client-login principals because their single
// client credential is not a user-owned account with self-service quotas.
type accountQuotaSummary struct {
	Visible     bool
	ClientUsed  int
	ClientLimit int
	TunnelUsed  int
	TunnelLimit int
}

// 初始化参数
func (s *BaseController) Prepare() {
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	controllerName, actionName := s.GetControllerAndAction()
	if len(controllerName) > len("Controller") {
		s.controllerName = strings.ToLower(controllerName[0 : len(controllerName)-len("Controller")])
	} else {
		s.controllerName = strings.ToLower(controllerName)
	}
	s.actionName = strings.ToLower(actionName)
	// web api verify
	// param 1 is md5(authKey+Current timestamp)
	// param 2 is timestamp (It's limited to 20 seconds.)
	md5Key := s.getEscapeString("auth_key")
	timestamp := s.GetIntNoErr("timestamp")
	configKey := strings.TrimSpace(beego.AppConfig.String("auth_key"))
	timeNowUnix := time.Now().Unix()
	expectedKey := crypt.Md5(configKey + strconv.Itoa(timestamp))
	apiAuthorized := configKey != "" && md5Key != "" &&
		(math.Abs(float64(timeNowUnix-int64(timestamp))) <= 20) &&
		subtle.ConstantTimeCompare([]byte(expectedKey), []byte(md5Key)) == 1
	// API credentials authorize this request only. Persisting this flag in a
	// browser session turns a short-lived signed API call into a lasting admin
	// login, which is both surprising and unsafe.
	s.apiAuthorized = apiAuthorized
	if !apiAuthorized && !sessionBool(s.GetSession("auth")) {
		// A redirect does not stop Beego from invoking the action by itself, so
		// StopRun is required before returning from Prepare.
		s.Redirect(beego.AppConfig.String("web_base_url")+"/login/index", 302)
		s.StopRun()
		return
	}
	isAdminSession := s.GetSession("isAdmin")
	isAdmin := s.IsAdmin()
	if !apiAuthorized {
		if _, ok := isAdminSession.(bool); !ok {
			// Keep downstream controllers compatible with their historical bool
			// assertions even when a session store returns a string or nil.
			s.SetSession("isAdmin", isAdmin)
		}
	}
	if !isAdmin {
		// A non-admin session must still map to an active principal on every
		// request. User/client status or ownership can change after login, so a
		// cached session alone is not sufficient authorization.
		if !s.hasActiveNonAdminPrincipal() {
			clearAuthenticationSession(s.DelSession)
			s.Redirect(beego.AppConfig.String("web_base_url")+"/login/index", 302)
			s.StopRun()
			return
		}
		s.Data["isAdmin"] = false
		s.Data["username"] = s.GetSession("username")
		s.Data["accountExpireTime"] = s.accountExpireTime()
		s.Data["accountQuota"] = s.accountQuota()
		// Ordinary users cannot browse the administrative user/global pages,
		// but they may call the narrowly-scoped password endpoint to rotate
		// their own dashboard credential from the account popover.
		if s.controllerName == "global" || (s.controllerName == "user" && s.actionName != "changepassword") {
			s.StopRun()
			return
		}
		s.CheckUserAuth()
	} else {
		s.Data["isAdmin"] = true
		s.Data["accountExpireTime"] = ""
		s.Data["accountQuota"] = accountQuotaSummary{}
	}
	s.Data["allow_user_login"], _ = beego.AppConfig.Bool("allow_user_login")
	s.Data["allow_flow_limit"], _ = beego.AppConfig.Bool("allow_flow_limit")
	s.Data["allow_rate_limit"], _ = beego.AppConfig.Bool("allow_rate_limit")
	s.Data["allow_connection_num_limit"], _ = beego.AppConfig.Bool("allow_connection_num_limit")
	s.Data["allow_multi_ip"], _ = beego.AppConfig.Bool("allow_multi_ip")
	s.Data["system_info_display"], _ = beego.AppConfig.Bool("system_info_display")
	s.Data["allow_tunnel_num_limit"], _ = beego.AppConfig.Bool("allow_tunnel_num_limit")
	s.Data["allow_local_proxy"], _ = beego.AppConfig.Bool("allow_local_proxy")
	s.Data["allow_user_change_username"], _ = beego.AppConfig.Bool("allow_user_change_username")
	showHttpProxyPort := beego.AppConfig.DefaultBool("show_http_proxy_port", true)
	httpPort := beego.AppConfig.String("http_proxy_port")
	if httpPort != "80" && showHttpProxyPort {
		s.Data["http_proxy_port"] = ":" + httpPort
	}
	httpsPort := beego.AppConfig.String("https_proxy_port")
	if httpsPort != "" && httpsPort != "443" {
		s.Data["https_proxy_port"] = ":" + httpsPort
	}
}

// IsAdmin returns the effective privilege for the current request. Signed API
// authentication is deliberately request-scoped; browser session elevation is
// kept separate from it.
func (s *BaseController) IsAdmin() bool {
	return isAdminAuthorized(s.apiAuthorized, s.GetSession("isAdmin"))
}

func isAdminAuthorized(apiAuthorized bool, sessionValue interface{}) bool {
	return apiAuthorized || sessionBool(sessionValue)
}

func (s *BaseController) hasActiveNonAdminPrincipal() bool {
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	clientID, _ := sessionInt(s.GetSession("clientId"))

	userActive := userID > 0 && file.GetDb().IsUserActive(userID)
	clientActive := false
	if clientID > 0 {
		if client, err := file.GetDb().GetClient(clientID); err == nil {
			client.RLock()
			clientActive = client.Status && !client.NoDisplay
			clientUserID := client.UserId
			client.RUnlock()
			if clientActive && clientUserID != 0 {
				clientActive = file.GetDb().IsUserActive(clientUserID)
			}
		}
	}
	return activeNonAdminPrincipal(principal, userID, clientID, userActive, clientActive)
}

func activeNonAdminPrincipal(principal string, userID, clientID int, userActive, clientActive bool) bool {
	switch principal {
	case sessionPrincipalUser:
		return userID > 0 && userActive
	case sessionPrincipalClient:
		return clientID > 0 && clientActive
	default:
		// Sessions created before the principal marker existed are deliberately
		// invalidated. They can contain stale user/client identifiers from a
		// previous login and cannot be attributed safely.
		return false
	}
}

// accountExpireTime returns the effective expiry that applies to the signed-in
// non-admin principal. A normal dashboard user is governed by the User
// record. Legacy client-logins can also be bounded by their assigned user, so
// the earlier valid expiry is shown instead of incorrectly claiming that the
// account never expires.
func (s *BaseController) accountExpireTime() string {
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	clientID, _ := sessionInt(s.GetSession("clientId"))

	var userExpire, clientExpire string
	if userID > 0 {
		if user, err := file.GetDb().GetUser(userID); err == nil && user != nil {
			user.RLock()
			userExpire = user.ExpireTime
			user.RUnlock()
		}
	}
	if clientID > 0 {
		if client, err := file.GetDb().GetClient(clientID); err == nil && client != nil {
			client.RLock()
			clientExpire = client.ExpireTime
			if userID == 0 {
				userID = client.UserId
			}
			client.RUnlock()
		}
		if userID > 0 && userExpire == "" {
			if user, err := file.GetDb().GetUser(userID); err == nil && user != nil {
				user.RLock()
				userExpire = user.ExpireTime
				user.RUnlock()
			}
		}
	}

	if principal == sessionPrincipalUser {
		return strings.TrimSpace(userExpire)
	}
	if principal != sessionPrincipalClient {
		return ""
	}
	return earliestExpireTime(clientExpire, userExpire)
}

// accountQuota returns the same allocation counters the server uses when a
// user creates resources. Disabled or expired clients are still counted: they
// remain allocated credentials until an administrator deletes or reassigns
// them, so the sidebar cannot imply that a new slot is available prematurely.
func (s *BaseController) accountQuota() accountQuotaSummary {
	userID, ok := s.currentUserPrincipalID()
	if !ok {
		return accountQuotaSummary{}
	}
	user, err := file.GetDb().GetUser(userID)
	if err != nil || user == nil {
		return accountQuotaSummary{}
	}
	user.RLock()
	clientLimit, tunnelLimit := user.MaxClientNum, user.MaxTunnelNum
	user.RUnlock()
	return accountQuotaSummary{
		Visible:     true,
		ClientUsed:  file.GetDb().GetUserClientNum(userID),
		ClientLimit: clientLimit,
		TunnelUsed:  file.GetDb().GetUserTunnelNum(userID),
		TunnelLimit: tunnelLimit,
	}
}

func earliestExpireTime(values ...string) string {
	var earliest time.Time
	var fallback string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if fallback == "" {
			fallback = value
		}
		if parsed, ok := ParseExpireTime(value); ok && (earliest.IsZero() || parsed.Before(earliest)) {
			earliest = parsed
		}
	}
	if !earliest.IsZero() {
		return earliest.Format("2006-01-02 15:04:05")
	}
	return fallback
}

func clearAuthenticationSession(deleteSession func(interface{})) {
	for _, key := range []string{
		"auth",
		"isAdmin",
		"clientId",
		"clientIds",
		"userId",
		"username",
		sessionPrincipalKey,
	} {
		deleteSession(key)
	}
}

// RequirePost guards state-changing management actions. The browser console
// uses same-origin POST requests, while signed API calls remain usable without
// an Origin header. Requiring both properties for session-backed calls blocks
// form-based CSRF as well as accidental GET mutations.
func (s *BaseController) RequirePost() bool {
	if s.Ctx.Request.Method != http.MethodPost {
		s.rejectRequest(http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if !s.apiAuthorized && !isSameOriginRequest(s.Ctx.Request) {
		s.rejectRequest(http.StatusForbidden, "invalid request origin")
		return false
	}
	return true
}

// RequireAdmin protects operations that alter the client ownership boundary.
// Hiding their buttons in the UI is not authorization; signed API requests
// continue to pass through IsAdmin's request-scoped credential check.
func (s *BaseController) RequireAdmin() bool {
	if s.IsAdmin() {
		return true
	}
	s.rejectRequest(http.StatusForbidden, "permission denied")
	return false
}

func (s *BaseController) rejectRequest(status int, message string) {
	s.Ctx.Output.SetStatus(status)
	s.Data["json"] = ajax(message, 0)
	s.ServeJSON()
}

func isSameOriginRequest(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" || request.Host == "" {
		return false
	}
	expected := requestScheme(request) + "://" + request.Host
	return strings.EqualFold(origin, expected)
}

func requestScheme(request *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		return forwarded
	}
	if request.TLS != nil {
		return "https"
	}
	return "http"
}

// sessionBool accepts the values produced by Beego's session backends. Some
// deployments serialize booleans as strings or numeric flags.
func sessionBool(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		if typed == "1" {
			return true
		}
		if typed == "0" {
			return false
		}
		parsed, err := strconv.ParseBool(typed)
		return err == nil && parsed
	case int:
		return typed != 0
	case int8:
		return typed != 0
	case int16:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint8:
		return typed != 0
	case uint16:
		return typed != 0
	case uint32:
		return typed != 0
	case uint64:
		return typed != 0
	default:
		return false
	}
}

func sessionInt(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	default:
		return 0, false
	}
}

// 加载模板
func (s *BaseController) display(tpl ...string) {
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	var tplname string
	if s.Data["menu"] == nil {
		s.Data["menu"] = s.actionName
	}
	if len(tpl) > 0 {
		tplname = strings.Join([]string{tpl[0], "html"}, ".")
	} else {
		tplname = s.controllerName + "/" + s.actionName + ".html"
	}
	ip := s.Ctx.Request.Host
	s.Data["ip"] = common.GetIpByAddr(ip)

	global := file.GetDb().GetGlobal()
	if global != nil && global.ServerUrl != "" && global.ServerUrl != ip {
		// 替换掉 http:// 或者 https://
		ip = global.ServerUrl
		ip = strings.ReplaceAll(ip, "http://", "")
		ip = strings.ReplaceAll(ip, "https://", "")
		s.Data["ip"] = ip
	}

	s.Data["bridgeType"] = beego.AppConfig.String("bridge_type")
	if common.IsWindows() {
		s.Data["win"] = "npc.exe"
	} else {
		s.Data["win"] = "./npc"
	}

	s.Data["p"] = strconv.Itoa(server.Bridge.TunnelPort)
	s.Data["version"] = version.VERSION
	s.Data["github_repository"] = version.GitHubRepository

	if bridge.ServerTlsEnable {
		tlsPort := strconv.Itoa(beego.AppConfig.DefaultInt("tls_bridge_port", 8025))
		s.Data["tls_p"] = tlsPort
		s.Data["tls_enable"] = true
		s.Data["p1"] = strconv.Itoa(server.Bridge.TunnelPort) + " / " + tlsPort
	} else {
		s.Data["tls_enable"] = false
		s.Data["p1"] = strconv.Itoa(server.Bridge.TunnelPort)
	}

	s.Data["proxyPort"] = beego.AppConfig.String("hostPort")
	s.Layout = "public/layout.html"
	s.TplName = tplname
}

// 错误
func (s *BaseController) error() {
	s.Data["web_base_url"] = beego.AppConfig.String("web_base_url")
	s.Layout = "public/layout.html"
	s.TplName = "public/error.html"
}

// getEscapeString
func (s *BaseController) getEscapeString(key string) string {
	return html.EscapeString(s.GetString(key))
}

// 去掉没有err返回值的int
func (s *BaseController) GetIntNoErr(key string, def ...int) int {
	strv := s.Ctx.Input.Query(key)
	if len(strv) == 0 && len(def) > 0 {
		return def[0]
	}
	val, _ := strconv.Atoi(strv)
	return val
}

// 获取去掉错误的bool值
func (s *BaseController) GetBoolNoErr(key string, def ...bool) bool {
	strv := s.Ctx.Input.Query(key)
	if len(strv) == 0 && len(def) > 0 {
		return def[0]
	}
	val, _ := strconv.ParseBool(strv)
	return val
}

// ajax正确返回
func (s *BaseController) AjaxOk(str string) {
	s.Data["json"] = ajax(str, 1)
	s.ServeJSON()
	s.StopRun()
}

// ajax正确返回
func (s *BaseController) AjaxOkWithId(str string, id int) {
	s.Data["json"] = ajaxWithId(str, 1, id)
	s.ServeJSON()
	s.StopRun()
}

// ajax错误返回
func (s *BaseController) AjaxErr(str string) {
	s.Data["json"] = ajax(str, 0)
	s.ServeJSON()
	s.StopRun()
}

// 组装ajax
func ajax(str string, status int) map[string]interface{} {
	json := make(map[string]interface{})
	json["status"] = status
	json["msg"] = str
	return json
}

// 组装ajax
func ajaxWithId(str string, status int, id int) map[string]interface{} {
	json := make(map[string]interface{})
	json["status"] = status
	json["msg"] = str
	json["id"] = id
	return json
}

// ajax table返回
func (s *BaseController) AjaxTable(list interface{}, cnt int, recordsTotal int, kwargs map[string]interface{}) {
	json := make(map[string]interface{})
	json["rows"] = list
	json["total"] = recordsTotal
	if kwargs != nil {
		for k, v := range kwargs {
			if v != nil {
				json[k] = v
			}
		}
	}
	s.Data["json"] = json
	s.ServeJSON()
	s.StopRun()
}

// ajax table参数
func (s *BaseController) GetAjaxParams() (start, limit int) {
	return s.GetIntNoErr("offset"), s.GetIntNoErr("limit")
}

// getOwnerListFilter parses the optional administrator list filter. The empty
// value means "all owners"; an explicit user_id=0 selects resources attached
// to unassigned clients. Controllers call this only after their admin check,
// so ordinary users cannot widen the ownership scope of their list requests.
func (s *BaseController) getOwnerListFilter() (*file.OwnerFilter, error) {
	raw := strings.TrimSpace(s.GetString("user_id"))
	if raw == "" {
		if s.GetBoolNoErr("unassigned") {
			unassigned := 0
			return &file.OwnerFilter{UserID: &unassigned}, nil
		}
		return nil, nil
	}
	userID, err := strconv.Atoi(raw)
	if err != nil || userID < 0 {
		return nil, errors.New("所属用户筛选无效")
	}
	return &file.OwnerFilter{UserID: &userID}, nil
}

func (s *BaseController) SetInfo(name string) {
	s.Data["name"] = name
}

// setOwnerFilterData supplies the administrator-only owner options used by
// resource list toolbars. Keeping this in the shared controller avoids
// duplicating the user lookup across client, tunnel, and host list actions;
// ordinary users never receive the list because their UI is already scoped to
// their own principal.
func (s *BaseController) setOwnerFilterData() {
	if !s.IsAdmin() {
		return
	}
	s.Data["users"], _ = file.GetDb().GetUserList(0, 10000, "")
}

// setClientFilterData supplies the administrator-only client options used by
// tunnel list toolbars. The list endpoint still enforces ownership and status
// on every request; these options are only presentation data for the filter.
func (s *BaseController) setClientFilterData() {
	if !s.IsAdmin() {
		return
	}
	s.Data["clients"], _ = file.GetDb().GetClientList(0, 10000, "", "", "", 0)
}

func (s *BaseController) SetType(name string) {
	s.Data["type"] = name
}

func (s *BaseController) CheckUserAuth() {
	allowedClientIds := s.GetAllowedClientIds()
	if s.controllerName == "client" {
		if s.actionName == "add" {
			// A dashboard User may create an additional client for itself. A
			// legacy client-login principal only represents one existing client
			// and must never be able to mint another one.
			if _, ok := s.currentUserPrincipalID(); !ok {
				s.StopRun()
			}
			return
		}
		if id := s.GetIntNoErr("id"); id != 0 {
			if !isAllowedClient(id, allowedClientIds) {
				s.StopRun()
				return
			}
		}
	}
	if s.controllerName == "index" {
		if clientId := s.GetIntNoErr("client_id"); clientId != 0 && !isAllowedClient(clientId, allowedClientIds) {
			s.StopRun()
			return
		}
		if id := s.GetIntNoErr("id"); id != 0 {
			belong := false
			if strings.Contains(s.actionName, "h") {
				if v, ok := file.GetDb().JsonDb.Hosts.Load(id); ok {
					host, valid := v.(*file.Host)
					if valid && host != nil {
						host.RLock()
						client := host.Client
						host.RUnlock()
						if client != nil {
							client.RLock()
							clientID := client.Id
							client.RUnlock()
							belong = isAllowedClient(clientID, allowedClientIds)
						}
					}
				}
			} else {
				if v, ok := file.GetDb().JsonDb.Tasks.Load(id); ok {
					task, valid := v.(*file.Tunnel)
					if valid && task != nil {
						task.RLock()
						client := task.Client
						task.RUnlock()
						if client != nil {
							client.RLock()
							clientID := client.Id
							client.RUnlock()
							belong = isAllowedClient(clientID, allowedClientIds)
						}
					}
				}
			}
			if !belong {
				s.StopRun()
			}
		}
	}
}

// currentUserPrincipalID returns the persisted dashboard User identity for
// the current session. It deliberately excludes the legacy per-client login
// principal, which has a client ID but no authority to create new clients.
func (s *BaseController) currentUserPrincipalID() (int, bool) {
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	if principal != sessionPrincipalUser || userID <= 0 {
		return 0, false
	}
	return userID, true
}

func (s *BaseController) GetAllowedClientIds() map[int]struct{} {
	principal, _ := s.GetSession(sessionPrincipalKey).(string)
	userID, _ := sessionInt(s.GetSession("userId"))
	clientID, _ := sessionInt(s.GetSession("clientId"))
	legacyIDs, _ := s.GetSession("clientIds").(map[int]struct{})
	return allowedClientIDsForPrincipal(principal, userID, clientID, legacyIDs, file.GetDb().UserClientIds)
}

// allowedClientIDsForPrincipal reads a user's membership from the database on
// every request. This makes reassignment and revocation effective immediately
// rather than when the next session is created.
func allowedClientIDsForPrincipal(principal string, userID, clientID int, legacyIDs map[int]struct{}, loadUserClientIDs func(int) map[int]struct{}) map[int]struct{} {
	switch principal {
	case sessionPrincipalUser:
		if userID > 0 {
			return loadUserClientIDs(userID)
		}
	case sessionPrincipalClient:
		if clientID > 0 {
			return map[int]struct{}{clientID: {}}
		}
	}
	if legacyIDs != nil {
		return legacyIDs
	}
	return map[int]struct{}{}
}

func isAllowedClient(id int, allowedClientIds map[int]struct{}) bool {
	_, ok := allowedClientIds[id]
	return ok
}
